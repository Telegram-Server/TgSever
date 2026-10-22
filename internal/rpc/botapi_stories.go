package rpc

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"unicode/utf8"
)

type botAPIStoryPersistence interface {
	CreateBotStory(context.Context, int64, domain.StoryCreateRequest, domain.BotStoryOwner) (domain.StoryCreateResult, error)
	BotStoryOwner(context.Context, domain.Peer, int) (domain.BotStoryOwner, error)
}

func (r *Router) botAPIStoryProvenance(ctx context.Context, peer domain.Peer, id int) (domain.BotStoryOwner, error) {
	persistence, ok := r.deps.Stories.(botAPIStoryPersistence)
	if !ok {
		return domain.BotStoryOwner{}, storyIDInvalidErr()
	}
	return persistence.BotStoryOwner(ctx, peer, id)
}

func (r *Router) botAPIStoryMedia(ctx context.Context, content domain.BotStoryContent) (*domain.MessageMedia, error) {
	files, ok := r.deps.Files.(interface {
		CreateStoryMedia(context.Context, domain.BotStoryContent) (*domain.MessageMedia, error)
	})
	if !ok {
		return nil, errors.New("STORY_CONTENT_INVALID")
	}
	return files.CreateStoryMedia(ctx, content)
}

func (r *Router) botAPIStoryOwner(ctx context.Context, botID int64, connectionID string) (int64, error) {
	owner, err := r.botAPIBusinessOwner(ctx, botID, connectionID, "can_manage_stories")
	if err != nil {
		return 0, err
	}
	if r.deps.Stories == nil {
		return 0, errors.New("STORY_ID_INVALID")
	}
	return owner, nil
}

func (r *Router) botAPIManagedStory(ctx context.Context, botID, ownerID int64, id int) (domain.Story, error) {
	if id <= 0 || id > domain.MaxStoryID {
		return domain.Story{}, storyIDInvalidErr()
	}
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}
	provenance, err := r.botAPIStoryProvenance(ctx, peer, id)
	if err != nil {
		return domain.Story{}, storyErr(err)
	}
	if provenance.BotID != botID {
		return domain.Story{}, storyIDInvalidErr()
	}
	list, err := r.deps.Stories.GetStoriesByID(ctx, ownerID, peer, []int{id}, int(r.clock.Now().Unix()))
	if err != nil {
		return domain.Story{}, storyErr(err)
	}
	if len(list.Stories) != 1 || list.Stories[0].Deleted || list.Stories[0].Owner != peer || list.Stories[0].ID != id {
		return domain.Story{}, storyIDInvalidErr()
	}
	return list.Stories[0], nil
}

func validateBotAPIStoryText(input domain.BotStoryInput) error {
	if !utf8.ValidString(input.Caption) || utf8.RuneCountInString(input.Caption) > 2048 {
		return errors.New("STORY_CAPTION_TOO_LONG")
	}
	entities := tgMessageEntities(input.Entities)
	if len(entities) != len(input.Entities) {
		return errors.New("ENTITY_TYPE_INVALID")
	}
	if err := validateStoryCaptionEntities(input.Caption, entities); err != nil {
		return err
	}
	return domain.ValidateBotStoryAreas(input.Areas)
}

func (r *Router) botAPICreateStory(ctx context.Context, botID, ownerID int64, input domain.BotStoryInput, media *domain.MessageMedia, forward *domain.StoryForward) (domain.Story, error) {
	if !domain.ValidBotStoryPeriod(input.ActivePeriod) {
		return domain.Story{}, storyPeriodInvalidErr()
	}
	persistence, ok := r.deps.Stories.(botAPIStoryPersistence)
	if !ok {
		return domain.Story{}, storyIDInvalidErr()
	}
	result, err := persistence.CreateBotStory(ctx, ownerID, domain.StoryCreateRequest{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}, RandomID: randomNonZeroInt64(), Date: int(r.clock.Now().Unix()), Period: input.ActivePeriod,
		Pinned: input.PostToChatPage, Public: true, NoForwards: input.ProtectContent,
		PrivacyRules: []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowAll}},
		Caption:      input.Caption, Entities: input.Entities, Media: media, MediaAreas: input.Areas, Forward: forward,
	}, domain.BotStoryOwner{BotID: botID, BusinessConnectionID: input.BusinessConnectionID})
	if err != nil {
		return domain.Story{}, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(result.Story.Owner)
	if err := r.recordStoryChange(ctx, ownerID, result.Story); err != nil {
		return domain.Story{}, err
	}
	return result.Story, nil
}

func (r *Router) BotAPIPostStory(ctx context.Context, botID int64, input domain.BotStoryInput) (domain.Story, error) {
	ownerID, err := r.botAPIStoryOwner(ctx, botID, input.BusinessConnectionID)
	if err != nil {
		return domain.Story{}, err
	}
	if !domain.ValidBotStoryPeriod(input.ActivePeriod) {
		return domain.Story{}, storyPeriodInvalidErr()
	}
	if err := validateBotAPIStoryText(input); err != nil {
		return domain.Story{}, err
	}
	if r.deps.Files == nil {
		return domain.Story{}, errors.New("STORY_CONTENT_INVALID")
	}
	media, err := r.botAPIStoryMedia(ctx, input.Content)
	if err != nil {
		return domain.Story{}, err
	}
	// Media validation may run an external decoder; re-check revocation before mutation.
	ownerID, err = r.botAPIStoryOwner(ctx, botID, input.BusinessConnectionID)
	if err != nil {
		return domain.Story{}, err
	}
	return r.botAPICreateStory(ctx, botID, ownerID, input, media, nil)
}

func (r *Router) BotAPIRepostStory(ctx context.Context, botID int64, input domain.BotStoryInput) (domain.Story, error) {
	ownerID, err := r.botAPIStoryOwner(ctx, botID, input.BusinessConnectionID)
	if err != nil {
		return domain.Story{}, err
	}
	if !domain.ValidBotStoryPeriod(input.ActivePeriod) {
		return domain.Story{}, storyPeriodInvalidErr()
	}
	if input.FromChatID <= 0 || input.FromChatID == ownerID || input.FromStoryID <= 0 || input.FromStoryID > domain.MaxStoryID {
		return domain.Story{}, storyIDInvalidErr()
	}
	sourcePeer := domain.Peer{Type: domain.PeerTypeUser, ID: input.FromChatID}
	provenance, err := r.botAPIStoryProvenance(ctx, sourcePeer, input.FromStoryID)
	if err != nil {
		return domain.Story{}, storyErr(err)
	}
	if provenance.BotID != botID {
		return domain.Story{}, storyIDInvalidErr()
	}
	business, ok := r.accountBusinessAutomation()
	if !ok {
		return domain.Story{}, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	connection, found, err := business.GetConnectedBusinessBot(ctx, input.FromChatID)
	if err != nil {
		return domain.Story{}, err
	}
	if !found || connection.BotUserID != botID {
		return domain.Story{}, errors.New("BUSINESS_CONNECTION_INVALID")
	}
	sourceOwner, err := r.botAPIBusinessOwner(ctx, botID, connection.ConnectionID, "can_manage_stories")
	if err != nil {
		return domain.Story{}, err
	}
	if sourceOwner != input.FromChatID {
		return domain.Story{}, storyIDInvalidErr()
	}
	// Read as the destination owner, not as the source: management isn't a privacy bypass.
	list, err := r.deps.Stories.GetStoriesByID(ctx, ownerID, sourcePeer, []int{input.FromStoryID}, int(r.clock.Now().Unix()))
	if err != nil {
		return domain.Story{}, storyErr(err)
	}
	if len(list.Stories) != 1 {
		return domain.Story{}, storyIDInvalidErr()
	}
	source := list.Stories[0]
	if source.Owner != sourcePeer || source.ID != input.FromStoryID || !source.Interactable(int(r.clock.Now().Unix())) || source.Media == nil {
		return domain.Story{}, storyIDInvalidErr()
	}
	if source.NoForwards {
		return domain.Story{}, chatForwardsRestrictedErr()
	}
	forward := &domain.StoryForward{Source: sourcePeer, From: sourcePeer, StoryID: source.ID}
	r.applyStoryForwardAuthorPrivacy(ctx, ownerID, forward)
	input.Caption, input.Entities, input.Areas = source.Caption, source.Entities, source.MediaAreas
	return r.botAPICreateStory(ctx, botID, ownerID, input, source.Media, forward)
}

func (r *Router) BotAPIEditStory(ctx context.Context, botID int64, input domain.BotStoryInput) (domain.Story, error) {
	ownerID, err := r.botAPIStoryOwner(ctx, botID, input.BusinessConnectionID)
	if err != nil {
		return domain.Story{}, err
	}
	if _, err := r.botAPIManagedStory(ctx, botID, ownerID, input.StoryID); err != nil {
		return domain.Story{}, err
	}
	if err := validateBotAPIStoryText(input); err != nil {
		return domain.Story{}, err
	}
	if r.deps.Files == nil {
		return domain.Story{}, errors.New("STORY_CONTENT_INVALID")
	}
	media, err := r.botAPIStoryMedia(ctx, input.Content)
	if err != nil {
		return domain.Story{}, err
	}
	ownerID, err = r.botAPIStoryOwner(ctx, botID, input.BusinessConnectionID)
	if err != nil {
		return domain.Story{}, err
	}
	result, err := r.deps.Stories.EditStory(ctx, ownerID, domain.StoryEditRequest{
		Owner: domain.Peer{Type: domain.PeerTypeUser, ID: ownerID}, ID: input.StoryID,
		Media: media, UpdateMedia: true, Caption: input.Caption, Entities: input.Entities, UpdateCaption: true, MediaAreas: input.Areas, UpdateMediaAreas: true,
	})
	if err != nil {
		return domain.Story{}, storyErr(err)
	}
	r.invalidateStoryProjectionCacheForPeer(result.Story.Owner)
	if err := r.recordStoryChange(ctx, ownerID, result.Story); err != nil {
		return domain.Story{}, err
	}
	return result.Story, nil
}

func (r *Router) BotAPIDeleteStory(ctx context.Context, botID int64, connectionID string, id int) (bool, error) {
	ownerID, err := r.botAPIStoryOwner(ctx, botID, connectionID)
	if err != nil {
		return false, err
	}
	previous, err := r.botAPIManagedStory(ctx, botID, ownerID, id)
	if err != nil {
		return false, err
	}
	now := int(r.clock.Now().Unix())
	result, err := r.deps.Stories.DeleteStories(ctx, ownerID, previous.Owner, []int{id}, now)
	if err != nil {
		return false, storyErr(err)
	}
	if len(result.Stories) != 1 {
		return false, storyIDInvalidErr()
	}
	r.invalidateStoryProjectionCacheForPeer(previous.Owner)
	for _, story := range result.Stories {
		if err := r.recordStoryChange(ctx, ownerID, story); err != nil {
			return false, err
		}
		if err := r.fanoutDeletedUserStory(ctx, ownerID, previous, story, now); err != nil {
			return false, err
		}
	}
	return true, nil
}

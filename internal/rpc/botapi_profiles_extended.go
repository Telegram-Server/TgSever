package rpc

import (
	"context"
	"errors"
	"telesrv/internal/domain"
)

func (r *Router) botAPIProfileUser(ctx context.Context, botID, userID int64) (domain.User, error) {
	if r == nil || !r.userIsBot(ctx, botID) || r.deps.Users == nil {
		return domain.User{}, errors.New("BOT_INVALID")
	}
	u, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return domain.User{}, err
	}
	if !found {
		return domain.User{}, errors.New("USER_ID_INVALID")
	}
	return u, nil
}
func (r *Router) BotAPIGetUserProfilePhotos(ctx context.Context, botID, userID int64, offset, limit int) ([]domain.Photo, int, error) {
	u, err := r.botAPIProfileUser(ctx, botID, userID)
	if err != nil {
		return nil, 0, err
	}
	if offset < 0 || limit < 1 || limit > 100 {
		return nil, 0, errors.New("LIMIT_INVALID")
	}
	if r.deps.Files == nil {
		return nil, 0, notImplementedErr()
	}
	if u.Deleted && u.ID != botID {
		return nil, 0, nil
	}
	visible, err := r.canSeeProfilePhotoAlbum(ctx, botID, u.ID)
	if err != nil {
		return nil, 0, err
	}
	if !visible {
		return r.hiddenProfilePhotoAlbum(ctx, u.ID, offset, limit, 0)
	}
	photos, total, err := r.deps.Files.GetProfilePhotos(ctx, domain.PeerTypeUser, u.ID, offset, limit, 0)
	if err == nil && total == 0 && u.ID != botID {
		return r.hiddenProfilePhotoAlbum(ctx, u.ID, offset, limit, 0)
	}
	return photos, total, err
}
func (r *Router) BotAPIGetUserProfileAudios(ctx context.Context, botID, userID int64, offset, limit int) (domain.SavedMusicList, error) {
	u, err := r.botAPIProfileUser(ctx, botID, userID)
	if err != nil {
		return domain.SavedMusicList{}, err
	}
	if offset < 0 || limit < 1 || limit > 100 {
		return domain.SavedMusicList{}, errors.New("LIMIT_INVALID")
	}
	allowed, err := r.canSeeSavedMusic(ctx, botID, u)
	if err != nil || !allowed {
		return domain.SavedMusicList{}, err
	}
	if r.deps.Account == nil {
		return domain.SavedMusicList{}, notImplementedErr()
	}
	return r.deps.Account.ListSavedMusic(ctx, u.ID, offset, limit)
}
func (r *Router) BotAPIGetUserPersonalChatMessages(ctx context.Context, botID, userID int64, limit int) ([]domain.Message, []domain.User, []domain.Channel, error) {
	u, err := r.botAPIProfileUser(ctx, botID, userID)
	if err != nil {
		return nil, nil, nil, err
	}
	if limit < 1 || limit > 20 {
		return nil, nil, nil, errors.New("LIMIT_INVALID")
	}
	if u.Deleted {
		return nil, nil, nil, nil
	}
	if r.deps.Channels == nil {
		return nil, nil, nil, notImplementedErr()
	}
	view, found := r.resolvePersonalChannel(ctx, botID, u.ID, u.PersonalChannelID)
	if !found {
		return nil, nil, nil, nil
	}
	history, err := r.deps.Channels.GetPersonalChannelHistory(ctx, botID, u.ID, domain.ChannelHistoryFilter{ChannelID: view.Channel.ID, Limit: limit})
	if err != nil {
		return nil, nil, nil, err
	}
	out := make([]domain.Message, 0, len(history.Messages))
	for _, m := range history.Messages {
		out = append(out, botAPIMessageFromChannel(botID, m))
	}
	return out, history.Users, append(history.Channels, history.Channel), nil
}
func (r *Router) BotAPIGetUserChatBoosts(ctx context.Context, botID, chatID, userID int64) ([]domain.PremiumBoostSlot, []domain.User, error) {
	if _, err := r.botAPIProfileUser(ctx, botID, userID); err != nil {
		return nil, nil, err
	}
	input, _, err := r.botAPIInputPeer(ctx, botID, chatID)
	if err != nil {
		return nil, nil, err
	}
	_, view, err := r.premiumBoostChannelView(WithUserID(ctx, botID), input, true)
	if err != nil {
		return nil, nil, err
	}
	list, err := r.deps.Channels.GetPremiumUserBoosts(ctx, botID, view.Channel.ID, userID, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, nil, premiumBoostErr(err)
	}
	users := r.domainUsersForIDs(ctx, botID, premiumBoostUserIDs(list.Boosts))
	return list.Boosts, users, nil
}
func (r *Router) BotAPISetMyDefaultAdministratorRights(ctx context.Context, botID int64, channels bool, rights domain.ChannelAdminRights) error {
	if r == nil || !r.userIsBot(ctx, botID) || r.deps.Bots == nil {
		return errors.New("BOT_INVALID")
	}
	_, err := r.deps.Bots.SetBotDefaultAdminRights(ctx, botID, channels, rights)
	return err
}
func (r *Router) BotAPISetMyProfilePhoto(ctx context.Context, botID int64, data []byte, animated bool, timestamp float64) error {
	if r == nil || !r.userIsBot(ctx, botID) {
		return errors.New("BOT_INVALID")
	}
	if r.deps.Files == nil {
		return notImplementedErr()
	}
	var photo domain.Photo
	var err error
	if animated {
		photo, err = r.deps.Files.CreateAvatarVideoFromBytes(ctx, data, timestamp)
	} else {
		photo, err = r.deps.Files.CreateAvatarFromBytes(ctx, data)
	}
	if err != nil {
		return err
	}
	photo, found, err := r.deps.Files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, botID, domain.ProfilePhotoKindProfile, photo.ID, int(r.clock.Now().Unix()))
	if err != nil {
		return err
	}
	if !found {
		return photoInvalidErr()
	}
	r.photosPhotoForSelf(ctx, botID, photo, domain.ProfilePhotoKindProfile)
	return nil
}
func (r *Router) BotAPIRemoveMyProfilePhoto(ctx context.Context, botID int64) error {
	if r == nil || !r.userIsBot(ctx, botID) {
		return errors.New("BOT_INVALID")
	}
	if r.deps.Files == nil {
		return notImplementedErr()
	}
	photo, found, err := r.deps.Files.CurrentProfilePhoto(ctx, domain.PeerTypeUser, botID)
	if err != nil {
		return err
	}
	if !found {
		return nil
	}
	if _, err = r.deps.Files.DeleteProfilePhotosKind(ctx, domain.PeerTypeUser, botID, domain.ProfilePhotoKindProfile, []int64{photo.ID}); err != nil {
		return err
	}
	current, _, err := r.deps.Files.CurrentProfilePhoto(ctx, domain.PeerTypeUser, botID)
	if err != nil {
		return err
	}
	r.photosPhotoForSelf(ctx, botID, current, domain.ProfilePhotoKindProfile)
	return nil
}

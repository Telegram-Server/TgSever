package rpc

import (
	appaccount "telesrv/internal/app/account"
	appstories "telesrv/internal/app/stories"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
	"testing"
)

func TestBotAPIStoryRepostPrivacyOwnershipAndRevocation(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	passwords := memory.NewPasswordStore()
	account := appaccount.NewService(passwords, appaccount.WithBusinessAutomation(passwords))
	stories := memory.NewStoryStore()
	f.router.deps.Account = account
	f.router.deps.Stories = appstories.NewService(stories)
	connect := func(ownerID int64) domain.ConnectedBusinessBot {
		connection, err := account.SaveConnectedBusinessBot(f.ctx, ownerID, domain.ConnectedBusinessBot{BotUserID: f.bot.ID, Recipients: domain.BusinessBotRecipients{ExistingChats: true, NewChats: true}, Rights: domain.BusinessBotRights{ManageStories: true}})
		if err != nil {
			t.Fatal(err)
		}
		return connection
	}
	sourceConnection, destinationConnection := connect(f.owner.ID), connect(f.member.ID)
	now := int(f.router.clock.Now().Unix())
	source, err := stories.CreateBotStory(f.ctx, domain.StoryCreateRequest{Owner: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, RandomID: 23, Date: now, Period: 86400, Public: true, Caption: "source", Media: &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: 123}}}, domain.BotStoryOwner{BotID: f.bot.ID, BusinessConnectionID: sourceConnection.ConnectionID})
	if err != nil {
		t.Fatal(err)
	}
	input := domain.BotStoryInput{BusinessConnectionID: destinationConnection.ConnectionID, FromChatID: f.owner.ID, FromStoryID: source.Story.ID, ActivePeriod: 21600, PostToChatPage: true}
	repost, err := f.router.BotAPIRepostStory(f.ctx, f.bot.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if repost.Owner.ID != f.member.ID || repost.Forward == nil || repost.Forward.Source != source.Story.Owner || repost.Forward.StoryID != source.Story.ID || repost.Caption != "source" || !repost.Pinned || repost.ExpireDate-now != 21600 {
		t.Fatalf("repost = %+v", repost)
	}
	provenance, err := stories.BotStoryOwner(f.ctx, repost.Owner, repost.ID)
	if err != nil || provenance.BotID != f.bot.ID || provenance.BusinessConnectionID != destinationConnection.ConnectionID {
		t.Fatalf("provenance = %+v, %v", provenance, err)
	}
	changeSource := func(story domain.Story) {
		t.Helper()
		if _, err := stories.UpsertStory(f.ctx, domain.UpsertStoryRequest{Story: story}); err != nil {
			t.Fatal(err)
		}
	}
	protected := source.Story
	protected.NoForwards = true
	changeSource(protected)
	if _, err := f.router.BotAPIRepostStory(f.ctx, f.bot.ID, input); !containsErr(err, "CHAT_FORWARDS_RESTRICTED") {
		t.Fatalf("protected err = %v", err)
	}
	private := source.Story
	private.Public = false
	changeSource(private)
	if _, err := f.router.BotAPIRepostStory(f.ctx, f.bot.ID, input); err == nil {
		t.Fatal("private story was reposted")
	}
	other, err := stories.CreateBotStory(f.ctx, domain.StoryCreateRequest{Owner: source.Story.Owner, RandomID: 24, Date: now, Period: 86400, Public: true, Media: source.Story.Media}, domain.BotStoryOwner{BotID: f.bot.ID + 100, BusinessConnectionID: sourceConnection.ConnectionID})
	if err != nil {
		t.Fatal(err)
	}
	otherInput := input
	otherInput.FromStoryID = other.Story.ID
	if _, err := f.router.BotAPIRepostStory(f.ctx, f.bot.ID, otherInput); err == nil {
		t.Fatal("another bot's story was reposted")
	}
	if _, err := f.router.BotAPIDeleteStory(f.ctx, f.bot.ID, sourceConnection.ConnectionID, other.Story.ID); err == nil {
		t.Fatal("another bot's story was deleted")
	}
	changeSource(source.Story)
	sourceConnection.Rights.ManageStories = false
	if _, err := account.SaveConnectedBusinessBot(f.ctx, f.owner.ID, sourceConnection); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.BotAPIRepostStory(f.ctx, f.bot.ID, input); !containsErr(err, "BUSINESS_BOT_RIGHT_REQUIRED") {
		t.Fatalf("source rights err = %v", err)
	}
	if _, err := account.DeleteConnectedBusinessBot(f.ctx, f.member.ID, f.bot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.BotAPIDeleteStory(f.ctx, f.bot.ID, destinationConnection.ConnectionID, repost.ID); err == nil {
		t.Fatal("revoked connection deleted story")
	}
	remaining, err := stories.GetStoriesByID(f.ctx, f.member.ID, repost.Owner, []int{repost.ID}, now)
	if err != nil || len(remaining.Stories) != 1 || remaining.Stories[0].Deleted {
		t.Fatalf("revoked delete changed story: %+v, %v", remaining, err)
	}
}

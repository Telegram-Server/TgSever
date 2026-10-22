package rpc

import (
	"bytes"
	"context"
	"go.uber.org/zap/zaptest"
	"image"
	"image/color"
	"image/png"
	appaccount "telesrv/internal/app/account"
	appfiles "telesrv/internal/app/files"
	appstories "telesrv/internal/app/stories"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/postgres"
	"telesrv/internal/store/postgres/pgtest"
	"testing"
	"time"
)

func TestBotAPIStoryBusinessLifecyclePostgres(t *testing.T) {
	ctx := context.Background()
	pool, err := postgres.Open(ctx, pgtest.TestDSN(t))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(pool.Close)
	users := postgres.NewUserStore(pool)
	createUser := func(phone, name string) domain.User {
		t.Helper()
		u, err := users.Create(ctx, domain.User{Phone: phone, FirstName: name, AccessHash: 101})
		if err != nil {
			t.Fatal(err)
		}
		return u
	}
	sourceOwner, destinationOwner := createUser("+19880008001", "Source"), createUser("+19880008002", "Destination")
	bot, _, err := postgres.NewBotStore(pool).CreateBotAccount(ctx, domain.User{FirstName: "Stories", Username: "storylifebot", AccessHash: 102}, domain.BotProfile{OwnerUserID: sourceOwner.ID, TokenSecret: "story-secret"})
	if err != nil {
		t.Fatal(err)
	}
	passwords := postgres.NewPasswordStore(pool)
	account := appaccount.NewService(passwords, appaccount.WithBusinessAutomation(passwords))
	connect := func(owner int64) domain.ConnectedBusinessBot {
		t.Helper()
		c, err := account.SaveConnectedBusinessBot(ctx, owner, domain.ConnectedBusinessBot{BotUserID: bot.ID, Recipients: domain.BusinessBotRecipients{ExistingChats: true, NewChats: true}, Rights: domain.BusinessBotRights{ManageStories: true}})
		if err != nil {
			t.Fatal(err)
		}
		return c
	}
	sourceConnection, destinationConnection := connect(sourceOwner.ID), connect(destinationOwner.ID)
	blobs, err := appfiles.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	storyStore := postgres.NewStoryStore(pool)
	router := New(Config{DC: 2}, Deps{Users: appusers.NewService(users), Account: account, Stories: appstories.NewService(storyStore), Files: appfiles.NewService(postgres.NewMediaStore(pool), blobs, 2)}, zaptest.NewLogger(t), fixedClock{now: time.Unix(1800000000, 0)})
	content := func(red uint8) domain.BotStoryContent {
		picture := image.NewRGBA(image.Rect(0, 0, 1080, 1920))
		picture.SetRGBA(0, 0, color.RGBA{R: red, A: 255})
		var b bytes.Buffer
		if err := png.Encode(&b, picture); err != nil {
			t.Fatal(err)
		}
		return domain.BotStoryContent{Type: "photo", File: domain.BotAPIFileInput{Bytes: b.Bytes(), FileName: "story.png", MimeType: "image/png"}}
	}
	input := domain.BotStoryInput{BusinessConnectionID: sourceConnection.ConnectionID, Content: content(1), ActivePeriod: 43200, Caption: "first", PostToChatPage: true, Areas: []domain.StoryMediaArea{{Kind: domain.StoryMediaAreaURL, Coordinates: domain.StoryMediaAreaCoordinates{X: 50, Y: 50, W: 20, H: 10}, URL: "https://example.org"}}}
	posted, err := router.BotAPIPostStory(ctx, bot.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if posted.Owner.ID != sourceOwner.ID || posted.ExpireDate-posted.Date != 43200 || !posted.Pinned || posted.Media == nil || posted.Media.Photo == nil {
		t.Fatalf("posted = %+v", posted)
	}
	input.StoryID, input.Content, input.Caption, input.Areas = posted.ID, content(2), "edited", nil
	edited, err := router.BotAPIEditStory(ctx, bot.ID, input)
	if err != nil {
		t.Fatal(err)
	}
	if edited.Media.Photo.ID == posted.Media.Photo.ID || edited.Caption != "edited" || !edited.Edited || len(edited.MediaAreas) != 0 || edited.ExpireDate != posted.ExpireDate {
		t.Fatalf("edited = %+v", edited)
	}
	if _, err := router.BotAPIEditStory(ctx, bot.ID, domain.BotStoryInput{BusinessConnectionID: destinationConnection.ConnectionID, StoryID: posted.ID, Content: content(3)}); err == nil {
		t.Fatal("different owner edited source")
	}
	repost, err := router.BotAPIRepostStory(ctx, bot.ID, domain.BotStoryInput{BusinessConnectionID: destinationConnection.ConnectionID, FromChatID: sourceOwner.ID, FromStoryID: posted.ID, ActivePeriod: 21600, ProtectContent: true})
	if err != nil {
		t.Fatal(err)
	}
	if repost.Owner.ID != destinationOwner.ID || repost.Forward == nil || repost.Forward.Source.ID != sourceOwner.ID || repost.Caption != "edited" || repost.Media.Photo.ID != edited.Media.Photo.ID || !repost.NoForwards {
		t.Fatalf("repost = %+v", repost)
	}
	// Reload through new store/service instances: bot ownership must survive process-local state.
	router.deps.Stories = appstories.NewService(postgres.NewStoryStore(pool))
	if _, err := account.DeleteConnectedBusinessBot(ctx, destinationOwner.ID, bot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := router.BotAPIDeleteStory(ctx, bot.ID, destinationConnection.ConnectionID, repost.ID); err == nil {
		t.Fatal("revoked connection deleted repost")
	}
	revokedInput := domain.BotStoryInput{BusinessConnectionID: destinationConnection.ConnectionID, StoryID: repost.ID, ActivePeriod: 21600, Content: content(3)}
	if _, err := router.BotAPIPostStory(ctx, bot.ID, revokedInput); err == nil {
		t.Fatal("revoked connection posted story")
	}
	if _, err := router.BotAPIEditStory(ctx, bot.ID, revokedInput); err == nil {
		t.Fatal("revoked connection edited story")
	}
	revokedInput.FromChatID, revokedInput.FromStoryID = sourceOwner.ID, posted.ID
	if _, err := router.BotAPIRepostStory(ctx, bot.ID, revokedInput); err == nil {
		t.Fatal("revoked connection reposted story")
	}
	destinationConnection = connect(destinationOwner.ID)
	if ok, err := router.BotAPIDeleteStory(ctx, bot.ID, destinationConnection.ConnectionID, repost.ID); err != nil || !ok {
		t.Fatalf("delete repost = %v, %v", ok, err)
	}
	list, err := storyStore.GetStoriesByID(ctx, destinationOwner.ID, repost.Owner, []int{repost.ID}, 1800000000)
	if err != nil {
		t.Fatal(err)
	}
	for _, story := range list.Stories {
		if !story.Deleted {
			t.Fatal("deleted repost remains visible")
		}
	}
	if ok, err := router.BotAPIDeleteStory(ctx, bot.ID, sourceConnection.ConnectionID, posted.ID); err != nil || !ok {
		t.Fatalf("delete source = %v, %v", ok, err)
	}
}

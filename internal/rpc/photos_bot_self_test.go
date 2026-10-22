package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
	"testing"
)

func TestBotSelfAvatarNotifiesOwnerRPC(t *testing.T) {
	f := newInlineBotRPCTestFixture(t)
	sessions := &captureSessions{}
	f.router.deps.Sessions = sessions
	ctx := WithUserID(context.Background(), f.bot.ID)
	assertOwner := func(t *testing.T, photoID int64) {
		t.Helper()
		recipients := sessions.pushedUserIDs()
		ownerReceived := false
		for _, id := range recipients {
			if id == f.owner.ID {
				ownerReceived = true
			}
		}
		if !ownerReceived {
			t.Fatalf("avatar update recipients = %v, missing owner %d", recipients, f.owner.ID)
		}
		updates, ok := sessions.lastUserPush().(*tg.Updates)
		if !ok {
			t.Fatalf("owner update = %T", sessions.lastUserPush())
		}
		if len(updates.Updates) != 1 {
			t.Fatalf("owner updates = %+v", updates.Updates)
		}
		signal, ok := updates.Updates[0].(*tg.UpdateUser)
		if !ok || signal.UserID != f.bot.ID {
			t.Fatalf("owner signal = %+v", updates.Updates[0])
		}
		bot := updates.Users[0].(*tg.User)
		if bot.ID != f.bot.ID || bot.Self || !bot.GetBotCanEdit() {
			t.Fatalf("owner bot projection = %+v", bot)
		}
		if photoID == 0 {
			if bot.Photo != nil {
				t.Fatalf("deleted photo = %+v", bot.Photo)
			}
			return
		}
		photo, ok := bot.Photo.(*tg.UserProfilePhoto)
		if !ok || photo.PhotoID != photoID {
			t.Fatalf("owner photo = %+v", bot.Photo)
		}
	}
	t.Run("bot self uploads static avatar", func(t *testing.T) {
		req := &tg.PhotosUploadProfilePhotoRequest{}
		req.SetFile(&tg.InputFile{ID: 6001, Parts: 1, Name: "bot.jpg"})
		got, err := f.router.onPhotosUploadProfilePhoto(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		assertOwner(t, got.Photo.(*tg.Photo).ID)
	})
	t.Run("bot self uploads video avatar", func(t *testing.T) {
		sessions.clearMessages()
		req := &tg.PhotosUploadProfilePhotoRequest{}
		req.SetVideo(&tg.InputFile{ID: 6002, Parts: 2, Name: "bot.mp4"})
		got, err := f.router.onPhotosUploadProfilePhoto(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		photo := got.Photo.(*tg.Photo)
		if len(photo.VideoSizes) != 1 {
			t.Fatalf("video photo = %+v", photo)
		}
		assertOwner(t, photo.ID)
	})
	t.Run("bot self clears current avatar", func(t *testing.T) {
		sessions.clearMessages()
		if _, err := f.router.onPhotosUpdateProfilePhoto(ctx, &tg.PhotosUpdateProfilePhotoRequest{ID: &tg.InputPhotoEmpty{}}); err != nil {
			t.Fatal(err)
		}
		if _, found, err := f.files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile); err != nil || found {
			t.Fatalf("current avatar found=%v err=%v", found, err)
		}
		assertOwner(t, 0)
	})
	t.Run("deleting bot photo from history notifies owner", func(t *testing.T) {
		req := &tg.PhotosUploadProfilePhotoRequest{}
		req.SetFile(&tg.InputFile{ID: 6003, Parts: 1, Name: "bot.jpg"})
		got, err := f.router.onPhotosUploadProfilePhoto(ctx, req)
		if err != nil {
			t.Fatal(err)
		}
		photo := got.Photo.(*tg.Photo)
		sessions.clearMessages()
		ids, err := f.router.onPhotosDeletePhotos(ctx, []tg.InputPhotoClass{&tg.InputPhoto{ID: photo.ID, AccessHash: photo.AccessHash, FileReference: photo.FileReference}})
		if err != nil {
			t.Fatal(err)
		}
		if len(ids) != 1 || ids[0] != photo.ID {
			t.Fatalf("deleted photos = %v", ids)
		}
		assertOwner(t, 0)
	})
}

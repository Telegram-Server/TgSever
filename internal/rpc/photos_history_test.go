package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	botsapp "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// photosHistoryFixture 搭一个带 owner / stranger / owner 名下 bot 的 router，
// 用于 updateProfilePhoto 选历史头像的归属校验（#38）。
type photosHistoryFixture struct {
	r        *Router
	files    *fakeFiles
	owner    domain.User
	stranger domain.User
	bot      domain.User
}

func newPhotosHistoryFixture(t *testing.T) photosHistoryFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	botStore := memory.NewBotStore(userStore)
	bots := botsapp.NewService(userStore, botStore, memory.NewMessageStore(memory.NewDialogStore()))
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550003801", FirstName: "Owner"})
	stranger, _ := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550003802", FirstName: "Stranger"})
	bot, _, err := bots.CreateBot(ctx, owner.ID, "History Bot", "history_photo_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	files := &fakeFiles{}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users: appusers.NewService(userStore, appusers.WithPhotoProvider(files)),
		Bots:  bots,
		Files: files,
	}, zaptest.NewLogger(t), clock.System)
	r.selfPhotoEchoPushDelay = 0
	return photosHistoryFixture{r: r, files: files, owner: owner, stranger: stranger, bot: bot}
}

// uploadProfile 以 caller 身份上传头像（photo=778，video=779），bot 非 nil 时代 bot 上传。
func (f photosHistoryFixture) uploadProfile(t *testing.T, caller int64, bot *domain.User, video, fallback bool) int64 {
	t.Helper()
	req := &tg.PhotosUploadProfilePhotoRequest{}
	if video {
		req.SetVideo(&tg.InputFile{ID: 43, Parts: 1, Name: "avatar.mp4"})
	} else {
		req.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "avatar.jpg"})
	}
	if bot != nil {
		req.SetBot(&tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash})
	}
	req.SetFallback(fallback)
	got, err := f.r.onPhotosUploadProfilePhoto(WithUserID(context.Background(), caller), req)
	if err != nil {
		t.Fatalf("uploadProfilePhoto: %v", err)
	}
	photo, ok := got.Photo.(*tg.Photo)
	if !ok {
		t.Fatalf("uploaded photo = %T, want *tg.Photo", got.Photo)
	}
	return photo.ID
}

func (f photosHistoryFixture) updateProfile(caller int64, bot *domain.User, photoID int64, fallback bool) (*tg.PhotosPhoto, error) {
	req := &tg.PhotosUpdateProfilePhotoRequest{ID: &tg.InputPhoto{ID: photoID, AccessHash: 7}}
	if bot != nil {
		req.SetBot(&tg.InputUser{UserID: bot.ID, AccessHash: bot.AccessHash})
	}
	req.SetFallback(fallback)
	return f.r.onPhotosUpdateProfilePhoto(WithUserID(context.Background(), caller), req)
}

func (f photosHistoryFixture) currentPhotoID(t *testing.T, ownerID int64, kind domain.ProfilePhotoKind) int64 {
	t.Helper()
	cur, ok, err := f.files.CurrentProfilePhotoKind(context.Background(), domain.PeerTypeUser, ownerID, kind)
	if err != nil {
		t.Fatalf("current profile photo: %v", err)
	}
	if !ok {
		return 0
	}
	return cur.ID
}

func wantPhotoIDInvalid(t *testing.T, err error) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), "PHOTO_ID_INVALID") {
		t.Fatalf("updateProfilePhoto error = %v, want PHOTO_ID_INVALID", err)
	}
}

func TestUpdateProfilePhotoAcceptsOwnHistoryPhoto(t *testing.T) {
	f := newPhotosHistoryFixture(t)
	first := f.uploadProfile(t, f.owner.ID, nil, false, false)
	second := f.uploadProfile(t, f.owner.ID, nil, true, false)
	if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindProfile); got != second {
		t.Fatalf("current before switch = %d, want %d", got, second)
	}

	got, err := f.updateProfile(f.owner.ID, nil, first, false)
	if err != nil {
		t.Fatalf("set own history photo: %v", err)
	}
	if photo, ok := got.Photo.(*tg.Photo); !ok || photo.ID != first {
		t.Fatalf("returned photo = %+v, want %d", got.Photo, first)
	}
	if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindProfile); got != first {
		t.Fatalf("current after switch = %d, want %d", got, first)
	}
}

func TestUpdateProfilePhotoRejectsPhotoOutsideOwnHistory(t *testing.T) {
	ctx := context.Background()
	f := newPhotosHistoryFixture(t)
	own := f.uploadProfile(t, f.owner.ID, nil, false, false)

	// stranger 的头像：在 stranger 的历史里，不在 owner 的历史里。
	strangerAvatar := f.files.putPhoto(domain.Photo{ID: 3801, AccessHash: 7, DCID: 2, Sizes: fakeAvatarStaticSizes()})
	if _, ok, err := f.files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, f.stranger.ID, domain.ProfilePhotoKindProfile, strangerAvatar.ID, 1); err != nil || !ok {
		t.Fatalf("seed stranger avatar: ok=%v err=%v", ok, err)
	}
	// 消息图片：photos 表里存在，但不在任何人的头像历史里。
	messagePhoto, err := f.files.CreatePhotoFromBytes(ctx, []byte("message photo"))
	if err != nil {
		t.Fatalf("seed message photo: %v", err)
	}

	for name, photoID := range map[string]int64{"other user's avatar": strangerAvatar.ID, "message photo": messagePhoto.ID} {
		t.Run(name, func(t *testing.T) {
			_, err := f.updateProfile(f.owner.ID, nil, photoID, false)
			wantPhotoIDInvalid(t, err)
			if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindProfile); got != own {
				t.Fatalf("owner current after rejected switch = %d, want unchanged %d", got, own)
			}
		})
	}
	if got := f.currentPhotoID(t, f.stranger.ID, domain.ProfilePhotoKindProfile); got != strangerAvatar.ID {
		t.Fatalf("stranger current = %d, want unchanged %d", got, strangerAvatar.ID)
	}
}

func TestUpdateProfilePhotoBotTargetUsesBotHistory(t *testing.T) {
	f := newPhotosHistoryFixture(t)
	bot := f.bot
	// owner 自己的历史头像（video=779）不在 bot 的历史里。
	ownerPhoto := f.uploadProfile(t, f.owner.ID, nil, true, false)
	botFirst := f.uploadProfile(t, f.owner.ID, &bot, false, false)

	_, err := f.updateProfile(f.owner.ID, &bot, ownerPhoto, false)
	wantPhotoIDInvalid(t, err)
	if got := f.currentPhotoID(t, bot.ID, domain.ProfilePhotoKindProfile); got != botFirst {
		t.Fatalf("bot current after rejected switch = %d, want unchanged %d", got, botFirst)
	}
	if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindProfile); got != ownerPhoto {
		t.Fatalf("owner current = %d, want unchanged %d", got, ownerPhoto)
	}

	// bot 自己历史里的照片可以切回。
	botSecond := f.uploadProfile(t, f.owner.ID, &bot, true, false)
	if botSecond == botFirst {
		t.Fatalf("fixture: bot uploads share id %d", botFirst)
	}
	if _, err := f.updateProfile(f.owner.ID, &bot, botFirst, false); err != nil {
		t.Fatalf("set bot history photo: %v", err)
	}
	if got := f.currentPhotoID(t, bot.ID, domain.ProfilePhotoKindProfile); got != botFirst {
		t.Fatalf("bot current after switch = %d, want %d", got, botFirst)
	}
}

// TestUpdateProfilePhotoAcceptsSuggestedPhoto 守护合法路径：别人通过
// uploadContactProfilePhoto(suggest) 建议的照片，收件人可以经 updateProfilePhoto 接受；
// 建议者自己不能借此把它设成头像。
func TestUpdateProfilePhotoAcceptsSuggestedPhoto(t *testing.T) {
	ctx := context.Background()
	r, _, _, owner, friend := newContactProfilePhotoTestRouter(t)
	r.selfPhotoEchoPushDelay = 0

	req := contactProfilePhotoReq(friend)
	req.SetSuggest(true)
	req.SetFile(uploadedAvatarFile())
	got, err := r.onPhotosUploadContactProfilePhoto(WithUserID(ctx, owner.ID), req)
	if err != nil {
		t.Fatalf("suggest contact profile photo: %v", err)
	}
	suggested, ok := got.Photo.(*tg.Photo)
	if !ok {
		t.Fatalf("suggested photo = %T, want *tg.Photo", got.Photo)
	}
	// 建议本身不改变收件人的头像。
	if _, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, friend.ID, domain.ProfilePhotoKindProfile); err != nil || found {
		t.Fatalf("friend current after suggest: found=%v err=%v, want none", found, err)
	}

	accept := &tg.PhotosUpdateProfilePhotoRequest{ID: &tg.InputPhoto{ID: suggested.ID, AccessHash: suggested.AccessHash}}
	if _, err := r.onPhotosUpdateProfilePhoto(WithUserID(ctx, friend.ID), accept); err != nil {
		t.Fatalf("accept suggested photo: %v", err)
	}
	if cur, found, err := r.deps.Files.CurrentProfilePhotoKind(ctx, domain.PeerTypeUser, friend.ID, domain.ProfilePhotoKindProfile); err != nil || !found || cur.ID != suggested.ID {
		t.Fatalf("friend current after accept = %+v found=%v err=%v, want %d", cur, found, err, suggested.ID)
	}

	steal := &tg.PhotosUpdateProfilePhotoRequest{ID: &tg.InputPhoto{ID: suggested.ID, AccessHash: suggested.AccessHash}}
	_, err = r.onPhotosUpdateProfilePhoto(WithUserID(ctx, owner.ID), steal)
	wantPhotoIDInvalid(t, err)
}

func TestUpdateProfilePhotoFallbackUsesOwnHistory(t *testing.T) {
	ctx := context.Background()
	f := newPhotosHistoryFixture(t)
	fallbackFirst := f.uploadProfile(t, f.owner.ID, nil, false, true)
	fallbackSecond := f.uploadProfile(t, f.owner.ID, nil, true, true)
	if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindFallback); got != fallbackSecond {
		t.Fatalf("fallback current = %d, want %d", got, fallbackSecond)
	}

	strangerAvatar := f.files.putPhoto(domain.Photo{ID: 3802, AccessHash: 7, DCID: 2, Sizes: fakeAvatarStaticSizes()})
	if _, ok, err := f.files.SetCurrentProfilePhotoKind(ctx, domain.PeerTypeUser, f.stranger.ID, domain.ProfilePhotoKindFallback, strangerAvatar.ID, 1); err != nil || !ok {
		t.Fatalf("seed stranger fallback: ok=%v err=%v", ok, err)
	}
	_, err := f.updateProfile(f.owner.ID, nil, strangerAvatar.ID, true)
	wantPhotoIDInvalid(t, err)
	if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindFallback); got != fallbackSecond {
		t.Fatalf("fallback current after rejected switch = %d, want unchanged %d", got, fallbackSecond)
	}

	if _, err := f.updateProfile(f.owner.ID, nil, fallbackFirst, true); err != nil {
		t.Fatalf("set own fallback history photo: %v", err)
	}
	if got := f.currentPhotoID(t, f.owner.ID, domain.ProfilePhotoKindFallback); got != fallbackFirst {
		t.Fatalf("fallback current after switch = %d, want %d", got, fallbackFirst)
	}
}

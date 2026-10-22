package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// setBotPhotoAsBot 以机器人自己的身份经 photos.uploadProfilePhoto（不带 bot 参数）设置头像，
// 与 setMyProfilePhoto 走同一处理器路径；返回新头像的 photo id。
func (f botAPIBridgeFixture) setBotPhotoAsBot(t *testing.T) int64 {
	t.Helper()
	req := &tg.PhotosUploadProfilePhotoRequest{}
	req.SetFile(&tg.InputFile{ID: 42, Parts: 1, Name: "bot.jpg"})
	got, err := f.router.onPhotosUploadProfilePhoto(WithUserID(f.ctx, f.bot.ID), req)
	if err != nil {
		t.Fatalf("bot uploads its own profile photo: %v", err)
	}
	photo, ok := got.Photo.(*tg.Photo)
	if !ok || photo.ID == 0 {
		t.Fatalf("uploaded photo = %T %+v, want a photo", got.Photo, got.Photo)
	}
	return photo.ID
}

// ownerSeesBotPhoto 断言所有者收到过针对机器人的 updateUser，且随附的机器人 user
// 是所有者视角（bot_can_edit）并带着 wantPhotoID（0 表示头像已清空）。
func (f botAPIBridgeFixture) ownerSeesBotPhoto(t *testing.T, wantPhotoID int64) {
	t.Helper()
	pushes := f.sessions.userPushesTo(f.owner.ID)
	if len(pushes) == 0 {
		t.Fatalf("owner received no push after the bot's photo changed")
	}
	updates, ok := pushes[len(pushes)-1].(*tg.Updates)
	if !ok {
		t.Fatalf("owner push = %T, want *tg.Updates", pushes[len(pushes)-1])
	}
	hasUpdateUser := false
	for _, u := range updates.Updates {
		if uu, ok := u.(*tg.UpdateUser); ok && uu.UserID == f.bot.ID {
			hasUpdateUser = true
		}
	}
	if !hasUpdateUser {
		t.Fatalf("owner push updates = %+v, want updateUser for bot %d", updates.Updates, f.bot.ID)
	}
	var bot *tg.User
	for _, u := range updates.Users {
		if uu, ok := u.(*tg.User); ok && uu.ID == f.bot.ID {
			bot = uu
		}
	}
	if bot == nil {
		t.Fatalf("owner push users = %+v, want the bot", updates.Users)
	}
	if bot.Self || !bot.Bot || !bot.GetBotCanEdit() {
		t.Fatalf("bot as pushed to owner: self=%v bot=%v can_edit=%v, want owner's view", bot.Self, bot.Bot, bot.GetBotCanEdit())
	}
	switch p := bot.Photo.(type) {
	case *tg.UserProfilePhoto:
		if p.PhotoID != wantPhotoID {
			t.Fatalf("pushed bot photo id = %d, want %d", p.PhotoID, wantPhotoID)
		}
	default:
		if wantPhotoID != 0 {
			t.Fatalf("pushed bot photo = %T, want photo %d", bot.Photo, wantPhotoID)
		}
	}
}

// 机器人自己换头像（setMyProfilePhoto 也走这条路径）时，所有者的会话收到 updateUser。
func TestBotSettingOwnPhotoNotifiesOwner(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	photoID := f.setBotPhotoAsBot(t)

	f.ownerSeesBotPhoto(t, photoID)
}

// removeMyProfilePhoto 删掉机器人的当前头像、返回 true，所有者收到头像已清空的 updateUser。
func TestBotAPIRemoveMyProfilePhotoDeletesPhotoAndNotifiesOwner(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.setBotPhotoAsBot(t)
	f.sessions.clearMessages()

	ok, err := f.router.BotAPIRemoveMyProfilePhoto(f.ctx, f.bot.ID)
	if err != nil || !ok {
		t.Fatalf("removeMyProfilePhoto = %v, %v; want true", ok, err)
	}

	if cur, found, err := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile); err != nil || found {
		t.Fatalf("bot current photo after remove = %+v found=%v err=%v, want none", cur, found, err)
	}
	f.ownerSeesBotPhoto(t, 0)
}

// 与官方一致：机器人没有头像时 removeMyProfilePhoto 仍返回 true（TDLib 对 bot 直接成功）。
func TestBotAPIRemoveMyProfilePhotoWithoutPhotoReturnsTrue(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	ok, err := f.router.BotAPIRemoveMyProfilePhoto(f.ctx, f.bot.ID)
	if err != nil || !ok {
		t.Fatalf("removeMyProfilePhoto without a photo = %v, %v; want true", ok, err)
	}
}

// 只有机器人能调用：普通用户 id 被拒。
func TestBotAPIRemoveMyProfilePhotoRejectsNonBot(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if _, err := f.router.BotAPIRemoveMyProfilePhoto(f.ctx, f.owner.ID); err == nil {
		t.Fatal("removeMyProfilePhoto accepted a non-bot user id")
	}
}

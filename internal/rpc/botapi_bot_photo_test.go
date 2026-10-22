package rpc

import (
	"bytes"
	"image"
	"image/png"
	"testing"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

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

// testProfilePNG 生成一张 w×h 的真实 PNG，作为 bot 上传的头像字节。
func testProfilePNG(t *testing.T, w, h int) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := png.Encode(&buf, image.NewRGBA(image.Rect(0, 0, w, h))); err != nil {
		t.Fatalf("encode png: %v", err)
	}
	return buf.Bytes()
}

func staticProfilePhoto(data []byte) domain.BotAPIProfilePhoto {
	return domain.BotAPIProfilePhoto{Kind: domain.BotAPIProfilePhotoStatic, FileName: "avatar.png", MimeType: "image/png", Data: data}
}

// setMyProfilePhoto（static）：上传的图片按头像规则（s/a/c）存成 bot 的当前头像，
// 所有者的会话收到带新头像的 updateUser。
func TestBotAPISetMyProfilePhotoStoresAvatarSizesAndNotifiesOwner(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	ok, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, staticProfilePhoto(testProfilePNG(t, 640, 480)))
	if err != nil || !ok {
		t.Fatalf("setMyProfilePhoto = %v, %v; want true", ok, err)
	}

	cur, found, err := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile)
	if err != nil || !found {
		t.Fatalf("bot current photo found=%v err=%v, want the new photo", found, err)
	}
	types := map[string]bool{}
	for _, size := range cur.Sizes {
		types[size.Type] = true
	}
	if len(cur.Sizes) != 3 || !types["s"] || !types["a"] || !types["c"] {
		t.Fatalf("bot avatar sizes = %+v, want avatar s/a/c sizes", cur.Sizes)
	}
	f.ownerSeesBotPhoto(t, cur.ID)
}

// 解不开的图片被拒（PHOTO_INVALID），原头像保持不变。
func TestBotAPISetMyProfilePhotoRejectsNonImage(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	before := f.setBotPhotoAsBot(t)

	_, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, staticProfilePhoto([]byte("not an image")))
	if !tgerr.Is(err, "PHOTO_INVALID") {
		t.Fatalf("setMyProfilePhoto with non-image err = %v, want PHOTO_INVALID", err)
	}
	if cur, found, _ := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile); !found || cur.ID != before {
		t.Fatalf("bot current photo = %d found=%v, want unchanged %d", cur.ID, found, before)
	}
}

// 超过头像上限（10MB）的文件被拒（FILE_TOO_BIG），不会成为头像。
func TestBotAPISetMyProfilePhotoRejectsOversizedFile(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	oversized := append(testProfilePNG(t, 8, 8), make([]byte, 10<<20)...)

	_, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, staticProfilePhoto(oversized))
	if !tgerr.Is(err, "FILE_TOO_BIG") {
		t.Fatalf("setMyProfilePhoto with 10MB+ file err = %v, want FILE_TOO_BIG", err)
	}
	if _, found, _ := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile); found {
		t.Fatal("oversized file became the bot's photo")
	}
}

func TestBotAPISetMyProfilePhotoRejectsNonBot(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if _, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.owner.ID, staticProfilePhoto(testProfilePNG(t, 8, 8))); err == nil {
		t.Fatal("setMyProfilePhoto accepted a non-bot user id")
	}
	if _, found, _ := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.owner.ID, domain.ProfilePhotoKindProfile); found {
		t.Fatal("non-bot user got a profile photo through setMyProfilePhoto")
	}
}

// 尚不支持的头像种类被拒，而不是被当作静态图处理。
func TestBotAPISetMyProfilePhotoRejectsUnknownKind(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	photo := staticProfilePhoto(testProfilePNG(t, 8, 8))
	photo.Kind = "sticker"

	if _, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, photo); !tgerr.Is(err, "PHOTO_INVALID") {
		t.Fatalf("setMyProfilePhoto with unknown kind err = %v, want PHOTO_INVALID", err)
	}
}

func animatedProfilePhoto(size int, mainFrame float64) domain.BotAPIProfilePhoto {
	return domain.BotAPIProfilePhoto{
		Kind:               domain.BotAPIProfilePhotoAnimated,
		FileName:           "avatar.mp4",
		MimeType:           "video/mp4",
		Data:               bytes.Repeat([]byte{0x5a}, size),
		MainFrameTimestamp: mainFrame,
	}
}

// setMyProfilePhoto（animated）：超过一个上传分片（512KB）的视频成为 bot 的视频头像，
// main_frame_timestamp 落到 'u' 视频尺寸的 video_start_ts；所有者收到 has_video 的 updateUser。
func TestBotAPISetMyProfilePhotoAnimatedStoresVideoAvatarAndNotifiesOwner(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	const videoBytes = 1<<20 + 7

	ok, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, animatedProfilePhoto(videoBytes, 2.5))
	if err != nil || !ok {
		t.Fatalf("setMyProfilePhoto(animated) = %v, %v; want true", ok, err)
	}

	cur, found, err := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile)
	if err != nil || !found {
		t.Fatalf("bot current photo found=%v err=%v, want the new video avatar", found, err)
	}
	var video *domain.PhotoSize
	for i := range cur.Sizes {
		if cur.Sizes[i].Kind == domain.PhotoSizeKindVideo {
			video = &cur.Sizes[i]
		}
	}
	if video == nil || video.Type != "u" || video.Size != videoBytes || video.VideoStartTs != 2.5 {
		t.Fatalf("bot avatar video size = %+v, want u of %d bytes with video_start_ts 2.5", video, videoBytes)
	}
	f.ownerSeesBotPhoto(t, cur.ID)
	if !f.ownerPushedBotPhotoHasVideo() {
		t.Fatal("owner's updateUser carries the bot photo without has_video")
	}
}

// ownerPushedBotPhotoHasVideo 读取所有者最近一次推送里机器人头像的 has_video。
func (f botAPIBridgeFixture) ownerPushedBotPhotoHasVideo() bool {
	pushes := f.sessions.pushesTo(f.owner.ID)
	if len(pushes) == 0 {
		return false
	}
	updates, ok := pushes[len(pushes)-1].(*tg.Updates)
	if !ok {
		return false
	}
	for _, u := range updates.Users {
		if bot, ok := u.(*tg.User); ok && bot.ID == f.bot.ID {
			photo, ok := bot.Photo.(*tg.UserProfilePhoto)
			return ok && photo.HasVideo
		}
	}
	return false
}

// 主帧时间戳必须是非负有限数；非法时拒绝（PHOTO_INVALID），不会成为头像。
func TestBotAPISetMyProfilePhotoAnimatedRejectsNegativeMainFrame(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	_, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, animatedProfilePhoto(1024, -1))
	if !tgerr.Is(err, "PHOTO_INVALID") {
		t.Fatalf("setMyProfilePhoto(animated, main frame -1) err = %v, want PHOTO_INVALID", err)
	}
	if _, found, _ := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile); found {
		t.Fatal("animated photo with a negative main frame became the bot's photo")
	}
}

// 超过头像上限（10MB）的视频被拒（FILE_TOO_BIG）。
func TestBotAPISetMyProfilePhotoAnimatedRejectsOversizedVideo(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	_, err := f.router.BotAPISetMyProfilePhoto(f.ctx, f.bot.ID, animatedProfilePhoto(10<<20+1, 0))
	if !tgerr.Is(err, "FILE_TOO_BIG") {
		t.Fatalf("setMyProfilePhoto(animated, 10MB+) err = %v, want FILE_TOO_BIG", err)
	}
	if _, found, _ := f.files.CurrentProfilePhotoKind(f.ctx, domain.PeerTypeUser, f.bot.ID, domain.ProfilePhotoKindProfile); found {
		t.Fatal("oversized video became the bot's photo")
	}
}

package rpc

import (
	"context"
	"reflect"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// botUserPhotosFixture：owner 有 5 张头像的相册、一个联系人、一个陌生人和一个机器人，
// 头像隐私默认「仅联系人」。files 真实按 offset/limit 分页。
type botUserPhotosFixture struct {
	ctx      context.Context
	r        *Router
	files    *fakeFiles
	privacy  *appprivacy.Service
	owner    domain.User
	contact  domain.User
	stranger domain.User
	bot      domain.User
	album    []domain.Photo
}

func newBotUserPhotosFixture(t *testing.T) botUserPhotosFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	contacts := memory.NewContactStore()
	privacy := appprivacy.NewService(memory.NewPrivacyStore(), contacts)
	mustCreate := func(u domain.User) domain.User {
		t.Helper()
		created, err := userStore.Create(ctx, u)
		if err != nil {
			t.Fatalf("create user %q: %v", u.FirstName, err)
		}
		return created
	}
	owner := mustCreate(domain.User{AccessHash: 51, Phone: "15550013201", FirstName: "Owner"})
	contact := mustCreate(domain.User{AccessHash: 52, Phone: "15550013202", FirstName: "Contact"})
	stranger := mustCreate(domain.User{AccessHash: 53, Phone: "15550013203", FirstName: "Stranger"})
	bot := mustCreate(domain.User{AccessHash: 54, FirstName: "Photos Bot", Username: "photos132_bot", Bot: true})
	if _, err := contacts.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: contact.ID, FirstName: "Contact"}); err != nil {
		t.Fatalf("owner adds contact: %v", err)
	}
	album := make([]domain.Photo, 0, 5)
	for i := 5; i >= 1; i-- {
		album = append(album, avatarTestPhoto(9132000000000000000+int64(i)))
	}
	files := &fakeFiles{photos: map[int64]domain.Photo{}, profileAlbums: map[int64][]domain.Photo{owner.ID: album}}
	for _, p := range album {
		files.putPhoto(p)
	}
	files.setCurrentProfile(domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile, album[0].ID)
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Privacy: privacy,
		Files:   files,
	}, zaptest.NewLogger(t), clock.System)
	f := botUserPhotosFixture{ctx: ctx, r: r, files: files, privacy: privacy, owner: owner, contact: contact, stranger: stranger, bot: bot, album: album}
	f.setPrivacy(t, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowContacts})
	return f
}

// avatarTestPhoto 造一张带 s/a/c 头像尺寸的照片。
func avatarTestPhoto(id int64) domain.Photo {
	return domain.Photo{ID: id, AccessHash: id % 1000, DCID: 2, Sizes: []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindDefault, Type: "s", W: 160, H: 160, Size: 1100},
		{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 320, H: 320, Size: 2200},
		{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 4400},
	}}
}

func (f botUserPhotosFixture) setPrivacy(t *testing.T, rules ...domain.PrivacyRule) {
	t.Helper()
	if _, err := f.privacy.SetRules(f.ctx, f.owner.ID, domain.PrivacyKeyProfilePhoto, rules); err != nil {
		t.Fatalf("set profile photo privacy: %v", err)
	}
}

func (f botUserPhotosFixture) setFallback() domain.Photo {
	photo := f.files.putPhoto(avatarTestPhoto(9132000000000000099))
	f.files.setCurrentProfile(domain.PeerTypeUser, f.owner.ID, domain.ProfilePhotoKindFallback, photo.ID)
	return photo
}

// botPage 以机器人身份取一页相册，返回 photo id 与 total_count。
func (f botUserPhotosFixture) botPage(t *testing.T, offset, limit int) ([]int64, int) {
	t.Helper()
	photos, total, err := f.r.BotAPIGetUserProfilePhotos(f.ctx, f.bot.ID, f.owner.ID, offset, limit)
	if err != nil {
		t.Fatalf("BotAPIGetUserProfilePhotos(offset=%d, limit=%d): %v", offset, limit, err)
	}
	ids := make([]int64, 0, len(photos))
	for _, p := range photos {
		if len(p.Sizes) == 0 {
			t.Fatalf("photo %d has no sizes", p.ID)
		}
		ids = append(ids, p.ID)
	}
	return ids, total
}

// clientAlbum 以 MTProto 客户端身份调 photos.getUserPhotos 取整本相册的 photo id。
func (f botUserPhotosFixture) clientAlbum(t *testing.T, viewer domain.User) []int64 {
	t.Helper()
	got, err := f.r.onPhotosGetUserPhotos(WithUserID(f.ctx, viewer.ID), &tg.PhotosGetUserPhotosRequest{
		UserID: &tg.InputUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash},
		Limit:  100,
	})
	if err != nil {
		t.Fatalf("getUserPhotos as %s: %v", viewer.FirstName, err)
	}
	ids, _ := albumIDsAndCount(t, got)
	return ids
}

func (f botUserPhotosFixture) albumIDs(from, to int) []int64 {
	ids := make([]int64, 0, to-from)
	for _, p := range f.album[from:to] {
		ids = append(ids, p.ID)
	}
	return ids
}

func TestBotAPIGetUserProfilePhotosPagesAlbum(t *testing.T) {
	f := newBotUserPhotosFixture(t)
	f.setPrivacy(t, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowUsers, UserIDs: []int64{f.bot.ID}})

	cases := []struct {
		offset, limit int
		want          []int64
	}{
		{0, 2, f.albumIDs(0, 2)},
		{2, 2, f.albumIDs(2, 4)},
		{4, 2, f.albumIDs(4, 5)},
		{0, 100, f.albumIDs(0, 5)},
		{5, 10, []int64{}},
	}
	for _, tc := range cases {
		ids, total := f.botPage(t, tc.offset, tc.limit)
		if total != 5 || !reflect.DeepEqual(ids, tc.want) {
			t.Fatalf("offset=%d limit=%d: ids=%v total=%d, want ids=%v total=5", tc.offset, tc.limit, ids, total, tc.want)
		}
	}
}

func TestBotAPIGetUserProfilePhotosFollowsProfilePhotoPrivacy(t *testing.T) {
	f := newBotUserPhotosFixture(t)

	// 仅联系人且没有公开头像：机器人与陌生人一样看到空相册，联系人看到整本。
	ids, total := f.botPage(t, 0, 100)
	if len(ids) != 0 || total != 0 {
		t.Fatalf("hidden album without fallback: bot got ids=%v total=%d, want empty", ids, total)
	}
	if got := f.clientAlbum(t, f.stranger); len(got) != 0 {
		t.Fatalf("stranger client sees %v, want empty", got)
	}
	if got := f.clientAlbum(t, f.contact); !reflect.DeepEqual(got, f.albumIDs(0, 5)) {
		t.Fatalf("contact client sees %v, want whole album", got)
	}

	// 有公开头像时，机器人看到的与陌生人客户端一致：只有那一张，total_count 为 1。
	fallback := f.setFallback()
	ids, total = f.botPage(t, 0, 100)
	if !reflect.DeepEqual(ids, []int64{fallback.ID}) || total != 1 {
		t.Fatalf("hidden album with fallback: bot got ids=%v total=%d, want [%d] total=1", ids, total, fallback.ID)
	}
	if got := f.clientAlbum(t, f.stranger); !reflect.DeepEqual(got, ids) {
		t.Fatalf("stranger client sees %v, bot sees %v; want the same", got, ids)
	}
	// 分页不泄露真实相册大小。
	if ids, total = f.botPage(t, 1, 100); len(ids) != 0 || total != 1 {
		t.Fatalf("hidden album offset=1: ids=%v total=%d, want empty total=1", ids, total)
	}

	// 隐私对所有人开放后，机器人看到整本相册。
	f.setPrivacy(t, domain.PrivacyRule{Kind: domain.PrivacyRuleAllowAll})
	if ids, total = f.botPage(t, 0, 100); !reflect.DeepEqual(ids, f.albumIDs(0, 5)) || total != 5 {
		t.Fatalf("public album: bot got ids=%v total=%d, want whole album", ids, total)
	}
}

func TestBotAPIGetUserProfilePhotosUnknownUserIsInvalid(t *testing.T) {
	f := newBotUserPhotosFixture(t)
	for _, userID := range []int64{0, -1, f.owner.ID + 100000} {
		_, _, err := f.r.BotAPIGetUserProfilePhotos(f.ctx, f.bot.ID, userID, 0, 100)
		wantBotAPIError(t, err, "USER_ID_INVALID")
	}
}

// 相册照片的 file_id 指向的就是这张照片：机器人能按 photo:<id>:<size> 下载并重发。
func TestBotAPIUserProfilePhotoCanBeDownloadedAndResent(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	avatar := f.files.putPhoto(avatarTestPhoto(9132000000000000500))
	f.files.profileAlbums = map[int64][]domain.Photo{f.owner.ID: {avatar}}
	f.files.setCurrentProfile(domain.PeerTypeUser, f.owner.ID, domain.ProfilePhotoKindProfile, avatar.ID)

	photos, total, err := f.router.BotAPIGetUserProfilePhotos(f.ctx, f.bot.ID, f.owner.ID, 0, 100)
	if err != nil || total != 1 || len(photos) != 1 || photos[0].ID != avatar.ID {
		t.Fatalf("album = %v total=%d err=%v, want the avatar", photos, total, err)
	}
	locationKey := "photo:" + strconv.FormatInt(avatar.ID, 10) + ":c"

	f.files.getFileFound = true
	f.files.getFileChunk = domain.FileChunk{Bytes: []byte("jpeg"), MimeType: "image/jpeg", Total: 4}
	if _, found, err := f.router.BotAPIGetFile(f.ctx, f.bot.ID, locationKey, 0, 1); err != nil || !found {
		t.Fatalf("getFile(%s): found=%v err=%v", locationKey, found, err)
	}
	if f.files.getFileRequest.LocationKey != locationKey {
		t.Fatalf("getFile read %q, want %q", f.files.getFileRequest.LocationKey, locationKey)
	}

	msg, err := f.router.BotAPISendMedia(f.ctx, f.bot.ID, f.chatID, "photo", locationKey, "", "", "", nil, "", nil, nil, false, 0)
	if err != nil {
		t.Fatalf("resend avatar: %v", err)
	}
	if msg.Media == nil || msg.Media.Photo == nil || msg.Media.Photo.ID != avatar.ID {
		t.Fatalf("resent message media = %+v, want photo %d", msg.Media, avatar.ID)
	}
}

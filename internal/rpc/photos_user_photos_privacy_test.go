package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appaccount "telesrv/internal/app/account"
	appprivacy "telesrv/internal/app/privacy"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// albumPrivacyFixture 搭一个头像隐私为「仅联系人」的 owner，以及联系人、陌生人、
// bot 三类查看者，用于 photos.getUserPhotos 按头像隐私裁剪相册（#39）。
type albumPrivacyFixture struct {
	r        *Router
	files    *fakeFiles
	owner    domain.User
	contact  domain.User
	stranger domain.User
	bot      domain.User
	album    []domain.Photo
}

const albumPrivacyAlbumTotal = 5

func newAlbumPrivacyFixture(t *testing.T) albumPrivacyFixture {
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
	owner := mustCreate(domain.User{AccessHash: 41, Phone: "15550003901", FirstName: "Owner"})
	contact := mustCreate(domain.User{AccessHash: 42, Phone: "15550003902", FirstName: "Contact"})
	stranger := mustCreate(domain.User{AccessHash: 43, Phone: "15550003903", FirstName: "Stranger"})
	bot := mustCreate(domain.User{AccessHash: 44, FirstName: "Album Bot", Username: "album_privacy_bot", Bot: true})
	if _, err := contacts.Upsert(ctx, owner.ID, domain.ContactInput{ContactUserID: contact.ID, FirstName: "Contact"}); err != nil {
		t.Fatalf("owner adds contact: %v", err)
	}
	if _, err := privacy.SetRules(ctx, owner.ID, domain.PrivacyKeyProfilePhoto, []domain.PrivacyRule{{Kind: domain.PrivacyRuleAllowContacts}}); err != nil {
		t.Fatalf("set profile photo privacy: %v", err)
	}
	album := []domain.Photo{
		albumTestPhoto(9100000000000003903, 3),
		albumTestPhoto(9100000000000003902, 2),
		albumTestPhoto(9100000000000003901, 1),
	}
	files := &fakeFiles{profilePhotos: album, profilePhotosTotal: albumPrivacyAlbumTotal}
	for _, p := range album {
		files.putPhoto(p)
	}
	files.setCurrentProfile(domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindProfile, album[0].ID)
	passwords := memory.NewPasswordStore()
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Privacy: privacy,
		Account: appaccount.NewService(passwords, appaccount.WithSavedMusic(passwords)),
		Files:   files,
	}, zaptest.NewLogger(t), clock.System)
	return albumPrivacyFixture{r: r, files: files, owner: owner, contact: contact, stranger: stranger, bot: bot, album: album}
}

// albumTestPhoto 造一张带尺寸的照片（无尺寸的照片会被渲染成 photoEmpty）。
func albumTestPhoto(id, accessHash int64) domain.Photo {
	return domain.Photo{
		ID:         id,
		AccessHash: accessHash,
		DCID:       2,
		Sizes:      []domain.PhotoSize{{Kind: domain.PhotoSizeKindDefault, Type: "x", W: 640, H: 640, Size: 1234}},
	}
}

// setFallback 给 owner 设一张公开（fallback）头像。
func (f albumPrivacyFixture) setFallback() domain.Photo {
	photo := f.files.putPhoto(albumTestPhoto(9100000000000003990, 90))
	f.files.setCurrentProfile(domain.PeerTypeUser, f.owner.ID, domain.ProfilePhotoKindFallback, photo.ID)
	return photo
}

func (f albumPrivacyFixture) getUserPhotos(t *testing.T, viewer domain.User, req tg.PhotosGetUserPhotosRequest) tg.PhotosPhotosClass {
	t.Helper()
	req.UserID = &tg.InputUser{UserID: f.owner.ID, AccessHash: f.owner.AccessHash}
	if viewer.ID == f.owner.ID {
		req.UserID = &tg.InputUserSelf{}
	}
	got, err := f.r.onPhotosGetUserPhotos(WithUserID(context.Background(), viewer.ID), &req)
	if err != nil {
		t.Fatalf("getUserPhotos as %s: %v", viewer.FirstName, err)
	}
	return got
}

// albumIDsAndCount 返回相册里的 photo id，以及客户端会采信的总数：
// photos.photos 的总数就是列表长度，photos.photosSlice 取 count。
func albumIDsAndCount(t *testing.T, got tg.PhotosPhotosClass) ([]int64, int) {
	t.Helper()
	var photos []tg.PhotoClass
	count := -1
	switch v := got.(type) {
	case *tg.PhotosPhotos:
		photos = v.Photos
	case *tg.PhotosPhotosSlice:
		photos = v.Photos
		count = v.Count
	default:
		t.Fatalf("getUserPhotos = %T, want photos.photos or photos.photosSlice", got)
	}
	ids := make([]int64, 0, len(photos))
	for _, p := range photos {
		photo, ok := p.(*tg.Photo)
		if !ok {
			t.Fatalf("album entry = %T, want *tg.Photo", p)
		}
		ids = append(ids, photo.ID)
	}
	if count < 0 {
		count = len(ids)
	}
	return ids, count
}

func TestPhotosGetUserPhotosHiddenAlbumShowsOnlyFallback(t *testing.T) {
	f := newAlbumPrivacyFixture(t)
	fallback := f.setFallback()
	for _, viewer := range []domain.User{f.stranger, f.bot} {
		got := f.getUserPhotos(t, viewer, tg.PhotosGetUserPhotosRequest{Limit: 10})
		ids, count := albumIDsAndCount(t, got)
		if !int64SliceEqual(ids, []int64{fallback.ID}) || count != 1 {
			t.Fatalf("%s sees album %v count=%d, want only fallback %d count=1", viewer.FirstName, ids, count, fallback.ID)
		}
	}
}

func TestPhotosGetUserPhotosHiddenAlbumWithoutFallbackIsEmpty(t *testing.T) {
	f := newAlbumPrivacyFixture(t)
	for _, viewer := range []domain.User{f.stranger, f.bot} {
		got := f.getUserPhotos(t, viewer, tg.PhotosGetUserPhotosRequest{Limit: 10})
		ids, count := albumIDsAndCount(t, got)
		if len(ids) != 0 || count != 0 {
			t.Fatalf("%s sees album %v count=%d, want empty", viewer.FirstName, ids, count)
		}
	}
}

func TestPhotosGetUserPhotosContactAndOwnerSeeFullAlbum(t *testing.T) {
	f := newAlbumPrivacyFixture(t)
	f.setFallback()
	want := []int64{f.album[0].ID, f.album[1].ID, f.album[2].ID}
	for _, viewer := range []domain.User{f.contact, f.owner} {
		got := f.getUserPhotos(t, viewer, tg.PhotosGetUserPhotosRequest{Limit: 3})
		ids, count := albumIDsAndCount(t, got)
		if !int64SliceEqual(ids, want) || count != albumPrivacyAlbumTotal {
			t.Fatalf("%s sees album %v count=%d, want %v count=%d", viewer.FirstName, ids, count, want, albumPrivacyAlbumTotal)
		}
	}
}

// TestPhotosGetUserPhotosFallbackOnlyOwnerShowsFallbackToEveryone 锁定只有 fallback、
// 没有 profile 头像的 owner：可见的联系人不能比陌生人看到的更少（user.photo 此时
// 对所有人都投影为 fallback）。
func TestPhotosGetUserPhotosFallbackOnlyOwnerShowsFallbackToEveryone(t *testing.T) {
	f := newAlbumPrivacyFixture(t)
	f.files.profilePhotos = nil
	f.files.profilePhotosTotal = 0
	delete(f.files.profile, fakeProfilePhotoKey{ownerType: domain.PeerTypeUser, ownerID: f.owner.ID, kind: domain.ProfilePhotoKindProfile})
	fallback := f.setFallback()
	for _, viewer := range []domain.User{f.contact, f.stranger} {
		got := f.getUserPhotos(t, viewer, tg.PhotosGetUserPhotosRequest{Limit: 10})
		ids, count := albumIDsAndCount(t, got)
		if !int64SliceEqual(ids, []int64{fallback.ID}) || count != 1 {
			t.Fatalf("%s sees album %v count=%d, want only fallback %d", viewer.FirstName, ids, count, fallback.ID)
		}
	}
}

// TestPhotosGetUserPhotosHiddenAlbumPaginatesOverFallbackOnly 锁定隐藏时分页与总数
// 只以 fallback 这一张为准：翻到第二页为空，总数不泄露真实相册大小。
func TestPhotosGetUserPhotosHiddenAlbumPaginatesOverFallbackOnly(t *testing.T) {
	f := newAlbumPrivacyFixture(t)
	fallback := f.setFallback()
	cases := []struct {
		name  string
		req   tg.PhotosGetUserPhotosRequest
		want  []int64
		count int
	}{
		{name: "first page limit 1", req: tg.PhotosGetUserPhotosRequest{Limit: 1}, want: []int64{fallback.ID}, count: 1},
		{name: "second page", req: tg.PhotosGetUserPhotosRequest{Offset: 1, Limit: 1}, want: nil, count: 1},
		{name: "older than fallback", req: tg.PhotosGetUserPhotosRequest{MaxID: fallback.ID, Limit: 10}, want: nil, count: 1},
		{name: "refresh fallback", req: tg.PhotosGetUserPhotosRequest{Offset: -1, MaxID: fallback.ID, Limit: 1}, want: []int64{fallback.ID}, count: 1},
		{name: "refresh hidden photo", req: tg.PhotosGetUserPhotosRequest{Offset: -1, MaxID: f.album[1].ID, Limit: 1}, want: nil, count: 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := f.getUserPhotos(t, f.stranger, tc.req)
			ids, count := albumIDsAndCount(t, got)
			if !int64SliceEqual(ids, tc.want) {
				t.Fatalf("album ids = %v, want %v", ids, tc.want)
			}
			// photos.photos 只带当前页，此时客户端以「已见数量」为总数；
			// photos.photosSlice 的 count 必须正好是可见相册大小。
			if slice, ok := got.(*tg.PhotosPhotosSlice); ok && slice.Count != tc.count {
				t.Fatalf("slice count = %d, want %d", slice.Count, tc.count)
			}
			if count > tc.count {
				t.Fatalf("visible count = %d, exceeds hidden album size %d", count, tc.count)
			}
		})
	}
}

func TestDeletedOwnerAlbumAndSavedMusicAreEmpty(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	// 已注销 owner 的相册和音乐数据仍留在存储里（墓碑是视图期投影），
	// 查看者必须拿到空列表。
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550003951", FirstName: "Owner", Deleted: true, DeletedAt: 1_800_000_000})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	viewer, err := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550003952", FirstName: "Viewer"})
	if err != nil {
		t.Fatalf("create viewer: %v", err)
	}
	music := testMusicDocument(3901, 39010, "Anthem")
	album := []domain.Photo{albumTestPhoto(9100000000000003951, 1)}
	files := &fakeFiles{profilePhotos: album, profilePhotosTotal: 1}
	files.putPhoto(album[0])
	files.setCurrentProfile(domain.PeerTypeUser, owner.ID, domain.ProfilePhotoKindFallback, album[0].ID)
	passwords := memory.NewPasswordStore()
	account := appaccount.NewService(passwords, appaccount.WithSavedMusic(passwords))
	if _, err := account.SaveMusic(ctx, owner.ID, domain.SaveMusicRequest{Document: music, Date: 1_700_000_000}); err != nil {
		t.Fatalf("seed owner music: %v", err)
	}
	if list, err := account.ListSavedMusic(ctx, owner.ID, 0, 10); err != nil || list.Count != 1 {
		t.Fatalf("seeded owner music = %+v, %v, want one song in storage", list, err)
	}
	r := New(Config{}, Deps{
		Users:   appusers.NewService(userStore),
		Privacy: appprivacy.NewService(memory.NewPrivacyStore(), memory.NewContactStore()),
		Account: account,
		Files:   files,
	}, zaptest.NewLogger(t), clock.System)
	viewerCtx := WithUserID(ctx, viewer.ID)
	input := &tg.InputUser{UserID: owner.ID, AccessHash: owner.AccessHash}

	photos, err := r.onPhotosGetUserPhotos(viewerCtx, &tg.PhotosGetUserPhotosRequest{UserID: input, Limit: 10})
	if err != nil {
		t.Fatalf("getUserPhotos of deleted owner: %v", err)
	}
	if ids, count := albumIDsAndCount(t, photos); len(ids) != 0 || count != 0 {
		t.Fatalf("deleted owner album = %v count=%d, want empty", ids, count)
	}

	saved, err := r.onUsersGetSavedMusic(viewerCtx, &tg.UsersGetSavedMusicRequest{ID: input, Limit: 10})
	if err != nil {
		t.Fatalf("getSavedMusic of deleted owner: %v", err)
	}
	list, ok := saved.(*tg.UsersSavedMusic)
	if !ok || list.Count != 0 || len(list.Documents) != 0 {
		t.Fatalf("deleted owner saved music = %T %+v, want empty", saved, saved)
	}
	bot, err := userStore.Create(ctx, domain.User{Bot: true, FirstName: "Deleted-profile reader", Username: "deleted_profile_reader_bot"})
	if err != nil {
		t.Fatal(err)
	}
	apiPhotos, total, err := r.BotAPIGetUserProfilePhotos(ctx, bot.ID, owner.ID, 0, 10)
	if err != nil || total != 0 || len(apiPhotos) != 0 {
		t.Fatalf("Bot API deleted album=%+v total=%d err=%v", apiPhotos, total, err)
	}
	apiAudios, err := r.BotAPIGetUserProfileAudios(ctx, bot.ID, owner.ID, 0, 10)
	if err != nil || apiAudios.Count != 0 || len(apiAudios.Documents) != 0 {
		t.Fatalf("Bot API deleted audios=%+v err=%v", apiAudios, err)
	}
}

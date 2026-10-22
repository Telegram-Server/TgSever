package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"reflect"
	"testing"

	"telesrv/internal/domain"
)

// fakeUserPhotosGateway 记录 getUserProfilePhotos 收到的参数，并返回预置的一页相册。
type fakeUserPhotosGateway struct {
	*fakeBotAPIGateway
	calls  int
	botID  int64
	userID int64
	offset int
	limit  int

	photos []domain.Photo
	total  int
	err    error
}

func (f *fakeUserPhotosGateway) BotAPIGetUserProfilePhotos(_ context.Context, botID, userID int64, offset, limit int) ([]domain.Photo, int, error) {
	f.calls++
	f.botID, f.userID, f.offset, f.limit = botID, userID, offset, limit
	return f.photos, f.total, f.err
}

func newFakeUserPhotosGateway() *fakeUserPhotosGateway {
	return &fakeUserPhotosGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
}

// avatarPhoto 造一张头像：s/a/c 三个静态尺寸、一个内联 stripped 和一个视频尺寸 u。
func avatarPhoto(id int64) domain.Photo {
	return domain.Photo{ID: id, Sizes: []domain.PhotoSize{
		{Kind: domain.PhotoSizeKindStripped, Type: "i", Bytes: []byte{1, 2, 3}},
		{Kind: domain.PhotoSizeKindDefault, Type: "s", W: 160, H: 160, Size: 1100},
		{Kind: domain.PhotoSizeKindDefault, Type: "a", W: 320, H: 320, Size: 2200},
		{Kind: domain.PhotoSizeKindDefault, Type: "c", W: 640, H: 640, Size: 4400},
		{Kind: domain.PhotoSizeKindVideo, Type: "u", W: 640, H: 640, Size: 99000},
	}}
}

type apiUserProfilePhotos struct {
	TotalCount int `json:"total_count"`
	Photos     [][]struct {
		FileID       string `json:"file_id"`
		FileUniqueID string `json:"file_unique_id"`
		Width        int    `json:"width"`
		Height       int    `json:"height"`
		FileSize     int    `json:"file_size"`
	} `json:"photos"`
}

func getUserProfilePhotosResult(t *testing.T, h http.Handler, body string) apiUserProfilePhotos {
	t.Helper()
	rec := callMethod(t, h, "getUserProfilePhotos", body)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("%s: status = %d body = %s", body, rec.Code, rec.Body.String())
	}
	var out apiUserProfilePhotos
	if err := json.Unmarshal(resp.Result, &out); err != nil {
		t.Fatalf("decode result %s: %v", resp.Result, err)
	}
	return out
}

func TestGetUserProfilePhotosProjectsAlbumPage(t *testing.T) {
	gateway := newFakeUserPhotosGateway()
	gateway.photos = []domain.Photo{avatarPhoto(7001), avatarPhoto(7002)}
	gateway.total = 5
	h := newTestHandler(t, gateway)

	got := getUserProfilePhotosResult(t, h, `{"user_id":42,"offset":1,"limit":2}`)

	if gateway.botID != testBotProfile.BotUserID || gateway.userID != 42 || gateway.offset != 1 || gateway.limit != 2 {
		t.Fatalf("gateway got bot=%d user=%d offset=%d limit=%d, want bot=%d user=42 offset=1 limit=2",
			gateway.botID, gateway.userID, gateway.offset, gateway.limit, testBotProfile.BotUserID)
	}
	if got.TotalCount != 5 {
		t.Fatalf("total_count = %d, want 5", got.TotalCount)
	}
	if len(got.Photos) != 2 {
		t.Fatalf("photos = %d, want 2", len(got.Photos))
	}
	// 每张头像只投影可下载的静态尺寸（s/a/c），从小到大；stripped 与视频尺寸不出现。
	for i, photo := range got.Photos {
		var dims [][3]int
		for _, size := range photo {
			dims = append(dims, [3]int{size.Width, size.Height, size.FileSize})
			if size.FileID == "" || size.FileUniqueID == "" {
				t.Fatalf("photo %d size %dx%d has empty file ids", i, size.Width, size.Height)
			}
		}
		want := [][3]int{{160, 160, 1100}, {320, 320, 2200}, {640, 640, 4400}}
		if !reflect.DeepEqual(dims, want) {
			t.Fatalf("photo %d sizes = %v, want %v", i, dims, want)
		}
	}
	if got.Photos[0][2].FileID == got.Photos[1][2].FileID {
		t.Fatal("two different photos share a file_id")
	}
}

func TestGetUserProfilePhotosDefaultsAndClampsPaging(t *testing.T) {
	cases := []struct {
		body       string
		wantOffset int
		wantLimit  int
	}{
		{`{"user_id":42}`, 0, 100},
		{`{"user_id":42,"offset":-3,"limit":0}`, 0, 1},
		{`{"user_id":42,"offset":4,"limit":500}`, 4, 100},
		{`{"user_id":"42","offset":"2","limit":"7"}`, 2, 7},
	}
	for _, tc := range cases {
		gateway := newFakeUserPhotosGateway()
		h := newTestHandler(t, gateway)
		got := getUserProfilePhotosResult(t, h, tc.body)
		if gateway.offset != tc.wantOffset || gateway.limit != tc.wantLimit {
			t.Fatalf("%s: offset=%d limit=%d, want offset=%d limit=%d", tc.body, gateway.offset, gateway.limit, tc.wantOffset, tc.wantLimit)
		}
		if got.TotalCount != 0 || got.Photos == nil || len(got.Photos) != 0 {
			t.Fatalf("%s: empty album = %+v, want total_count 0 and photos []", tc.body, got)
		}
	}
}

func TestGetUserProfilePhotosInvalidUserIDUsesOfficialDescription(t *testing.T) {
	for _, body := range []string{`{}`, `{"user_id":"abc"}`, `{"user_id":0}`, `{"user_id":-5}`} {
		gateway := newFakeUserPhotosGateway()
		rec := callMethod(t, newTestHandler(t, gateway), "getUserProfilePhotos", body)
		resp := decodeAPIResponse(t, rec)
		if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != "Bad Request: invalid user_id specified" {
			t.Fatalf("%s: status=%d body=%s", body, rec.Code, rec.Body.String())
		}
		if gateway.calls != 0 {
			t.Fatalf("%s: gateway called %d times for an invalid user_id", body, gateway.calls)
		}
	}
}

func TestGetUserProfilePhotosUnknownUserUsesOfficialDescription(t *testing.T) {
	gateway := newFakeUserPhotosGateway()
	gateway.err = errors.New("USER_ID_INVALID")
	rec := callMethod(t, newTestHandler(t, gateway), "getUserProfilePhotos", `{"user_id":4242}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusBadRequest || resp.OK || resp.Description != "Bad Request: user not found" {
		t.Fatalf("status=%d body=%s", rec.Code, rec.Body.String())
	}
}

func TestGetUserProfilePhotosWithoutGatewayIsNotImplemented(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "getUserProfilePhotos", `{"user_id":42}`)
	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("status = %d, want 501; body = %s", rec.Code, rec.Body.String())
	}
}

// 相册里的 file_id 与消息照片同源：getFile 读到对应尺寸，sendPhoto 按同一张照片重发。
func TestGetUserProfilePhotosFileIDsWorkWithGetFileAndSendPhoto(t *testing.T) {
	gateway := newFakeUserPhotosGateway()
	gateway.photos = []domain.Photo{avatarPhoto(7001)}
	gateway.total = 1
	gateway.fileChunks = map[string]domain.FileChunk{
		"photo:7001:c": {Bytes: []byte("jpeg"), MimeType: "image/jpeg", Total: 4400},
	}
	h := newTestHandler(t, gateway)
	album := getUserProfilePhotosResult(t, h, `{"user_id":42}`)
	biggest := album.Photos[0][len(album.Photos[0])-1].FileID

	rec := callMethod(t, h, "getFile", `{"file_id":"`+biggest+`"}`)
	resp := decodeAPIResponse(t, rec)
	if rec.Code != http.StatusOK || !resp.OK {
		t.Fatalf("getFile: status=%d body=%s", rec.Code, rec.Body.String())
	}
	var file struct {
		FileSize int    `json:"file_size"`
		FilePath string `json:"file_path"`
	}
	if err := json.Unmarshal(resp.Result, &file); err != nil {
		t.Fatalf("decode getFile: %v", err)
	}
	if gateway.fileLocationKey != "photo:7001:c" || file.FileSize != 4400 || file.FilePath == "" {
		t.Fatalf("getFile read %q size=%d path=%q, want photo:7001:c size=4400", gateway.fileLocationKey, file.FileSize, file.FilePath)
	}

	rec = callMethod(t, h, "sendPhoto", `{"chat_id":42,"photo":"`+biggest+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("sendPhoto: status=%d body=%s", rec.Code, rec.Body.String())
	}
	if gateway.sendMediaKind != "photo" || gateway.sendMediaLocationKey != "photo:7001:c" {
		t.Fatalf("sendPhoto sent kind=%q location=%q, want photo photo:7001:c", gateway.sendMediaKind, gateway.sendMediaLocationKey)
	}
}

package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	appbots "telesrv/internal/app/bots"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"testing"
)

func TestPaidMediaHTTPFileReferencesAreOwnerScoped(t *testing.T) {
	f := newCommerceRPCFixture(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	bots := f.r.deps.Bots.(*appbots.Service)
	_, otherToken, err := bots.CreateBot(ctx, f.buyer.ID, "Unrelated file viewer", "OtherFileBot")
	if err != nil {
		t.Fatal(err)
	}
	server, err := botapi.Start(ctx, "127.0.0.1:0", bots, f.r.deps.Users.(*appusers.Service), nil, nil, f.r, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	sellerToken := domain.FormatBotToken(f.bot.ID, "secret")
	call := func(token, method string, body []byte, contentType string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, bytes.NewReader(body))
		request.Header.Set("Content-Type", contentType)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		return response
	}
	var picture bytes.Buffer
	if err := png.Encode(&picture, image.NewRGBA(image.Rect(0, 0, 8, 8))); err != nil {
		t.Fatal(err)
	}
	var upload bytes.Buffer
	form := multipart.NewWriter(&upload)
	for name, value := range map[string]string{"chat_id": strconv.FormatInt(f.buyer.ID, 10), "star_count": "40", "media": "[{\"type\":\"photo\",\"media\":\"attach://photo\"}]"} {
		if err := form.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	part, err := form.CreateFormFile("photo", "paid.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := part.Write(picture.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err := form.Close(); err != nil {
		t.Fatal(err)
	}
	sent := call(sellerToken, "sendPaidMedia", upload.Bytes(), form.FormDataContentType())
	var message struct {
		Result struct {
			PaidMedia struct {
				Items []struct {
					Photos []struct {
						FileID string `json:"file_id"`
					} `json:"photo"`
				} `json:"paid_media"`
			} `json:"paid_media"`
		} `json:"result"`
	}
	if sent.Code != http.StatusOK {
		t.Fatalf("upload: %d %s", sent.Code, sent.Body.String())
	}
	if err := json.Unmarshal(sent.Body.Bytes(), &message); err != nil {
		t.Fatal(err)
	}
	fileID := message.Result.PaidMedia.Items[0].Photos[0].FileID
	fileBody, err := json.Marshal(map[string]string{"file_id": fileID})
	if err != nil {
		t.Fatal(err)
	}
	metadata := call(sellerToken, "getFile", fileBody, "application/json")
	var file struct {
		Result struct {
			Path string `json:"file_path"`
		} `json:"result"`
	}
	if metadata.Code != http.StatusOK {
		t.Fatalf("seller metadata: %d %s", metadata.Code, metadata.Body.String())
	}
	if err := json.Unmarshal(metadata.Body.Bytes(), &file); err != nil {
		t.Fatal(err)
	}
	download := func(token string) *httptest.ResponseRecorder {
		t.Helper()
		request := httptest.NewRequest(http.MethodGet, "/file/bot"+token+"/"+file.Result.Path, nil)
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		return response
	}
	owned := download(sellerToken)
	if owned.Code != http.StatusOK {
		t.Fatalf("seller download: %d %s", owned.Code, owned.Body.String())
	}
	decoded, _, err := image.Decode(bytes.NewReader(owned.Body.Bytes()))
	if err != nil || decoded.Bounds().Dx() != 8 || decoded.Bounds().Dy() != 8 {
		t.Fatalf("downloaded paid image: %v %v", decoded, err)
	}
	if valid := call(otherToken, "getMe", []byte("{}"), "application/json"); valid.Code != http.StatusOK {
		t.Fatalf("unrelated token is not a valid bot: %s", valid.Body.String())
	}
	for _, response := range []*httptest.ResponseRecorder{call(otherToken, "getFile", fileBody, "application/json"), download(otherToken)} {
		var denied struct {
			OK          bool            `json:"ok"`
			Description string          `json:"description"`
			Result      json.RawMessage `json:"result"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &denied); err != nil {
			t.Fatal(err)
		}
		if response.Code != http.StatusBadRequest || denied.OK || denied.Description != "FILE_ID_INVALID" || len(denied.Result) != 0 {
			t.Fatalf("foreign file access: %d %s", response.Code, response.Body.String())
		}
	}
}

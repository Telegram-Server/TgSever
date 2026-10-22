package rpc

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"testing"

	"github.com/iamxvbaba/td/tg"
	appfiles "telesrv/internal/app/files"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestBotAPIStickerHTTPAuthorizationAndLifecycle(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	media := memory.NewMediaStore()
	blobs, err := appfiles.NewLocalFS(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f.router.deps.Files = appfiles.NewService(media, blobs, 2)
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	token := strconv.FormatInt(f.bot.ID, 10) + ":secret"
	call := func(method string, payload any, want bool) json.RawMessage {
		t.Helper()
		data, err := json.Marshal(payload)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/"+method, bytes.NewReader(data))
		req.Header.Set("Content-Type", "application/json")
		rr := httptest.NewRecorder()
		server.Handler.ServeHTTP(rr, req)
		var response struct {
			OK          bool            `json:"ok"`
			Result      json.RawMessage `json:"result"`
			Description string          `json:"description"`
		}
		if err := json.Unmarshal(rr.Body.Bytes(), &response); err != nil {
			t.Fatal(err)
		}
		if response.OK != want {
			t.Fatalf("%s = %s", method, rr.Body)
		}
		return response.Result
	}
	var img bytes.Buffer
	if err := png.Encode(&img, image.NewNRGBA(image.Rect(0, 0, 512, 256))); err != nil {
		t.Fatal(err)
	}
	var body bytes.Buffer
	writer := multipart.NewWriter(&body)
	if err = writer.WriteField("user_id", strconv.FormatInt(f.member.ID, 10)); err != nil {
		t.Fatal(err)
	}
	if err = writer.WriteField("sticker_format", "static"); err != nil {
		t.Fatal(err)
	}
	part, err := writer.CreateFormFile("sticker", "art.png")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = part.Write(img.Bytes()); err != nil {
		t.Fatal(err)
	}
	if err = writer.Close(); err != nil {
		t.Fatal(err)
	}
	upload := func(want bool) string {
		t.Helper()
		req := httptest.NewRequest(http.MethodPost, "/bot"+token+"/uploadStickerFile", bytes.NewReader(body.Bytes()))
		req.Header.Set("Content-Type", writer.FormDataContentType())
		rr := httptest.NewRecorder()
		server.Handler.ServeHTTP(rr, req)
		var response struct {
			OK     bool `json:"ok"`
			Result struct {
				FileID string `json:"file_id"`
			} `json:"result"`
		}
		if json.Unmarshal(rr.Body.Bytes(), &response) != nil || response.OK != want {
			t.Fatalf("upload: %s", rr.Body)
		}
		return response.Result.FileID
	}
	upload(false) // Knowing an arbitrary user ID is not authority to act for them.
	if _, err = f.bots.AllowSendMessage(f.ctx, f.member.ID, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	fileID := upload(true)
	input := map[string]any{"sticker": fileID, "format": "static", "emoji_list": []string{"🙂", "😄"}, "keywords": []string{"one,two"}}
	name := "http_stickers_by_bridgebot"
	call("createNewStickerSet", map[string]any{"user_id": f.member.ID, "name": name, "title": "HTTP stickers", "stickers": []any{input}}, true)
	raw := call("getStickerSet", map[string]any{"name": name}, true)
	var set struct {
		Name     string `json:"name"`
		Type     string `json:"sticker_type"`
		Stickers []struct {
			FileID string `json:"file_id"`
			Width  int    `json:"width"`
			Height int    `json:"height"`
			Emoji  string `json:"emoji"`
		} `json:"stickers"`
	}
	if err = json.Unmarshal(raw, &set); err != nil {
		t.Fatal(err)
	}
	if set.Name != name || set.Type != "regular" || len(set.Stickers) != 1 || set.Stickers[0].Width != 512 || set.Stickers[0].Height != 256 {
		t.Fatalf("projected set: %s", raw)
	}
	sticker := set.Stickers[0].FileID
	call("setStickerEmojiList", map[string]any{"sticker": sticker, "emoji_list": []string{"💚"}}, true)
	call("setStickerKeywords", map[string]any{"sticker": sticker, "keywords": []string{"new,term"}}, true)
	call("setStickerPositionInSet", map[string]any{"sticker": sticker, "position": 0}, true)
	call("setStickerMaskPosition", map[string]any{"sticker": sticker, "mask_position": map[string]any{"point": "eyes", "scale": 1}}, false)
	call("setStickerSetTitle", map[string]any{"name": name, "title": "New title"}, true)
	call("addStickerToSet", map[string]any{"user_id": f.member.ID, "name": name, "sticker": input}, true)
	call("replaceStickerInSet", map[string]any{"user_id": f.member.ID, "name": name, "old_sticker": sticker, "sticker": input}, true)
	call("setStickerSetThumbnail", map[string]any{"user_id": f.member.ID, "name": name, "format": "static"}, true)
	call("setCustomEmojiStickerSetThumbnail", map[string]any{"name": name}, false)
	var emojiPNG bytes.Buffer
	if err = png.Encode(&emojiPNG, image.NewNRGBA(image.Rect(0, 0, 100, 100))); err != nil {
		t.Fatal(err)
	}
	emojiDoc, err := f.router.deps.Files.(botStickerFiles).BotStickerFile(f.ctx, domain.BotAPIFileInput{Bytes: emojiPNG.Bytes()}, "static", domain.StickerSetKindEmoji, false)
	if err != nil {
		t.Fatal(err)
	}
	emojiInput := map[string]any{"sticker": base64.RawURLEncoding.EncodeToString([]byte(fmt.Sprintf("doc:%d", emojiDoc.ID))), "format": "static", "emoji_list": []string{"💚"}}
	emojiName := "http_emoji_by_bridgebot"
	call("createNewStickerSet", map[string]any{"user_id": f.member.ID, "name": emojiName, "title": "Emoji", "sticker_type": "custom_emoji", "needs_repainting": true, "stickers": []any{emojiInput}}, true)
	raw = call("getCustomEmojiStickers", map[string]any{"custom_emoji_ids": []string{fmt.Sprint(emojiDoc.ID)}}, true)
	var custom []struct {
		Type    string `json:"type"`
		ID      string `json:"custom_emoji_id"`
		Repaint bool   `json:"needs_repainting"`
	}
	if json.Unmarshal(raw, &custom) != nil || len(custom) != 1 || custom[0].Type != "custom_emoji" || custom[0].ID != fmt.Sprint(emojiDoc.ID) || !custom[0].Repaint {
		t.Fatalf("custom emoji projection: %s", raw)
	}
	call("setCustomEmojiStickerSetThumbnail", map[string]any{"name": emojiName, "custom_emoji_id": fmt.Sprint(emojiDoc.ID)}, true)
	call("deleteStickerSet", map[string]any{"name": emojiName}, true)
	maskName := "http_mask_by_bridgebot"
	maskInput := map[string]any{"sticker": fileID, "format": "static", "emoji_list": []string{"🙂"}, "mask_position": map[string]any{"point": "eyes", "scale": 1}}
	call("createNewStickerSet", map[string]any{"user_id": f.member.ID, "name": maskName, "title": "Masks", "sticker_type": "mask", "stickers": []any{maskInput}}, true)
	raw = call("getStickerSet", map[string]any{"name": maskName}, true)
	if err = json.Unmarshal(raw, &set); err != nil || set.Type != "mask" || len(set.Stickers) != 1 {
		t.Fatalf("mask creation: %s %v", raw, err)
	}
	maskFile := set.Stickers[0].FileID
	call("setStickerMaskPosition", map[string]any{"sticker": maskFile, "mask_position": map[string]any{"point": "chin", "x_shift": -0.5, "y_shift": 1.25, "scale": 2}}, true)
	maskSet, maskDocs, err := f.router.BotAPIGetStickerSet(f.ctx, f.bot.ID, maskName)
	if err != nil || maskSet.Kind != domain.StickerSetKindMasks {
		t.Fatalf("native mask load: %+v %v", maskSet, err)
	}
	var coords tg.MaskCoords
	var present bool
	for _, a := range tgDocumentAttributes(maskDocs[0].MimeType, maskDocs[0].Attributes) {
		if sticker, ok := a.(*tg.DocumentAttributeSticker); ok {
			coords, present = sticker.GetMaskCoords()
		}
	}
	if !present || coords.N != 3 || coords.X != -0.5 || coords.Y != 1.25 || coords.Zoom != 2 {
		t.Fatalf("Bot API edit not visible in native coordinates: %+v", coords)
	}
	call("setStickerMaskPosition", map[string]any{"sticker": maskFile}, true)
	call("deleteStickerSet", map[string]any{"name": maskName}, true)
	raw = call("getStickerSet", map[string]any{"name": name}, true)
	if json.Unmarshal(raw, &set) != nil || set.Type != "regular" || set.Stickers[0].FileID != sticker {
		t.Fatalf("reused file changed original pack: %s", raw)
	}
	// Same user ownership is insufficient: a second bot may not mutate this pack.
	second, secondToken, err := f.bots.CreateBot(f.ctx, f.owner.ID, "Second", "SecondStickerBot")
	if err != nil {
		t.Fatal(err)
	}
	if _, err = f.bots.AllowSendMessage(f.ctx, f.member.ID, second.ID, true); err != nil {
		t.Fatal(err)
	}
	originalToken := token
	token = secondToken
	call("setStickerSetTitle", map[string]any{"name": name, "title": "Stolen"}, false)
	call("addStickerToSet", map[string]any{"user_id": f.member.ID, "name": name, "sticker": input}, false)
	call("deleteStickerSet", map[string]any{"name": name}, false)
	token = originalToken
	call("deleteStickerFromSet", map[string]any{"sticker": sticker}, true)
	call("deleteStickerSet", map[string]any{"name": name}, true)
	call("getStickerSet", map[string]any{"name": name}, false)
}

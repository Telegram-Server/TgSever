package botapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/domain"
)

// fakeBotProfilePhotoGateway 在默认假网关上补 BotProfilePhotoGateway，记录调用的机器人。
type fakeBotProfilePhotoGateway struct {
	*fakeBotAPIGateway
	removeCalls []int64
	setCalls    []domain.BotAPIProfilePhoto
	err         error
}

func (g *fakeBotProfilePhotoGateway) BotAPISetMyProfilePhoto(_ context.Context, botID int64, photo domain.BotAPIProfilePhoto) (bool, error) {
	if botID != testBotProfile.BotUserID {
		return false, errors.New("BOT_INVALID")
	}
	g.setCalls = append(g.setCalls, photo)
	if g.err != nil {
		return false, g.err
	}
	return true, nil
}

func (g *fakeBotProfilePhotoGateway) BotAPIRemoveMyProfilePhoto(_ context.Context, botID int64) (bool, error) {
	g.removeCalls = append(g.removeCalls, botID)
	if g.err != nil {
		return false, g.err
	}
	return true, nil
}

func TestRemoveMyProfilePhotoReturnsTrue(t *testing.T) {
	gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}

	resp := decodeAPIResponse(t, callMethod(t, newTestHandler(t, gateway), "removeMyProfilePhoto", `{}`))

	if !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("removeMyProfilePhoto = %+v, want ok true", resp)
	}
	if len(gateway.removeCalls) != 1 || gateway.removeCalls[0] != testBotProfile.BotUserID {
		t.Fatalf("gateway calls = %v, want one call for bot %d", gateway.removeCalls, testBotProfile.BotUserID)
	}
}

func TestRemoveMyProfilePhotoReportsGatewayError(t *testing.T) {
	gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, err: errors.New("BOT_INVALID")}

	rec := callMethod(t, newTestHandler(t, gateway), "removeMyProfilePhoto", `{}`)
	resp := decodeAPIResponse(t, rec)

	if resp.OK || rec.Code != http.StatusBadRequest || resp.Description != "BOT_INVALID" {
		t.Fatalf("removeMyProfilePhoto with gateway error = %d %+v, want 400 BOT_INVALID", rec.Code, resp)
	}
}

// 网关没有实现这组接口时回 501，与其他可选方法一致。
func TestRemoveMyProfilePhotoWithoutGatewaySupport(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "removeMyProfilePhoto", `{}`)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("removeMyProfilePhoto without gateway = %d %s, want 501", rec.Code, rec.Body.String())
	}
}

// setMyProfilePhoto 的 static 头像：InputProfilePhotoStatic 的 photo 用 attach:// 指向本次
// multipart 上传的文件，网关原样拿到文件字节、文件名与种类。
func TestSetMyProfilePhotoPassesStaticUpload(t *testing.T) {
	gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}

	resp := postMultipart(t, gateway, "setMyProfilePhoto",
		map[string]string{"photo": `{"type":"static","photo":"attach://pic"}`},
		map[string][]byte{"pic": []byte("jpeg bytes")})

	if !resp.OK || string(resp.Result) != "true" {
		t.Fatalf("setMyProfilePhoto = %+v, want ok true", resp)
	}
	if len(gateway.setCalls) != 1 {
		t.Fatalf("gateway calls = %+v, want one", gateway.setCalls)
	}
	got := gateway.setCalls[0]
	if got.Kind != domain.BotAPIProfilePhotoStatic || string(got.Data) != "jpeg bytes" || got.FileName != "pic.jpg" {
		t.Fatalf("gateway photo = kind %q name %q data %q, want static pic.jpg \"jpeg bytes\"", got.Kind, got.FileName, got.Data)
	}
}

// setMyProfilePhoto 的 animated 头像：animation 用 attach:// 指向上传的视频，
// main_frame_timestamp（数字或数字字符串）原样交给网关；缺省为 0。
func TestSetMyProfilePhotoPassesAnimatedUpload(t *testing.T) {
	cases := []struct {
		name      string
		photo     string
		wantFrame float64
	}{
		{"with main frame", `{"type":"animated","animation":"attach://clip","main_frame_timestamp":1.25}`, 1.25},
		{"main frame as string", `{"type":"animated","animation":"attach://clip","main_frame_timestamp":"2.5"}`, 2.5},
		{"default main frame", `{"type":"animated","animation":"attach://clip"}`, 0},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}

			resp := postMultipart(t, gateway, "setMyProfilePhoto",
				map[string]string{"photo": tc.photo},
				map[string][]byte{"clip": []byte("mp4 bytes")})

			if !resp.OK || string(resp.Result) != "true" {
				t.Fatalf("setMyProfilePhoto = %+v, want ok true", resp)
			}
			if len(gateway.setCalls) != 1 {
				t.Fatalf("gateway calls = %+v, want one", gateway.setCalls)
			}
			got := gateway.setCalls[0]
			if got.Kind != domain.BotAPIProfilePhotoAnimated || string(got.Data) != "mp4 bytes" || got.MainFrameTimestamp != tc.wantFrame {
				t.Fatalf("gateway photo = kind %q data %q main frame %v, want animated \"mp4 bytes\" %v", got.Kind, got.Data, got.MainFrameTimestamp, tc.wantFrame)
			}
		})
	}
}

// animated 的参数错误：没有上传的 animation、主帧时间戳不是数字或为负，都在网关层拒绝。
func TestSetMyProfilePhotoRejectsMalformedAnimated(t *testing.T) {
	cases := []struct {
		name  string
		photo string
		want  string
	}{
		{"photo field instead of animation", `{"type":"animated","photo":"attach://clip"}`, "Bad Request: profile photo must be uploaded as a new file using attach://"},
		{"animation file_id", `{"type":"animated","animation":"CgACAgIAAxkBAAIB"}`, "Bad Request: profile photo must be uploaded as a new file using attach://"},
		{"main frame not a number", `{"type":"animated","animation":"attach://clip","main_frame_timestamp":"soon"}`, `Bad Request: field "main_frame_timestamp" must be a valid Number`},
		{"main frame object", `{"type":"animated","animation":"attach://clip","main_frame_timestamp":{}}`, `Bad Request: field "main_frame_timestamp" must be a valid Number`},
		{"negative main frame", `{"type":"animated","animation":"attach://clip","main_frame_timestamp":-1}`, "Bad Request: wrong main frame timestamp specified"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
			resp := postMultipart(t, gateway, "setMyProfilePhoto",
				map[string]string{"photo": tc.photo},
				map[string][]byte{"clip": []byte("mp4 bytes")})
			if resp.OK || resp.Description != tc.want {
				t.Fatalf("setMyProfilePhoto = %+v, want error %q", resp, tc.want)
			}
			if len(gateway.setCalls) != 0 {
				t.Fatalf("gateway called with %+v", gateway.setCalls)
			}
		})
	}
}

// InputProfilePhoto 解析失败时在网关层就给出明确错误，不调用网关。
func TestSetMyProfilePhotoRejectsMalformedInputProfilePhoto(t *testing.T) {
	pic := map[string][]byte{"pic": []byte("jpeg bytes")}
	cases := []struct {
		name   string
		fields map[string]string
		want   string
	}{
		{"missing photo", map[string]string{}, `Bad Request: parameter "photo" is required`},
		{"not json", map[string]string{"photo": "attach://pic"}, "Bad Request: can't parse InputProfilePhoto JSON object"},
		{"not an object", map[string]string{"photo": `["static"]`}, "Bad Request: can't parse InputProfilePhoto JSON object"},
		{"missing type", map[string]string{"photo": `{"photo":"attach://pic"}`}, "Bad Request: invalid InputProfilePhoto type specified"},
		{"unknown type", map[string]string{"photo": `{"type":"sticker","photo":"attach://pic"}`}, "Bad Request: invalid InputProfilePhoto type specified"},
		{"static without file", map[string]string{"photo": `{"type":"static"}`}, "Bad Request: profile photo must be uploaded as a new file using attach://"},
		{"static with file_id", map[string]string{"photo": `{"type":"static","photo":"AgACAgIAAxkBAAIB"}`}, "Bad Request: profile photo must be uploaded as a new file using attach://"},
		{"static with url", map[string]string{"photo": `{"type":"static","photo":"https://example.com/a.jpg"}`}, "Bad Request: profile photo must be uploaded as a new file using attach://"},
		{"attach to missing field", map[string]string{"photo": `{"type":"static","photo":"attach://other"}`}, "Bad Request: profile photo must be uploaded as a new file using attach://"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}
			resp := postMultipart(t, gateway, "setMyProfilePhoto", tc.fields, pic)
			if resp.OK || resp.Description != tc.want {
				t.Fatalf("setMyProfilePhoto = %+v, want error %q", resp, tc.want)
			}
			if len(gateway.setCalls) != 0 {
				t.Fatalf("gateway called with %+v", gateway.setCalls)
			}
		})
	}
}

// JSON body 带不了文件：photo 对象即使形状正确也找不到 attach:// 指向的上传。
func TestSetMyProfilePhotoJSONBodyHasNoUpload(t *testing.T) {
	gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}

	resp := decodeAPIResponse(t, callMethod(t, newTestHandler(t, gateway), "setMyProfilePhoto", `{"photo":{"type":"static","photo":"attach://pic"}}`))

	if resp.OK || resp.Description != "Bad Request: profile photo must be uploaded as a new file using attach://" || len(gateway.setCalls) != 0 {
		t.Fatalf("setMyProfilePhoto via JSON = %+v calls=%d, want upload error", resp, len(gateway.setCalls))
	}
}

// 处理器报告的格式错误与过大错误映射成明确的说明。
func TestSetMyProfilePhotoDescribesGatewayErrors(t *testing.T) {
	cases := map[string]string{
		"PHOTO_INVALID": "Bad Request: IMAGE_PROCESS_FAILED",
		"FILE_TOO_BIG":  "Bad Request: file is too big",
		"BOT_INVALID":   "BOT_INVALID",
	}
	for code, want := range cases {
		gateway := &fakeBotProfilePhotoGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, err: errors.New(code)}
		resp := postMultipart(t, gateway, "setMyProfilePhoto",
			map[string]string{"photo": `{"type":"static","photo":"attach://pic"}`},
			map[string][]byte{"pic": []byte("jpeg bytes")})
		if resp.OK || resp.Description != want {
			t.Fatalf("gateway error %s: response %+v, want %q", code, resp, want)
		}
	}
}

func TestSetMyProfilePhotoWithoutGatewaySupport(t *testing.T) {
	rec := callMethod(t, newTestHandler(t, &fakeBotAPIGateway{}), "setMyProfilePhoto", `{}`)

	if rec.Code != http.StatusNotImplemented {
		t.Fatalf("setMyProfilePhoto without gateway = %d %s, want 501", rec.Code, rec.Body.String())
	}
}

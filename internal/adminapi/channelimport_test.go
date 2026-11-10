package adminapi

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"telesrv/internal/admin"
	"telesrv/internal/app/channelimport"
	"telesrv/internal/domain"
)

// captureChannelImportService 记录每个调用的参数并返回预置的结果；业务规则由
// app/channelimport 自己的测试覆盖，这里只看路由、鉴权、解码和错误映射。
type captureChannelImportService struct {
	fakeService
	err error

	created   []channelimport.CreateRequest
	uploads   []channelimport.MediaUpload
	bodies    []string
	batches   [][]channelimport.Message
	finalized []channelimport.FinalizeRequest
	deleted   [][]int
	actors    []string
	metas     []channelimport.Meta
	detached  []int64
	existing  bool
}

func (s *captureChannelImportService) ChannelImport() (admin.ChannelImportService, bool) {
	return s, true
}

func (s *captureChannelImportService) status(id int64) channelimport.Status {
	return channelimport.Status{
		Import:  domain.ChannelImport{ID: id, ChannelID: 4001, State: domain.ChannelImportImporting, SenderUserID: 10, MaxSourceID: 3},
		Channel: domain.Channel{ID: 4001, Title: "Mirror", CreatorUserID: 10, TopMessageID: 3},
		Stats:   domain.ChannelImportStats{Messages: 2, Media: 1},
	}
}

func (s *captureChannelImportService) Create(_ context.Context, req channelimport.CreateRequest) (channelimport.CreateResult, error) {
	s.created = append(s.created, req)
	return channelimport.CreateResult{Status: s.status(7), Existing: s.existing}, s.err
}

func (s *captureChannelImportService) Get(_ context.Context, id int64) (channelimport.Status, error) {
	return s.status(id), s.err
}

func (s *captureChannelImportService) PutMedia(_ context.Context, _ int64, upload channelimport.MediaUpload) (channelimport.MediaResult, error) {
	body, err := io.ReadAll(upload.Body)
	if err != nil {
		return channelimport.MediaResult{}, err
	}
	upload.Body = nil
	s.uploads = append(s.uploads, upload)
	s.bodies = append(s.bodies, string(body))
	return channelimport.MediaResult{SourceKey: upload.SourceKey, Media: domain.ChannelImportMedia{Kind: upload.Kind, MediaID: 9_007_199_254_740_993}}, s.err
}

func (s *captureChannelImportService) ImportMessages(_ context.Context, _ int64, messages []channelimport.Message) (channelimport.BatchResult, error) {
	s.batches = append(s.batches, messages)
	return channelimport.BatchResult{
		Import:   domain.ChannelImport{ID: 7, State: domain.ChannelImportImporting, MaxSourceID: 3},
		Written:  2,
		LocalIDs: map[int]int{2: 2, 3: 3},
		Skipped:  []channelimport.Skipped{{ID: 4, Reason: "unsupported action \"x\""}},
	}, s.err
}

func (s *captureChannelImportService) Finalize(_ context.Context, id int64, req channelimport.FinalizeRequest) (channelimport.Status, error) {
	s.finalized = append(s.finalized, req)
	status := s.status(id)
	status.Import.State = domain.ChannelImportMirroring
	return status, s.err
}

func (s *captureChannelImportService) DeleteMessages(_ context.Context, _ int64, ids []int, actor string) (channelimport.DeleteResult, error) {
	s.deleted = append(s.deleted, ids)
	s.actors = append(s.actors, actor)
	return channelimport.DeleteResult{Deleted: []int{2}, Unknown: []int{99}}, s.err
}

func (s *captureChannelImportService) SetMeta(_ context.Context, id int64, meta channelimport.Meta) (channelimport.Status, error) {
	s.metas = append(s.metas, meta)
	return s.status(id), s.err
}

func (s *captureChannelImportService) Detach(_ context.Context, id int64) (channelimport.Status, error) {
	s.detached = append(s.detached, id)
	return s.status(id), s.err
}

func newChannelImportServer(svc Service) *Server {
	return &Server{
		token: "secret",
		scoped: []ScopedToken{
			{Name: "cloner", Token: "import-token", Permissions: []string{PermissionChannelsImport}},
			{Name: "prices", Token: "prices-token", Permissions: []string{PermissionPricesManage}},
		},
		svc: svc,
		log: zap.NewNop(),
	}
}

func channelImportDo(srv *Server, method, path, token, body string) *httptest.ResponseRecorder {
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	rec := httptest.NewRecorder()
	srv.routes().ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %q: %v", rec.Body.String(), err)
	}
	return out
}

// 整组路由只认 channels.import：没有 token 是 401，别的权限的 token 是 403，带这个权限的
// scoped token 和 master token 都能调。
func TestAdminAPIChannelImportPermission(t *testing.T) {
	srv := newChannelImportServer(&captureChannelImportService{})
	routes := []struct{ method, path, body string }{
		{http.MethodPost, "/v1/channel-imports", `{}`},
		{http.MethodGet, "/v1/channel-imports/7", ``},
		{http.MethodPost, "/v1/channel-imports/7/media?source_key=k&kind=photo", `x`},
		{http.MethodPost, "/v1/channel-imports/7/messages", `{"messages":[]}`},
		{http.MethodPost, "/v1/channel-imports/7/messages/delete", `{"ids":[1]}`},
		{http.MethodPost, "/v1/channel-imports/7/finalize", `{}`},
		{http.MethodPost, "/v1/channel-imports/7/meta", `{"title":"x"}`},
		{http.MethodPost, "/v1/channel-imports/7/photo", `x`},
		{http.MethodPost, "/v1/channel-imports/7/detach", ``},
	}
	for _, route := range routes {
		if rec := channelImportDo(srv, route.method, route.path, "", route.body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a token: status=%d, want 401", route.method, route.path, rec.Code)
		}
		rec := channelImportDo(srv, route.method, route.path, "prices-token", route.body)
		if rec.Code != http.StatusForbidden || decodeBody(t, rec)["permission"] != PermissionChannelsImport {
			t.Fatalf("%s %s with another scope: status=%d body=%s, want 403 naming channels.import", route.method, route.path, rec.Code, rec.Body.String())
		}
		for _, token := range []string{"import-token", "secret"} {
			if rec := channelImportDo(srv, route.method, route.path, token, route.body); rec.Code < 200 || rec.Code > 299 {
				t.Fatalf("%s %s with token %q: status=%d body=%s", route.method, route.path, token, rec.Code, rec.Body.String())
			}
		}
	}
	// 这个权限不通向别的后台路由。
	if rec := channelImportDo(srv, http.MethodPost, "/v1/channels/create-group", "import-token", `{}`); rec.Code != http.StatusForbidden {
		t.Fatalf("create-group with the import token: status=%d, want 403", rec.Code)
	}
}

func TestAdminAPIChannelImportFlow(t *testing.T) {
	svc := &captureChannelImportService{}
	srv := newChannelImportServer(svc)

	create := `{"source":{"channel_id":1234567890,"username":"durov"},
		"channel":{"owner_user_id":10,"title":"Mirror","about":"a","created_date":1420070400,"create_title":"First title","create_message_missing":true},
		"sender_user_id":11}`
	rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports", "import-token", create)
	if rec.Code != http.StatusCreated {
		t.Fatalf("create status=%d body=%s", rec.Code, rec.Body.String())
	}
	want := channelimport.CreateRequest{
		SourceChannelID: 1234567890, SourceUsername: "durov", OwnerUserID: 10, Title: "Mirror", About: "a",
		CreatedDate: 1420070400, CreateTitle: "First title", CreateMessageMissing: true, SenderUserID: 11,
	}
	if len(svc.created) != 1 || svc.created[0] != want {
		t.Fatalf("create request = %+v, want %+v", svc.created, want)
	}
	body := decodeBody(t, rec)
	if body["import_id"] != float64(7) || body["channel_id"] != float64(4001) || body["state"] != "importing" || body["max_source_id"] != float64(3) {
		t.Fatalf("create response = %v", body)
	}
	if channel := body["channel"].(map[string]any); channel["title"] != "Mirror" || channel["owner_user_id"] != float64(10) {
		t.Fatalf("create response channel = %v", channel)
	}
	// 来源已有任务：200 而不是 201，并标明 existing。
	svc.existing = true
	rec = channelImportDo(srv, http.MethodPost, "/v1/channel-imports", "import-token", `{"target_channel_id":4001}`)
	if rec.Code != http.StatusOK || decodeBody(t, rec)["existing"] != true || svc.created[1].TargetChannelID != 4001 {
		t.Fatalf("existing create status=%d body=%s request=%+v", rec.Code, rec.Body.String(), svc.created[1])
	}

	// 媒体：请求体就是文件字节，元数据在查询参数里。
	media := "/v1/channel-imports/7/media?source_key=AgAD-1&kind=document&date=1420070500&mime_type=video%2Fmp4" +
		"&attributes=" + `%5B%7B%22kind%22%3A%22video%22%2C%22w%22%3A640%2C%22h%22%3A360%2C%22duration%22%3A12.5%7D%2C%7B%22kind%22%3A%22filename%22%2C%22file_name%22%3A%22clip.mp4%22%7D%5D`
	rec = channelImportDo(srv, http.MethodPost, media, "import-token", "mp4-bytes")
	if rec.Code != http.StatusCreated {
		t.Fatalf("media status=%d body=%s", rec.Code, rec.Body.String())
	}
	upload := svc.uploads[0]
	if upload.SourceKey != "AgAD-1" || upload.Kind != domain.ChannelImportMediaDocument || upload.Date != 1420070500 || upload.MimeType != "video/mp4" || svc.bodies[0] != "mp4-bytes" {
		t.Fatalf("upload = %+v body %q", upload, svc.bodies[0])
	}
	if len(upload.Attributes) != 2 || upload.Attributes[0].Kind != domain.DocAttrVideo || upload.Attributes[0].Duration != 12.5 || upload.Attributes[1].FileName != "clip.mp4" {
		t.Fatalf("upload attributes = %+v", upload.Attributes)
	}
	// 媒体 id 超出 JSON 数字的精确范围，以字符串给出。
	if got := decodeBody(t, rec)["media_id"]; got != "9007199254740993" {
		t.Fatalf("media_id = %v, want the exact id as a string", got)
	}
	for _, bad := range []string{
		"/v1/channel-imports/7/media?source_key=k&kind=photo&date=yesterday",
		"/v1/channel-imports/7/media?source_key=k&kind=document&attributes=%7B%7D",
	} {
		if rec := channelImportDo(srv, http.MethodPost, bad, "import-token", "x"); rec.Code != http.StatusBadRequest {
			t.Fatalf("%s status=%d, want 400", bad, rec.Code)
		}
	}

	batch := `{"messages":[
		{"id":2,"date":1420070500,"text":"hello","entities":[{"type":"bold","offset":0,"length":5}],"views":120,"pinned":true,
		 "grouped_id":"13000000000000001","media":{"source_key":"AgAD-1","spoiler":true},
		 "fwd_from":{"from_name":"Other","date":1420000000,"from_source_channel_id":55,"channel_post":8},
		 "buttons":[[{"text":"Open","url":"https://example.com"}]]},
		{"id":3,"date":1420070600,"reply_to_msg_id":2,"action":{"type":"pin_message"}}
	]}`
	rec = channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/messages", "import-token", batch)
	if rec.Code != http.StatusOK {
		t.Fatalf("messages status=%d body=%s", rec.Code, rec.Body.String())
	}
	got := svc.batches[0]
	if len(got) != 2 || got[0].ID != 2 || got[0].Text != "hello" || got[0].GroupedID != 13000000000000001 || !got[0].Pinned || got[0].Views != 120 ||
		got[0].Media == nil || got[0].Media.SourceKey != "AgAD-1" || !got[0].Media.Spoiler ||
		got[0].Forward == nil || got[0].Forward.FromSourceChannelID != 55 || got[0].Forward.ChannelPost != 8 ||
		len(got[0].Buttons) != 1 || got[0].Buttons[0][0].URL != "https://example.com" || len(got[0].Entities) != 1 {
		t.Fatalf("decoded message 2 = %+v", got[0])
	}
	if got[1].Action == nil || got[1].Action.Type != "pin_message" || got[1].ReplyToMsgID != 2 {
		t.Fatalf("decoded message 3 = %+v", got[1])
	}
	body = decodeBody(t, rec)
	if body["written"] != float64(2) || body["max_source_id"] != float64(3) || body["local_ids"].(map[string]any)["3"] != float64(3) {
		t.Fatalf("messages response = %v", body)
	}
	if skipped := body["skipped"].([]any); len(skipped) != 1 || skipped[0].(map[string]any)["id"] != float64(4) {
		t.Fatalf("messages response skipped = %v", body["skipped"])
	}
	// 写错的字段名不能被悄悄忽略。
	if rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/messages", "import-token", `{"messages":[{"id":2,"date":1,"txt":"typo"}]}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("unknown field status=%d, want 400", rec.Code)
	}

	rec = channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/messages/delete", "import-token", `{"ids":[2,99]}`)
	if rec.Code != http.StatusOK || len(svc.deleted[0]) != 2 || svc.actors[0] != "cloner" {
		t.Fatalf("delete status=%d ids=%v actors=%v, want the scoped token name as the actor", rec.Code, svc.deleted, svc.actors)
	}
	if body := decodeBody(t, rec); len(body["deleted_local_ids"].([]any)) != 1 || len(body["unknown_ids"].([]any)) != 1 {
		t.Fatalf("delete response = %v", body)
	}
	if rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/messages/delete", "secret", `{"ids":[2]}`); rec.Code != http.StatusOK || svc.actors[1] != "admin-api" {
		t.Fatalf("master-token delete status=%d actors=%v", rec.Code, svc.actors)
	}

	rec = channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/finalize", "import-token", `{"username":"mirror","reserve_through":510}`)
	if rec.Code != http.StatusOK || svc.finalized[0] != (channelimport.FinalizeRequest{Username: "mirror", ReserveThrough: 510}) || decodeBody(t, rec)["state"] != "mirroring" {
		t.Fatalf("finalize status=%d request=%+v body=%s", rec.Code, svc.finalized, rec.Body.String())
	}

	rec = channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/meta", "import-token", `{"about":""}`)
	if rec.Code != http.StatusOK || svc.metas[0].Title != nil || svc.metas[0].About == nil || *svc.metas[0].About != "" {
		t.Fatalf("meta status=%d meta=%+v, want about cleared and the title untouched", rec.Code, svc.metas[0])
	}
	if rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/meta", "import-token", `{}`); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty meta status=%d, want 400", rec.Code)
	}
	rec = channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/photo", "import-token", "jpeg-bytes")
	if rec.Code != http.StatusOK || string(svc.metas[1].Photo) != "jpeg-bytes" {
		t.Fatalf("photo status=%d meta=%+v", rec.Code, svc.metas[1])
	}
	if rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/photo", "import-token", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("empty photo status=%d, want 400", rec.Code)
	}

	if rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/detach", "import-token", ""); rec.Code != http.StatusOK || svc.detached[0] != 7 {
		t.Fatalf("detach status=%d detached=%v", rec.Code, svc.detached)
	}
	if rec := channelImportDo(srv, http.MethodGet, "/v1/channel-imports/abc", "import-token", ""); rec.Code != http.StatusBadRequest {
		t.Fatalf("non-numeric id status=%d, want 400", rec.Code)
	}
}

// 业务错误映射成克隆程序能区分的状态码和错误码。
func TestAdminAPIChannelImportErrors(t *testing.T) {
	cases := []struct {
		err    error
		status int
		code   string
	}{
		{domain.ErrChannelImportNotFound, http.StatusNotFound, codeChannelImportNotFound},
		{domain.ErrChannelImportState, http.StatusConflict, codeChannelImportState},
		{domain.ErrChannelImportOrder, http.StatusConflict, codeChannelImportOrder},
		{domain.ErrChannelImportTarget, http.StatusConflict, codeChannelImportTarget},
		{domain.ErrChannelImportExists, http.StatusConflict, codeChannelImportExists},
		{domain.ErrUsernameOccupied, http.StatusConflict, codeChannelImportUsername},
		{channelimport.ErrOwnerInvalid, http.StatusBadRequest, codeChannelImportOwner},
		{channelimport.ErrMediaInvalid, http.StatusBadRequest, codeChannelImportMedia},
		{domain.ErrChannelInvalid, http.StatusBadRequest, codeChannelImportInvalid},
		{domain.ErrChannelTitleInvalid, http.StatusBadRequest, codeChannelImportInvalid},
		{io.ErrUnexpectedEOF, http.StatusInternalServerError, ""},
	}
	for _, tc := range cases {
		srv := newChannelImportServer(&captureChannelImportService{err: tc.err})
		rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/messages", "import-token", `{"messages":[{"id":2,"date":1,"text":"x"}]}`)
		body := decodeBody(t, rec)
		code, _ := body["code"].(string)
		if rec.Code != tc.status || code != tc.code {
			t.Fatalf("%v: status=%d code=%q body=%s, want %d %q", tc.err, rec.Code, code, rec.Body.String(), tc.status, tc.code)
		}
	}

	// 没有接入频道导入的服务：501，而不是一个模糊的 500。
	srv := newChannelImportServer(&fakeService{})
	if rec := channelImportDo(srv, http.MethodGet, "/v1/channel-imports/7", "import-token", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured status=%d, want 501", rec.Code)
	}
}

package adminapi

import (
	"context"
	"errors"
	"net/http"
	"testing"

	"telesrv/internal/app/channelimport"
	"telesrv/internal/domain"
)

// 只读方法的测试替身：记录参数，返回预置的结果。业务规则在 app/channelimport 的测试里。

func (s *captureChannelImportService) Ping(context.Context) error { return s.pingErr }

func (s *captureChannelImportService) List(_ context.Context, filter domain.ChannelImportFilter) ([]channelimport.ListedImport, error) {
	s.filters = append(s.filters, filter)
	return []channelimport.ListedImport{{
		Import:  domain.ChannelImport{ID: 7, ChannelID: 4001, State: domain.ChannelImportMirroring, SourceChannelID: 1234567890, MaxSourceID: 9},
		Channel: domain.Channel{ID: 4001, Title: "Mirror", Username: "mirror", CreatorUserID: 10},
	}}, s.err
}

func (s *captureChannelImportService) channelInfo(id int64) channelimport.ChannelInfo {
	return channelimport.ChannelInfo{
		ChannelImportChannelSummary: domain.ChannelImportChannelSummary{
			Channel:         domain.Channel{ID: id, Title: "Mirror", About: "a", Username: "mirror", Broadcast: true, CreatorUserID: 10, Date: 1420070400, Pts: 5300, ParticipantsCount: 128, PinnedMessageID: 3, PhotoID: 55},
			LastMessageID:   5120,
			LastMessageDate: 1794001234,
			MaxMessageID:    5120,
			Import:          &domain.ChannelImport{ID: 7, State: domain.ChannelImportMirroring, SourceChannelID: 1234567890, MaxSourceID: 5120},
		},
		Owner:        &domain.User{ID: 10, FirstName: "Olga", LastName: "K", Username: "olga"},
		MessageCount: -1,
	}
}

func (s *captureChannelImportService) Channel(_ context.Context, id int64) (channelimport.ChannelInfo, error) {
	info := s.channelInfo(id)
	info.MessageCount = 4987
	return info, s.err
}

func (s *captureChannelImportService) SearchChannels(_ context.Context, search channelimport.ChannelSearch) ([]channelimport.ChannelInfo, error) {
	s.searches = append(s.searches, search)
	empty := channelimport.ChannelInfo{
		ChannelImportChannelSummary: domain.ChannelImportChannelSummary{
			Channel: domain.Channel{ID: 4002, Title: "Mirror", Broadcast: true, CreatorUserID: 11, Date: 1700000000}, LastMessageID: 1, MaxMessageID: 1, LastMessageDate: 1700000000,
		},
		MessageCount: -1,
	}
	return []channelimport.ChannelInfo{s.channelInfo(4001), empty}, s.err
}

func (s *captureChannelImportService) ChannelMessages(_ context.Context, channelID int64, beforeID, limit int) ([]channelimport.MessageView, error) {
	s.messagePages = append(s.messagePages, [3]int{int(channelID), beforeID, limit})
	source := 9
	all := []channelimport.MessageView{
		{ID: 10, Date: 1794001234, Text: "local"},
		{ID: 9, SourceID: &source, Date: 1500000009, Text: "nine", Views: 12, GroupedID: 13000000000000001},
		{ID: 3, Date: 1500000003, Action: &channelimport.ActionView{Type: "edit_title", Title: "Old"}},
	}
	if limit < len(all) {
		all = all[:limit]
	}
	return all, s.err
}

func (s *captureChannelImportService) ResolveMessageIDs(_ context.Context, _ int64, sourceIDs []int) (map[int]int, error) {
	s.resolved = append(s.resolved, sourceIDs)
	return map[int]int{2: 2, 41: 42}, s.err
}

// 只读路由同样只认 channels.import。
func TestAdminAPIChannelImportReadPermission(t *testing.T) {
	srv := newChannelImportServer(&captureChannelImportService{})
	routes := []struct{ method, path, body string }{
		{http.MethodGet, "/v1/channel-imports", ``},
		{http.MethodGet, "/v1/channel-imports/ping", ``},
		{http.MethodGet, "/v1/channel-imports/channels?q=mirror", ``},
		{http.MethodGet, "/v1/channel-imports/channels/4001", ``},
		{http.MethodGet, "/v1/channel-imports/channels/4001/messages", ``},
		{http.MethodPost, "/v1/channel-imports/7/message-ids", `{"source_ids":[2]}`},
	}
	for _, route := range routes {
		if rec := channelImportDo(srv, route.method, route.path, "", route.body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s without a token: status=%d, want 401", route.method, route.path, rec.Code)
		}
		if rec := channelImportDo(srv, route.method, route.path, "wrong-token", route.body); rec.Code != http.StatusUnauthorized {
			t.Fatalf("%s %s with an unknown token: status=%d, want 401", route.method, route.path, rec.Code)
		}
		rec := channelImportDo(srv, route.method, route.path, "prices-token", route.body)
		if rec.Code != http.StatusForbidden || decodeBody(t, rec)["permission"] != PermissionChannelsImport {
			t.Fatalf("%s %s with another scope: status=%d body=%s, want 403 naming channels.import", route.method, route.path, rec.Code, rec.Body.String())
		}
		for _, token := range []string{"import-token", "secret"} {
			if rec := channelImportDo(srv, route.method, route.path, token, route.body); rec.Code != http.StatusOK {
				t.Fatalf("%s %s with token %q: status=%d body=%s", route.method, route.path, token, rec.Code, rec.Body.String())
			}
		}
	}
}

// ping 一次回答三件事：令牌是谁、带什么权限、存储是否可用。
func TestAdminAPIChannelImportPing(t *testing.T) {
	svc := &captureChannelImportService{}
	srv := newChannelImportServer(svc)
	srv.version = "4cae8c31"

	rec := channelImportDo(srv, http.MethodGet, "/v1/channel-imports/ping", "import-token", "")
	body := decodeBody(t, rec)
	if rec.Code != http.StatusOK || body["ok"] != true || body["token_name"] != "cloner" || body["database"] != "ok" || body["version"] != "4cae8c31" {
		t.Fatalf("ping status=%d body=%v", rec.Code, body)
	}
	if perms := body["permissions"].([]any); len(perms) != 1 || perms[0] != PermissionChannelsImport {
		t.Fatalf("ping permissions = %v, want channels.import", perms)
	}
	if serverTime, _ := body["server_time"].(float64); serverTime < 1_700_000_000 {
		t.Fatalf("ping server_time = %v, want a unix timestamp", body["server_time"])
	}
	// master token：没有名字，权限是通配符。
	body = decodeBody(t, channelImportDo(srv, http.MethodGet, "/v1/channel-imports/ping", "secret", ""))
	if perms := body["permissions"].([]any); body["token_name"] != "" || len(perms) != 1 || perms[0] != PermissionAll {
		t.Fatalf("master ping body = %v, want an unnamed token carrying *", body)
	}
	// 存储不可用：503，字段不变，克隆程序的界面能指出是哪一环。
	svc.pingErr = errors.New("connection refused")
	rec = channelImportDo(srv, http.MethodGet, "/v1/channel-imports/ping", "import-token", "")
	body = decodeBody(t, rec)
	if rec.Code != http.StatusServiceUnavailable || body["ok"] != false || body["database"] != "unavailable" || body["token_name"] != "cloner" {
		t.Fatalf("ping with a dead store: status=%d body=%v", rec.Code, body)
	}
	// 没有接入频道导入：501。
	if rec := channelImportDo(newChannelImportServer(&fakeService{}), http.MethodGet, "/v1/channel-imports/ping", "import-token", ""); rec.Code != http.StatusNotImplemented {
		t.Fatalf("unconfigured ping status=%d, want 501", rec.Code)
	}
}

func TestAdminAPIChannelImportReadRoutes(t *testing.T) {
	svc := &captureChannelImportService{}
	srv := newChannelImportServer(svc)
	get := func(path string) (int, map[string]any) {
		rec := channelImportDo(srv, http.MethodGet, path, "import-token", "")
		return rec.Code, decodeBody(t, rec)
	}

	// 任务列表与过滤。
	code, body := get("/v1/channel-imports?state=mirroring&source_channel_id=1234567890&limit=20&offset=40")
	want := domain.ChannelImportFilter{State: domain.ChannelImportMirroring, SourceChannelID: 1234567890, Limit: 20, Offset: 40}
	if code != http.StatusOK || svc.filters[0] != want {
		t.Fatalf("list status=%d filter=%+v, want %+v", code, svc.filters[0], want)
	}
	item := body["imports"].([]any)[0].(map[string]any)
	if item["import_id"] != float64(7) || item["state"] != "mirroring" || item["max_source_id"] != float64(9) || item["channel"].(map[string]any)["username"] != "mirror" {
		t.Fatalf("list item = %v", item)
	}
	for _, bad := range []string{"?limit=-1", "?offset=x", "?source_channel_id=0", "?source_channel_id=abc"} {
		if code, _ := get("/v1/channel-imports" + bad); code != http.StatusBadRequest {
			t.Fatalf("list %s status=%d, want 400", bad, code)
		}
	}

	// 单个频道。
	code, body = get("/v1/channel-imports/channels/4001")
	if code != http.StatusOK || body["channel_id"] != float64(4001) || body["pts"] != float64(5300) || body["top_message_id"] != float64(5120) ||
		body["last_message_date"] != float64(1794001234) || body["message_count"] != float64(4987) || body["created_date"] != float64(1420070400) ||
		body["participants_count"] != float64(128) || body["pinned_message_id"] != float64(3) || body["has_photo"] != true || body["importable"] != false {
		t.Fatalf("channel status=%d body=%v", code, body)
	}
	if owner := body["owner"].(map[string]any); owner["user_id"] != float64(10) || owner["first_name"] != "Olga" || owner["username"] != "olga" || owner["found"] != true {
		t.Fatalf("channel owner = %v", owner)
	}
	if imp := body["import"].(map[string]any); imp["import_id"] != float64(7) || imp["state"] != "mirroring" || imp["source_channel_id"] != float64(1234567890) {
		t.Fatalf("channel import = %v", imp)
	}
	if code, _ := get("/v1/channel-imports/channels/abc"); code != http.StatusBadRequest {
		t.Fatalf("non-numeric channel id status=%d, want 400", code)
	}

	// 按名字查：同名的都返回；列表里没有 message_count；没有任务的频道 import 为 null。
	code, body = get("/v1/channel-imports/channels?q=Mirror&include_deleted=true&limit=5&offset=10")
	if code != http.StatusOK || svc.searches[0] != (channelimport.ChannelSearch{Query: "Mirror", IncludeDeleted: true, Limit: 5, Offset: 10}) {
		t.Fatalf("search status=%d search=%+v", code, svc.searches[0])
	}
	channels := body["channels"].([]any)
	if len(channels) != 2 {
		t.Fatalf("search returned %d channels, want both namesakes", len(channels))
	}
	second := channels[1].(map[string]any)
	if _, has := second["message_count"]; has || second["import"] != nil || second["importable"] != true || second["owner"].(map[string]any)["found"] != false {
		t.Fatalf("second search result = %v, want an importable channel without an import, a count or a known owner", second)
	}
	for _, bad := range []string{"", "?q=", "?q=%40", "?q=x&include_deleted=maybe", "?q=x&limit=ten"} {
		if code, _ := get("/v1/channel-imports/channels" + bad); code != http.StatusBadRequest {
			t.Fatalf("search %q status=%d, want 400", bad, code)
		}
	}

	// 消息分页：多取一条判断有没有下一页。
	code, body = get("/v1/channel-imports/channels/4001/messages?before_id=50&limit=2")
	if code != http.StatusOK || svc.messagePages[0] != [3]int{4001, 50, 3} {
		t.Fatalf("messages status=%d page=%v, want one extra row requested", code, svc.messagePages[0])
	}
	messages := body["messages"].([]any)
	if len(messages) != 2 || body["next_before_id"] != float64(9) {
		t.Fatalf("messages = %d next=%v, want 2 messages and next_before_id 9", len(messages), body["next_before_id"])
	}
	if first := messages[0].(map[string]any); first["source_id"] != nil || first["text"] != "local" {
		t.Fatalf("local message = %v, want a null source_id", first)
	}
	if mirrored := messages[1].(map[string]any); mirrored["source_id"] != float64(9) || mirrored["views"] != float64(12) || mirrored["grouped_id"] != "13000000000000001" {
		t.Fatalf("mirrored message = %v, want its source id and the album id as a string", mirrored)
	}
	// 最后一页：next_before_id 为 0；limit 缺省或超限时取上限。
	code, body = get("/v1/channel-imports/channels/4001/messages?limit=5000")
	if code != http.StatusOK || body["next_before_id"] != float64(0) || len(body["messages"].([]any)) != 3 || svc.messagePages[1][2] != domain.MaxChannelImportPage+1 {
		t.Fatalf("last page status=%d next=%v page=%v", code, body["next_before_id"], svc.messagePages[1])
	}

	// 消息 id 换算。
	rec := channelImportDo(srv, http.MethodPost, "/v1/channel-imports/7/message-ids", "import-token", `{"source_ids":[2,41,99]}`)
	body = decodeBody(t, rec)
	if rec.Code != http.StatusOK || len(svc.resolved[0]) != 3 || body["local_ids"].(map[string]any)["41"] != float64(42) {
		t.Fatalf("resolve status=%d body=%v", rec.Code, body)
	}
	if unknown := body["unknown_ids"].([]any); len(unknown) != 1 || unknown[0] != float64(99) {
		t.Fatalf("resolve unknown_ids = %v, want [99]", unknown)
	}

	// 频道不存在：404 与专门的错误码。
	missing := newChannelImportServer(&captureChannelImportService{err: channelimport.ErrChannelNotFound})
	for _, path := range []string{"/v1/channel-imports/channels/9", "/v1/channel-imports/channels/9/messages"} {
		rec := channelImportDo(missing, http.MethodGet, path, "import-token", "")
		if rec.Code != http.StatusNotFound || decodeBody(t, rec)["code"] != codeChannelImportChannelNotFound {
			t.Fatalf("%s status=%d body=%s, want 404 channel_not_found", path, rec.Code, rec.Body.String())
		}
	}
}

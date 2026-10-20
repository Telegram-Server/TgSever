package main

import (
	"context"
	"encoding/json"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/updatecdn"
)

// Desktop client updates. The panel only forwards: the update catalog lives in
// the telesrv process (admin API, 2599), so the admin container needs neither
// the update volume nor the update root public key.

const (
	// clientUpdateUploadSlack 与 adminapi 一致：包上限之外的少量余量。
	clientUpdateUploadSlack = 1 << 20
	// clientUpdateUploadTimeout 是一次上传转发的总时限，略长于 adminapi 自己的 30
	// 分钟读取时限，让超时由离暂存更近的一端报告。
	clientUpdateUploadTimeout = 35 * time.Minute
)

// clientUpdateUploadClient 转发上传。不用 http.DefaultClient：那里没有时限，而这里
// 的时限由请求的 context 给出。
var clientUpdateUploadClient = &http.Client{}

func (s *server) handleClientUpdatesAPI(w http.ResponseWriter, r *http.Request) {
	s.proxyAdminJSONNoStore(w, r, "/v1/client-updates", 1<<20)
}

// handleClientUpdateUploadAPI streams the browser's request body (the raw
// package, application/octet-stream) straight to the admin API: it is never
// read into memory or re-encoded. Query: reason (required), allow_downgrade,
// command_id (optional; a dry-run id is generated). The upload is the
// preflight, so it is always a dry-run command.
func (s *server) handleClientUpdateUploadAPI(w http.ResponseWriter, r *http.Request) {
	defer r.Body.Close()
	if mediaType, _, err := mime.ParseMediaType(r.Header.Get("Content-Type")); err != nil || mediaType != "application/octet-stream" {
		writeAPIError(w, http.StatusUnsupportedMediaType, "upload the package as application/octet-stream")
		return
	}
	if r.ContentLength > updatecdn.MaxDesktopPackageSize {
		writeJSON(w, http.StatusRequestEntityTooLarge, map[string]any{"error": "package exceeds the v2 package limit", "code": admin.CodeUpdatePackageTooLarge})
		return
	}
	query := r.URL.Query()
	reason := strings.TrimSpace(query.Get("reason"))
	if reason == "" {
		writeAPIError(w, http.StatusBadRequest, "reason is required")
		return
	}
	allowDowngrade, _ := strconv.ParseBool(query.Get("allow_downgrade"))
	meta := s.commandMetaFromAPI(r, query.Get("command_id"), reason, false, "client-update")
	upstreamQuery := url.Values{
		"command_id":      {meta.CommandID},
		"actor":           {meta.Actor},
		"reason":          {meta.Reason},
		"allow_downgrade": {strconv.FormatBool(allowDowngrade)},
	}

	ctx, cancel := context.WithTimeout(r.Context(), clientUpdateUploadTimeout)
	defer cancel()
	_ = http.NewResponseController(w).SetReadDeadline(time.Now().Add(clientUpdateUploadTimeout))
	body := http.MaxBytesReader(w, r.Body, updatecdn.MaxDesktopPackageSize+clientUpdateUploadSlack)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.cfg.AdminAPIURL+"/v1/client-updates/uploads?"+upstreamQuery.Encode(), body)
	if err != nil {
		writeCommandResultAPI(w, admin.CommandResult{}, err)
		return
	}
	// 保留浏览器声明的长度（未知时为 -1，按分块转发）。
	req.ContentLength = r.ContentLength
	if r.ContentLength == 0 {
		req.Body = http.NoBody
	}
	req.Header.Set("Content-Type", "application/octet-stream")
	req.Header.Set("Authorization", "Bearer "+s.cfg.AdminAPIToken)
	resp, err := clientUpdateUploadClient.Do(req)
	if err != nil {
		writeCommandResultAPI(w, admin.CommandResult{}, err)
		return
	}
	defer resp.Body.Close()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	var result admin.CommandResult
	if err := json.Unmarshal(raw, &result); err != nil || (result.Status == "" && result.Error == "") {
		// 不是命令结果（例如 adminapi 的 403/415/501 是 {"error": ...}）：原样转述。
		var plain map[string]any
		if json.Unmarshal(raw, &plain) != nil {
			plain = map[string]any{"error": strings.TrimSpace(string(raw))}
		}
		writeJSON(w, upstreamStatus(resp.StatusCode), plain)
		return
	}
	writeJSON(w, upstreamStatus(resp.StatusCode), result)
}

// upstreamStatus keeps the admin API's status, except that its auth failures
// are the panel's configuration problem, not the operator's session.
func upstreamStatus(status int) int {
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		return http.StatusBadGateway
	}
	return status
}

type clientUpdatePublishAPIRequest struct {
	CommandID      string `json:"command_id"`
	Reason         string `json:"reason"`
	Confirm        bool   `json:"confirm"`
	StagingID      string `json:"staging_id"`
	AllowDowngrade bool   `json:"allow_downgrade"`
}

func (s *server) handleClientUpdatePublishAPI(w http.ResponseWriter, r *http.Request) {
	var body clientUpdatePublishAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.PublishClientUpdateRequest{
		CommandMeta:    s.commandMetaFromAPI(r, body.CommandID, strings.TrimSpace(body.Reason), body.Confirm, "client-update-publish"),
		StagingID:      strings.TrimSpace(body.StagingID),
		AllowDowngrade: body.AllowDowngrade,
	}
	result, err := s.callAdminAPI(r.Context(), "/v1/client-updates/publish", req)
	writeCommandResultAPI(w, result, err)
}

type clientUpdateEntryAPIRequest struct {
	CommandID string `json:"command_id"`
	Reason    string `json:"reason"`
	Confirm   bool   `json:"confirm"`
	Platform  string `json:"platform"`
	Channel   string `json:"channel"`
}

// handleClientUpdateDisableAPI 停用一个桌面条目：立即停止下发，条目仍留在清单里。
func (s *server) handleClientUpdateDisableAPI(w http.ResponseWriter, r *http.Request) {
	s.proxyClientUpdateEntry(w, r, "client-update-disable", "/v1/client-updates/disable")
}

// handleClientUpdateEnableAPI 重新启用一个已停用的条目；理由必填（命令框架统一要求）。
func (s *server) handleClientUpdateEnableAPI(w http.ResponseWriter, r *http.Request) {
	s.proxyClientUpdateEntry(w, r, "client-update-enable", "/v1/client-updates/enable")
}

func (s *server) proxyClientUpdateEntry(w http.ResponseWriter, r *http.Request, prefix, apiPath string) {
	var body clientUpdateEntryAPIRequest
	if !decodeAction(w, r, &body) {
		return
	}
	req := admin.ClientUpdateEntryRequest{
		CommandMeta: s.commandMetaFromAPI(r, body.CommandID, strings.TrimSpace(body.Reason), body.Confirm, prefix),
		Platform:    strings.TrimSpace(body.Platform),
		Channel:     strings.TrimSpace(body.Channel),
	}
	result, err := s.callAdminAPI(r.Context(), apiPath, req)
	writeCommandResultAPI(w, result, err)
}

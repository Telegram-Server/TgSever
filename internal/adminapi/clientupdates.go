package adminapi

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/admin"
	"telesrv/internal/updatecdn"
)

// Desktop client updates. The status read is never an error for a disabled or
// external update service: the mode field says which, and the panel explains it.

var _ clientUpdatesService = (*admin.Service)(nil)

const (
	// clientUpdateUploadDeadline 是一次上传最长的读取时间，clientUpdateUploadIdle 是
	// 两次读到数据之间允许的最长间隔：慢速链路上 256 MiB 也能传完，停住的上传则
	// 很快被断开，不会一直占着一个写了一半的暂存文件。
	clientUpdateUploadDeadline = 30 * time.Minute
	clientUpdateUploadIdle     = 2 * time.Minute
)

func (s *Server) handleClientUpdates(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(clientUpdatesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "client updates are not configured")
		return
	}
	status, err := svc.ClientUpdateStatus(r.Context())
	if err != nil {
		writeError(w, http.StatusInternalServerError, err.Error())
		return
	}
	writeJSON(w, http.StatusOK, status)
}

// handleUploadClientUpdate takes the raw package bytes as the request body
// (application/octet-stream, never multipart) and the command metadata as
// query parameters: command_id, actor, reason, allow_downgrade. The body is
// handed to the service as a stream, so a 256 MiB package is never held in
// memory; it is cut off past the package limit, and on a deadline of its own.
func (s *Server) handleUploadClientUpdate(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(clientUpdatesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "client updates are not configured")
		return
	}
	defer r.Body.Close()
	limited, err := updatecdn.PackageUploadBody(w, r)
	switch {
	case errors.Is(err, updatecdn.ErrUploadContentType):
		writeError(w, http.StatusUnsupportedMediaType, err.Error())
		return
	case err != nil:
		writeCodedError(w, http.StatusRequestEntityTooLarge, admin.CodeUpdatePackageTooLarge, err.Error())
		return
	}
	query := r.URL.Query()
	allowDowngrade := false
	if raw := strings.TrimSpace(query.Get("allow_downgrade")); raw != "" {
		parsed, err := strconv.ParseBool(raw)
		if err != nil {
			writeError(w, http.StatusBadRequest, "allow_downgrade must be a boolean")
			return
		}
		allowDowngrade = parsed
	}
	req := admin.UploadClientUpdateRequest{
		CommandMeta: admin.CommandMeta{
			CommandID: query.Get("command_id"),
			Actor:     query.Get("actor"),
			Reason:    query.Get("reason"),
			DryRun:    true,
		},
		AllowDowngrade: allowDowngrade,
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)

	body := newUploadDeadlineReader(w, limited)
	result, err := svc.UploadClientUpdate(r.Context(), req, body)
	body.done()
	if status, ok := uploadFailureStatus[admin.ErrorCode(err)]; ok {
		writeJSON(w, status, failedResult(result, err))
		return
	}
	writeCommandResult(w, result, err)
}

// uploadFailureStatus 是暂存失败的 HTTP 状态；其余失败照常是 400。服务器存储失败是
// 500，不让运营者以为是自己的包或网络有问题。
var uploadFailureStatus = map[string]int{
	admin.CodeUpdatePackageTooLarge: http.StatusRequestEntityTooLarge,
	admin.CodeUpdateStagingFull:     http.StatusTooManyRequests,
	admin.CodeUpdateStagingStorage:  http.StatusInternalServerError,
}

func (s *Server) handlePublishClientUpdate(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.svc.(clientUpdatesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "client updates are not configured")
		return
	}
	var req admin.PublishClientUpdateRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := svc.PublishClientUpdate(r.Context(), req)
	writeCommandResult(w, result, err)
}

// handleDisableClientUpdate and handleEnableClientUpdate take an
// admin.ClientUpdateEntryRequest (platform, channel and the command
// metadata) as JSON.
func (s *Server) handleDisableClientUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleClientUpdateEntry(w, r, clientUpdatesService.DisableClientUpdate)
}

func (s *Server) handleEnableClientUpdate(w http.ResponseWriter, r *http.Request) {
	s.handleClientUpdateEntry(w, r, clientUpdatesService.EnableClientUpdate)
}

func (s *Server) handleClientUpdateEntry(w http.ResponseWriter, r *http.Request, call func(clientUpdatesService, context.Context, admin.ClientUpdateEntryRequest) (admin.CommandResult, error)) {
	svc, ok := s.svc.(clientUpdatesService)
	if !ok {
		writeError(w, http.StatusNotImplemented, "client updates are not configured")
		return
	}
	var req admin.ClientUpdateEntryRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	s.applyVerificationPrincipal(r, &req.CommandMeta)
	result, err := call(svc, r.Context(), req)
	writeCommandResult(w, result, err)
}

func failedResult(result admin.CommandResult, err error) admin.CommandResult {
	if result.CommandID == "" {
		return admin.CommandResult{Status: "failed", Message: "command failed", Error: err.Error(), Code: admin.ErrorCode(err)}
	}
	return result
}

// uploadDeadlineReader 给上传请求体设读取时限：总时限 clientUpdateUploadDeadline，
// 每次读之前把时限推到 clientUpdateUploadIdle 之后（不超过总时限）。连接不支持
// 设置时限（例如测试中的 ResponseRecorder）时照常读取。
type uploadDeadlineReader struct {
	r        io.Reader
	rc       *http.ResponseController
	deadline time.Time
	ok       bool
}

func newUploadDeadlineReader(w http.ResponseWriter, r io.Reader) *uploadDeadlineReader {
	rc := http.NewResponseController(w)
	deadline := time.Now().Add(clientUpdateUploadDeadline)
	return &uploadDeadlineReader{r: r, rc: rc, deadline: deadline, ok: rc.SetReadDeadline(deadline) == nil}
}

func (u *uploadDeadlineReader) Read(p []byte) (int, error) {
	if u.ok {
		next := time.Now().Add(clientUpdateUploadIdle)
		if next.After(u.deadline) {
			next = u.deadline
		}
		_ = u.rc.SetReadDeadline(next)
	}
	return u.r.Read(p)
}

// done clears the read deadline once the service is finished with the body.
func (u *uploadDeadlineReader) done() {
	if u.ok {
		_ = u.rc.SetReadDeadline(time.Time{})
	}
}

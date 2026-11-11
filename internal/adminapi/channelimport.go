package adminapi

import (
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/admin"
	"telesrv/internal/app/channelimport"
	"telesrv/internal/domain"
)

// 频道导入：一个在仓库之外的克隆程序把外部来源（官方 Telegram 的频道）的历史和后续更新
// 写进本地广播频道。整组路由只认 channels.import 一个权限，给克隆程序配一个只带它的
// scoped token 即可；接口说明见 docs/channel-import.md。
//
// 这些路由不是 admin command：没有 command_id、不写命令审计。每个写入按自身的键幂等——
// 任务按来源频道、媒体按来源文件标识、消息按来源消息 id——所以重试任何一个请求都是安全的。

// channelImportProvider 由 *admin.Service 实现。
type channelImportProvider interface {
	ChannelImport() (admin.ChannelImportService, bool)
}

var _ channelImportProvider = (*admin.Service)(nil)

const (
	// maxChannelImportBatchBytes 是一批消息的请求体上限：500 条带长正文和实体的消息远小于它。
	maxChannelImportBatchBytes = 16 << 20
	// maxChannelImportDocumentBytes 是一个导入文档的上限，与来源单个文件的上限（4 GiB）一致。
	maxChannelImportDocumentBytes = 4 << 30
)

// 错误码：克隆程序据此区分“改请求再试”“换个顺序再试”和“任务已经结束”。
const (
	codeChannelImportNotFound = "channel_import_not_found"
	// codeChannelImportChannelNotFound：只读查询指定的本地频道不存在。
	codeChannelImportChannelNotFound = "channel_not_found"
	codeChannelImportState           = "channel_import_state"
	codeChannelImportOrder           = "channel_import_order"
	codeChannelImportTarget          = "channel_import_target"
	codeChannelImportExists          = "channel_import_exists"
	codeChannelImportOwner           = "channel_import_owner"
	codeChannelImportMedia           = "channel_import_media"
	codeChannelImportUsername        = "channel_import_username"
	codeChannelImportInvalid         = "channel_import_invalid"
)

func (s *Server) channelImports(w http.ResponseWriter) (admin.ChannelImportService, bool) {
	provider, ok := s.svc.(channelImportProvider)
	if !ok {
		writeError(w, http.StatusNotImplemented, "channel import is not configured")
		return nil, false
	}
	svc, ok := provider.ChannelImport()
	if !ok {
		writeError(w, http.StatusNotImplemented, "channel import is not configured")
		return nil, false
	}
	return svc, true
}

func channelImportID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id <= 0 {
		writeError(w, http.StatusBadRequest, "import id must be a positive integer")
		return 0, false
	}
	return id, true
}

// writeChannelImportError 把业务错误映射成状态码和错误码：404 任务不存在；409 与任务或频道
// 的当前状态冲突（重试同一个请求没有用）；400 请求本身不合法；其余是服务器错误。
func writeChannelImportError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, domain.ErrChannelImportNotFound):
		writeCodedError(w, http.StatusNotFound, codeChannelImportNotFound, err.Error())
	case errors.Is(err, channelimport.ErrChannelNotFound):
		writeCodedError(w, http.StatusNotFound, codeChannelImportChannelNotFound, err.Error())
	case errors.Is(err, domain.ErrChannelImportState):
		writeCodedError(w, http.StatusConflict, codeChannelImportState, err.Error())
	case errors.Is(err, domain.ErrChannelImportOrder):
		writeCodedError(w, http.StatusConflict, codeChannelImportOrder, err.Error())
	case errors.Is(err, domain.ErrChannelImportTarget):
		writeCodedError(w, http.StatusConflict, codeChannelImportTarget, err.Error())
	case errors.Is(err, domain.ErrChannelImportExists):
		writeCodedError(w, http.StatusConflict, codeChannelImportExists, err.Error())
	case errors.Is(err, domain.ErrUsernameOccupied), errors.Is(err, domain.ErrUsernameInvalid):
		writeCodedError(w, http.StatusConflict, codeChannelImportUsername, err.Error())
	case errors.Is(err, channelimport.ErrOwnerInvalid):
		writeCodedError(w, http.StatusBadRequest, codeChannelImportOwner, err.Error())
	case errors.Is(err, channelimport.ErrMediaInvalid), errors.Is(err, domain.ErrPhotoInvalid), errors.Is(err, domain.ErrDocumentInvalid):
		writeCodedError(w, http.StatusBadRequest, codeChannelImportMedia, err.Error())
	case errors.Is(err, domain.ErrChannelInvalid), errors.Is(err, domain.ErrChannelTitleInvalid):
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, err.Error())
	default:
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeCodedError(w, http.StatusRequestEntityTooLarge, codeChannelImportMedia, "upload is too large")
			return
		}
		writeError(w, http.StatusInternalServerError, err.Error())
	}
}

type channelImportStatusResponse struct {
	ImportID        int64  `json:"import_id"`
	ChannelID       int64  `json:"channel_id"`
	State           string `json:"state"`
	SourceChannelID int64  `json:"source_channel_id"`
	SourceUsername  string `json:"source_username"`
	SenderUserID    int64  `json:"sender_user_id"`
	// MaxSourceID 是已写入的最大来源消息 id：断点续传从它之后继续。
	MaxSourceID int `json:"max_source_id"`
	CreatedAt   int `json:"created_at"`
	FinalizedAt int `json:"finalized_at"`
	// Existing 只在创建时有意义：true 表示来源频道已有在用的任务，返回的是它。
	Existing bool                      `json:"existing,omitempty"`
	Channel  channelImportChannelView  `json:"channel"`
	Stats    channelImportStatsPayload `json:"stats"`
}

type channelImportChannelView struct {
	Title        string `json:"title"`
	About        string `json:"about"`
	Username     string `json:"username"`
	OwnerUserID  int64  `json:"owner_user_id"`
	Date         int    `json:"date"`
	TopMessageID int    `json:"top_message_id"`
}

type channelImportStatsPayload struct {
	Messages int `json:"messages"`
	Media    int `json:"media"`
}

func channelImportStatus(status channelimport.Status) channelImportStatusResponse {
	return channelImportStatusResponse{
		ImportID:        status.Import.ID,
		ChannelID:       status.Import.ChannelID,
		State:           string(status.Import.State),
		SourceChannelID: status.Import.SourceChannelID,
		SourceUsername:  status.Import.SourceUsername,
		SenderUserID:    status.Import.SenderUserID,
		MaxSourceID:     status.Import.MaxSourceID,
		CreatedAt:       status.Import.CreatedAt,
		FinalizedAt:     status.Import.FinalizedAt,
		Channel: channelImportChannelView{
			Title:        status.Channel.Title,
			About:        status.Channel.About,
			Username:     status.Channel.Username,
			OwnerUserID:  status.Channel.CreatorUserID,
			Date:         status.Channel.Date,
			TopMessageID: status.Channel.TopMessageID,
		},
		Stats: channelImportStatsPayload{Messages: status.Stats.Messages, Media: status.Stats.Media},
	}
}

type createChannelImportRequest struct {
	Source struct {
		ChannelID int64  `json:"channel_id"`
		Username  string `json:"username"`
	} `json:"source"`
	// TargetChannelID 非零时导入到这个已有的空广播频道，此时 channel 里只看 created_date、
	// create_title 和 create_message_missing。
	TargetChannelID int64 `json:"target_channel_id"`
	Channel         struct {
		OwnerUserID          int64  `json:"owner_user_id"`
		Title                string `json:"title"`
		About                string `json:"about"`
		CreatedDate          int    `json:"created_date"`
		CreateTitle          string `json:"create_title"`
		CreateMessageMissing bool   `json:"create_message_missing"`
	} `json:"channel"`
	SenderUserID int64 `json:"sender_user_id"`
}

func (s *Server) handleCreateChannelImport(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	var req createChannelImportRequest
	if !decodeJSON(w, r, &req) {
		return
	}
	res, err := svc.Create(r.Context(), channelimport.CreateRequest{
		SourceChannelID:      req.Source.ChannelID,
		SourceUsername:       req.Source.Username,
		TargetChannelID:      req.TargetChannelID,
		OwnerUserID:          req.Channel.OwnerUserID,
		Title:                req.Channel.Title,
		About:                req.Channel.About,
		CreatedDate:          req.Channel.CreatedDate,
		CreateTitle:          req.Channel.CreateTitle,
		CreateMessageMissing: req.Channel.CreateMessageMissing,
		SenderUserID:         req.SenderUserID,
	})
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	out := channelImportStatus(res.Status)
	out.Existing = res.Existing
	status := http.StatusCreated
	if res.Existing {
		status = http.StatusOK
	}
	writeJSON(w, status, out)
}

func (s *Server) handleChannelImport(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	status, err := svc.Get(r.Context(), id)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelImportStatus(status))
}

// handleUploadChannelImportMedia 的请求体就是文件字节（application/octet-stream，不是
// multipart），元数据在查询参数里：source_key、kind（photo|document）、date、mime_type、
// attributes（文档属性的 JSON 数组）。文档流式落盘，不进内存；同一个 source_key 第二次
// 上传不读请求体，直接返回第一次的结果。
func (s *Server) handleUploadChannelImportMedia(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	defer r.Body.Close()
	query := r.URL.Query()
	upload := channelimport.MediaUpload{
		SourceKey: query.Get("source_key"),
		Kind:      domain.ChannelImportMediaKind(query.Get("kind")),
		MimeType:  query.Get("mime_type"),
	}
	if raw := strings.TrimSpace(query.Get("date")); raw != "" {
		date, err := strconv.Atoi(raw)
		if err != nil || date < 0 {
			writeCodedError(w, http.StatusBadRequest, codeChannelImportMedia, "date must be a unix timestamp")
			return
		}
		upload.Date = date
	}
	if raw := strings.TrimSpace(query.Get("attributes")); raw != "" {
		if err := json.Unmarshal([]byte(raw), &upload.Attributes); err != nil {
			writeCodedError(w, http.StatusBadRequest, codeChannelImportMedia, "attributes must be a JSON array: "+err.Error())
			return
		}
	}
	limit := int64(maxChannelImportDocumentBytes)
	if upload.Kind == domain.ChannelImportMediaPhoto {
		limit = channelimport.MaxPhotoBytes
	}
	body := newUploadDeadlineReader(w, http.MaxBytesReader(w, r.Body, limit))
	upload.Body = body
	res, err := svc.PutMedia(r.Context(), id, upload)
	body.done()
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	status := http.StatusCreated
	if res.Existing {
		status = http.StatusOK
	}
	writeJSON(w, status, map[string]any{
		"source_key": res.SourceKey,
		"kind":       string(res.Media.Kind),
		"media_id":   strconv.FormatInt(res.Media.MediaID, 10),
		"existing":   res.Existing,
	})
}

// decodeChannelImportJSON 与 decodeJSON 相同，但请求体上限按一批消息的大小放宽。
func decodeChannelImportJSON(w http.ResponseWriter, r *http.Request, dst any) bool {
	defer r.Body.Close()
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, maxChannelImportBatchBytes))
	dec.DisallowUnknownFields()
	if err := dec.Decode(dst); err != nil {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, "invalid json: "+err.Error())
		return false
	}
	return true
}

func (s *Server) handleImportChannelMessages(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	var req struct {
		Messages []channelimport.Message `json:"messages"`
	}
	if !decodeChannelImportJSON(w, r, &req) {
		return
	}
	res, err := svc.ImportMessages(r.Context(), id, req.Messages)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	// JSON 对象的键只能是字符串：来源 id → 本地 id 以 {"来源 id": 本地 id} 给出。
	localIDs := make(map[string]int, len(res.LocalIDs))
	for source, local := range res.LocalIDs {
		localIDs[strconv.Itoa(source)] = local
	}
	skipped := res.Skipped
	if skipped == nil {
		skipped = []channelimport.Skipped{}
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"written":       res.Written,
		"edited":        res.Edited,
		"unchanged":     res.Unchanged,
		"skipped":       skipped,
		"local_ids":     localIDs,
		"max_source_id": res.Import.MaxSourceID,
		"state":         string(res.Import.State),
	})
}

func (s *Server) handleDeleteChannelImportMessages(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	var req struct {
		IDs []int `json:"ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	actor := principalName(r.Context())
	if actor == "" {
		actor = "admin-api"
	}
	res, err := svc.DeleteMessages(r.Context(), id, req.IDs, actor)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	deleted, unknown := res.Deleted, res.Unknown
	if deleted == nil {
		deleted = []int{}
	}
	if unknown == nil {
		unknown = []int{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"deleted_local_ids": deleted, "unknown_ids": unknown})
}

func (s *Server) handleFinalizeChannelImport(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	var req struct {
		Username string `json:"username"`
		// ReserveThrough 是来源已经用到的最大消息 id（包括末尾被删掉的）：之后本地分配的
		// 消息 id 都大于它。
		ReserveThrough int `json:"reserve_through"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	status, err := svc.Finalize(r.Context(), id, channelimport.FinalizeRequest{Username: req.Username, ReserveThrough: req.ReserveThrough})
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelImportStatus(status))
}

func (s *Server) handleSetChannelImportMeta(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	// 指针区分“不改”和“改成空”：简介可以清空，标题不能为空。
	var req struct {
		Title *string `json:"title"`
		About *string `json:"about"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	if req.Title == nil && req.About == nil {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, "title or about is required")
		return
	}
	status, err := svc.SetMeta(r.Context(), id, channelimport.Meta{Title: req.Title, About: req.About})
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelImportStatus(status))
}

// handleSetChannelImportPhoto 的请求体是新头像的图片字节。
func (s *Server) handleSetChannelImportPhoto(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	defer r.Body.Close()
	data, err := io.ReadAll(http.MaxBytesReader(w, r.Body, channelimport.MaxPhotoBytes))
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	if len(data) == 0 {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportMedia, "photo body is empty")
		return
	}
	status, err := svc.SetMeta(r.Context(), id, channelimport.Meta{Photo: data})
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelImportStatus(status))
}

func (s *Server) handleDetachChannelImport(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	status, err := svc.Detach(r.Context(), id)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelImportStatus(status))
}

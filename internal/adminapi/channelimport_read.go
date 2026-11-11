package adminapi

import (
	"net/http"
	"sort"
	"strconv"
	"strings"
	"time"

	"telesrv/internal/app/channelimport"
	"telesrv/internal/domain"
)

// 频道导入的只读路由：连通检查、任务列表、本地频道的查询与搜索、频道消息、消息 id 换算。
// 与写入路由一样只认 channels.import。

// channelImportQueryInt 读一个非负整数查询参数；缺省为 0。
func channelImportQueryInt(w http.ResponseWriter, r *http.Request, name string) (int, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return 0, true
	}
	value, err := strconv.Atoi(raw)
	if err != nil || value < 0 {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, name+" must be a non-negative integer")
		return 0, false
	}
	return value, true
}

func channelImportQueryBool(w http.ResponseWriter, r *http.Request, name string) (bool, bool) {
	raw := strings.TrimSpace(r.URL.Query().Get(name))
	if raw == "" {
		return false, true
	}
	value, err := strconv.ParseBool(raw)
	if err != nil {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, name+" must be a boolean")
		return false, false
	}
	return value, true
}

// handleChannelImportPing 让克隆程序一次确认三件事：到服务器的网络通、令牌有效且带
// channels.import（否则在鉴权层就是 401 / 403）、服务器的存储可用。存储不可用时仍然回答
// 200 之外的 503，并带同样的字段，便于界面显示是哪一环出了问题。
func (s *Server) handleChannelImportPing(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	caller, _ := r.Context().Value(principalKey{}).(principal)
	permissions := make([]string, 0, len(caller.permissions.names)+1)
	if caller.permissions.all {
		permissions = append(permissions, PermissionAll)
	}
	for name := range caller.permissions.names {
		permissions = append(permissions, name)
	}
	sort.Strings(permissions)
	out := map[string]any{
		"ok":          true,
		"token_name":  caller.name,
		"permissions": permissions,
		"server_time": time.Now().Unix(),
		"version":     s.version,
		"database":    "ok",
	}
	status := http.StatusOK
	if err := svc.Ping(r.Context()); err != nil {
		s.log.Warn("channel import ping: store unavailable")
		out["ok"] = false
		out["database"] = "unavailable"
		status = http.StatusServiceUnavailable
	}
	writeJSON(w, status, out)
}

type channelImportListItem struct {
	ImportID        int64                    `json:"import_id"`
	ChannelID       int64                    `json:"channel_id"`
	State           string                   `json:"state"`
	SourceChannelID int64                    `json:"source_channel_id"`
	SourceUsername  string                   `json:"source_username"`
	SenderUserID    int64                    `json:"sender_user_id"`
	MaxSourceID     int                      `json:"max_source_id"`
	CreatedAt       int                      `json:"created_at"`
	FinalizedAt     int                      `json:"finalized_at"`
	Channel         channelImportChannelView `json:"channel"`
}

// handleListChannelImports 列出任务，新的在前。查询参数：state、source_channel_id、limit、offset。
func (s *Server) handleListChannelImports(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	filter := domain.ChannelImportFilter{State: domain.ChannelImportState(strings.TrimSpace(r.URL.Query().Get("state")))}
	if raw := strings.TrimSpace(r.URL.Query().Get("source_channel_id")); raw != "" {
		id, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || id <= 0 {
			writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, "source_channel_id must be a positive integer")
			return
		}
		filter.SourceChannelID = id
	}
	if filter.Limit, ok = channelImportQueryInt(w, r, "limit"); !ok {
		return
	}
	if filter.Offset, ok = channelImportQueryInt(w, r, "offset"); !ok {
		return
	}
	listed, err := svc.List(r.Context(), filter)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	items := make([]channelImportListItem, 0, len(listed))
	for _, item := range listed {
		items = append(items, channelImportListItem{
			ImportID:        item.Import.ID,
			ChannelID:       item.Import.ChannelID,
			State:           string(item.Import.State),
			SourceChannelID: item.Import.SourceChannelID,
			SourceUsername:  item.Import.SourceUsername,
			SenderUserID:    item.Import.SenderUserID,
			MaxSourceID:     item.Import.MaxSourceID,
			CreatedAt:       item.Import.CreatedAt,
			FinalizedAt:     item.Import.FinalizedAt,
			Channel: channelImportChannelView{
				Title:        item.Channel.Title,
				About:        item.Channel.About,
				Username:     item.Channel.Username,
				OwnerUserID:  item.Channel.CreatorUserID,
				Date:         item.Channel.Date,
				TopMessageID: item.Channel.TopMessageID,
			},
		})
	}
	writeJSON(w, http.StatusOK, map[string]any{"imports": items})
}

type channelImportOwnerView struct {
	UserID    int64  `json:"user_id"`
	FirstName string `json:"first_name"`
	LastName  string `json:"last_name"`
	Username  string `json:"username"`
	// Found 为 false 表示频道主的账号已不存在，其余字段为空。
	Found bool `json:"found"`
}

type channelImportBriefView struct {
	ImportID        int64  `json:"import_id"`
	State           string `json:"state"`
	SourceChannelID int64  `json:"source_channel_id"`
	SourceUsername  string `json:"source_username"`
	MaxSourceID     int    `json:"max_source_id"`
}

type channelImportChannelInfoView struct {
	ChannelID         int64                  `json:"channel_id"`
	Title             string                 `json:"title"`
	About             string                 `json:"about"`
	Username          string                 `json:"username"`
	Broadcast         bool                   `json:"broadcast"`
	Megagroup         bool                   `json:"megagroup"`
	Deleted           bool                   `json:"deleted"`
	Owner             channelImportOwnerView `json:"owner"`
	CreatedDate       int                    `json:"created_date"`
	ParticipantsCount int                    `json:"participants_count"`
	Pts               int                    `json:"pts"`
	TopMessageID      int                    `json:"top_message_id"`
	// LastMessageDate 是最后一条未删除消息的日期（来源日期）；没有消息时为 0。
	LastMessageDate int `json:"last_message_date"`
	// MessageCount 只在查单个频道时给出。
	MessageCount    *int `json:"message_count,omitempty"`
	PinnedMessageID int  `json:"pinned_message_id"`
	HasPhoto        bool `json:"has_photo"`
	// Importable 表示这个频道可以作为新导入任务的目标。
	Importable bool                    `json:"importable"`
	Import     *channelImportBriefView `json:"import"`
}

func channelImportChannelInfo(info channelimport.ChannelInfo) channelImportChannelInfoView {
	ch := info.Channel
	view := channelImportChannelInfoView{
		ChannelID:         ch.ID,
		Title:             ch.Title,
		About:             ch.About,
		Username:          ch.Username,
		Broadcast:         ch.Broadcast,
		Megagroup:         ch.Megagroup,
		Deleted:           ch.Deleted,
		Owner:             channelImportOwnerView{UserID: ch.CreatorUserID},
		CreatedDate:       ch.Date,
		ParticipantsCount: ch.ParticipantsCount,
		Pts:               ch.Pts,
		TopMessageID:      info.LastMessageID,
		LastMessageDate:   info.LastMessageDate,
		PinnedMessageID:   ch.PinnedMessageID,
		HasPhoto:          ch.PhotoID != 0,
		Importable:        info.Importable(),
	}
	if info.Owner != nil {
		view.Owner.FirstName = info.Owner.FirstName
		view.Owner.LastName = info.Owner.LastName
		view.Owner.Username = info.Owner.Username
		view.Owner.Found = true
	}
	if info.MessageCount >= 0 {
		count := info.MessageCount
		view.MessageCount = &count
	}
	if imp := info.Import; imp != nil {
		view.Import = &channelImportBriefView{
			ImportID:        imp.ID,
			State:           string(imp.State),
			SourceChannelID: imp.SourceChannelID,
			SourceUsername:  imp.SourceUsername,
			MaxSourceID:     imp.MaxSourceID,
		}
	}
	return view
}

func channelImportChannelID(w http.ResponseWriter, r *http.Request) (int64, bool) {
	id, err := strconv.ParseInt(r.PathValue("channel_id"), 10, 64)
	if err != nil || id <= 0 {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, "channel id must be a positive integer")
		return 0, false
	}
	return id, true
}

// handleSearchChannelImportChannels 按名字查本地频道。查询参数：q（必填）、include_deleted、
// limit、offset。同名的频道全部返回，最近有消息的在前。
func (s *Server) handleSearchChannelImportChannels(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	search := channelimport.ChannelSearch{Query: r.URL.Query().Get("q")}
	if strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(search.Query), "@")) == "" {
		writeCodedError(w, http.StatusBadRequest, codeChannelImportInvalid, "q is required")
		return
	}
	if search.IncludeDeleted, ok = channelImportQueryBool(w, r, "include_deleted"); !ok {
		return
	}
	if search.Limit, ok = channelImportQueryInt(w, r, "limit"); !ok {
		return
	}
	if search.Offset, ok = channelImportQueryInt(w, r, "offset"); !ok {
		return
	}
	infos, err := svc.SearchChannels(r.Context(), search)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	channels := make([]channelImportChannelInfoView, 0, len(infos))
	for _, info := range infos {
		channels = append(channels, channelImportChannelInfo(info))
	}
	writeJSON(w, http.StatusOK, map[string]any{"channels": channels})
}

func (s *Server) handleChannelImportChannel(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportChannelID(w, r)
	if !ok {
		return
	}
	info, err := svc.Channel(r.Context(), id)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	writeJSON(w, http.StatusOK, channelImportChannelInfo(info))
}

// handleChannelImportChannelMessages 从新到旧返回频道里的消息。查询参数：before_id、limit。
// next_before_id 非零时还有更早的消息，把它作为下一页的 before_id。
func (s *Server) handleChannelImportChannelMessages(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportChannelID(w, r)
	if !ok {
		return
	}
	beforeID, ok := channelImportQueryInt(w, r, "before_id")
	if !ok {
		return
	}
	limit, ok := channelImportQueryInt(w, r, "limit")
	if !ok {
		return
	}
	if limit <= 0 || limit > domain.MaxChannelImportPage {
		limit = domain.MaxChannelImportPage
	}
	// 多取一条判断有没有下一页。
	messages, err := svc.ChannelMessages(r.Context(), id, beforeID, limit+1)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	next := 0
	if len(messages) > limit {
		messages = messages[:limit]
		next = messages[limit-1].ID
	}
	if messages == nil {
		messages = []channelimport.MessageView{}
	}
	writeJSON(w, http.StatusOK, map[string]any{"messages": messages, "next_before_id": next})
}

// handleResolveChannelImportMessageIDs 把来源消息 id 换成本地 id。用 POST 是因为一次可以
// 问上千个 id。
func (s *Server) handleResolveChannelImportMessageIDs(w http.ResponseWriter, r *http.Request) {
	svc, ok := s.channelImports(w)
	if !ok {
		return
	}
	id, ok := channelImportID(w, r)
	if !ok {
		return
	}
	var req struct {
		SourceIDs []int `json:"source_ids"`
	}
	if !decodeJSON(w, r, &req) {
		return
	}
	mapped, err := svc.ResolveMessageIDs(r.Context(), id, req.SourceIDs)
	if err != nil {
		writeChannelImportError(w, err)
		return
	}
	localIDs := make(map[string]int, len(mapped))
	unknown := []int{}
	for _, sourceID := range req.SourceIDs {
		if local, ok := mapped[sourceID]; ok {
			localIDs[strconv.Itoa(sourceID)] = local
		} else {
			unknown = append(unknown, sourceID)
		}
	}
	writeJSON(w, http.StatusOK, map[string]any{"local_ids": localIDs, "unknown_ids": unknown})
}

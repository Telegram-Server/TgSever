package channelimport

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"telesrv/internal/domain"
)

// 只读查询：克隆程序的界面用它们确认连通、找回自己的任务、查看和挑选本地频道。

// ErrChannelNotFound 查询的频道不存在。
var ErrChannelNotFound = errors.New("channel import: channel not found")

// Ping 确认存储后端可用。
func (s *Service) Ping(ctx context.Context) error {
	if s == nil || s.imports == nil {
		return fmt.Errorf("channel import dependencies are not configured")
	}
	return s.imports.PingChannelImportStore(ctx)
}

// ChannelInfo 是一个本地频道的概况。
type ChannelInfo struct {
	domain.ChannelImportChannelSummary
	// Owner 是频道主；账号已不存在时为 nil。
	Owner *domain.User
	// MessageCount 是未删除的消息数；只在查单个频道时计算，列表里为 -1。
	MessageCount int
}

func (s *Service) channelInfos(ctx context.Context, summaries []domain.ChannelImportChannelSummary) ([]ChannelInfo, error) {
	owners := map[int64]*domain.User{}
	out := make([]ChannelInfo, 0, len(summaries))
	for _, summary := range summaries {
		ownerID := summary.Channel.CreatorUserID
		owner, known := owners[ownerID]
		if !known && s.users != nil && ownerID != 0 {
			user, found, err := s.users.ByID(ctx, ownerID)
			if err != nil {
				return nil, err
			}
			if found {
				owner = &user
			}
			owners[ownerID] = owner
		}
		out = append(out, ChannelInfo{ChannelImportChannelSummary: summary, Owner: owner, MessageCount: -1})
	}
	return out, nil
}

// Channel 返回一个本地频道的概况；不要求它有导入任务。
func (s *Service) Channel(ctx context.Context, channelID int64) (ChannelInfo, error) {
	if channelID <= 0 {
		return ChannelInfo{}, ErrChannelNotFound
	}
	summaries, err := s.imports.ChannelImportChannelSummaries(ctx, []int64{channelID})
	if err != nil {
		return ChannelInfo{}, err
	}
	if len(summaries) == 0 {
		return ChannelInfo{}, ErrChannelNotFound
	}
	infos, err := s.channelInfos(ctx, summaries)
	if err != nil {
		return ChannelInfo{}, err
	}
	count, err := s.imports.CountChannelImportChannelMessages(ctx, channelID)
	if err != nil {
		return ChannelInfo{}, err
	}
	infos[0].MessageCount = count
	return infos[0], nil
}

// ChannelSearch 按名字查频道。Query 以 @ 开头时只按用户名精确匹配；否则按标题做不区分
// 大小写的包含匹配，同时也接受与它完全相同的用户名。
type ChannelSearch struct {
	Query          string
	IncludeDeleted bool
	Limit          int
	Offset         int
}

// SearchChannels 返回所有匹配的频道（同名的都在），最近有消息的在前。
func (s *Service) SearchChannels(ctx context.Context, search ChannelSearch) ([]ChannelInfo, error) {
	text := strings.TrimSpace(search.Query)
	query := domain.ChannelImportChannelQuery{IncludeDeleted: search.IncludeDeleted, Limit: search.Limit, Offset: search.Offset}
	if strings.HasPrefix(text, "@") {
		query.Username = strings.TrimPrefix(text, "@")
	} else {
		query.Title = text
		if !strings.ContainsAny(text, " \t") {
			query.Username = text
		}
	}
	if query.Title == "" && query.Username == "" {
		return nil, domain.ErrChannelInvalid
	}
	summaries, err := s.imports.SearchChannelImportChannels(ctx, query)
	if err != nil {
		return nil, err
	}
	return s.channelInfos(ctx, summaries)
}

// ListedImport 是任务列表里的一项。Channel 在频道已不存在时为零值。
type ListedImport struct {
	Import  domain.ChannelImport
	Channel domain.Channel
}

// List 列出导入任务，新的在前。克隆程序重启或换机器之后据此找回自己的任务。
func (s *Service) List(ctx context.Context, filter domain.ChannelImportFilter) ([]ListedImport, error) {
	switch filter.State {
	case "", domain.ChannelImportImporting, domain.ChannelImportMirroring, domain.ChannelImportDetached:
	default:
		return nil, domain.ErrChannelInvalid
	}
	imports, err := s.imports.ListChannelImports(ctx, filter)
	if err != nil {
		return nil, err
	}
	ids := make([]int64, 0, len(imports))
	for _, imp := range imports {
		ids = append(ids, imp.ChannelID)
	}
	summaries, err := s.imports.ChannelImportChannelSummaries(ctx, ids)
	if err != nil {
		return nil, err
	}
	channels := make(map[int64]domain.Channel, len(summaries))
	for _, summary := range summaries {
		channels[summary.Channel.ID] = summary.Channel
	}
	out := make([]ListedImport, 0, len(imports))
	for _, imp := range imports {
		out = append(out, ListedImport{Import: imp, Channel: channels[imp.ChannelID]})
	}
	return out, nil
}

// ResolveMessageIDs 把来源消息 id 换成本地 id；没有导入过的 id 不出现在结果里。克隆程序
// 改写正文里指向来源的链接时用它。
func (s *Service) ResolveMessageIDs(ctx context.Context, importID int64, sourceIDs []int) (map[int]int, error) {
	if len(sourceIDs) == 0 || len(sourceIDs) > domain.MaxDeleteMessageIDs {
		return nil, domain.ErrChannelInvalid
	}
	return s.imports.ResolveChannelImportMessageIDs(ctx, importID, sourceIDs)
}

// ChannelMessages 从新到旧返回频道里的消息；beforeID 非零时只返回 id 小于它的。
func (s *Service) ChannelMessages(ctx context.Context, channelID int64, beforeID, limit int) ([]MessageView, error) {
	if channelID <= 0 {
		return nil, ErrChannelNotFound
	}
	summaries, err := s.imports.ChannelImportChannelSummaries(ctx, []int64{channelID})
	if err != nil {
		return nil, err
	}
	if len(summaries) == 0 {
		return nil, ErrChannelNotFound
	}
	stored, err := s.imports.ListChannelImportChannelMessages(ctx, channelID, beforeID, limit)
	if err != nil {
		return nil, err
	}
	out := make([]MessageView, 0, len(stored))
	for _, msg := range stored {
		out = append(out, viewMessage(msg))
	}
	return out, nil
}

// MessageView 是频道里一条消息的线上表示，字段与写入时的 Message 对齐。所有消息 id 都是
// 本地 id；SourceID 是它在来源的 id，本地发的帖子为 nil。
type MessageView struct {
	ID           int          `json:"id"`
	SourceID     *int         `json:"source_id"`
	Date         int          `json:"date"`
	EditDate     int          `json:"edit_date,omitempty"`
	Text         string       `json:"text,omitempty"`
	Entities     []Entity     `json:"entities,omitempty"`
	Media        *MediaView   `json:"media,omitempty"`
	GroupedID    Int64        `json:"grouped_id,omitempty"`
	ReplyToMsgID int          `json:"reply_to_msg_id,omitempty"`
	Forward      *ForwardView `json:"fwd_from,omitempty"`
	PostAuthor   string       `json:"post_author,omitempty"`
	Views        int          `json:"views"`
	Forwards     int          `json:"forwards"`
	Silent       bool         `json:"silent,omitempty"`
	NoForwards   bool         `json:"noforwards,omitempty"`
	Pinned       bool         `json:"pinned,omitempty"`
	Buttons      [][]Button   `json:"buttons,omitempty"`
	Action       *ActionView  `json:"action,omitempty"`
}

// MediaView 是消息媒体的摘要；不含文件内容。
type MediaView struct {
	// Kind: photo、document，或其它媒体的领域名（poll、geo、web_page……）。
	Kind     string  `json:"kind"`
	MediaID  Int64   `json:"media_id,omitempty"`
	MimeType string  `json:"mime_type,omitempty"`
	Size     int64   `json:"size,omitempty"`
	FileName string  `json:"file_name,omitempty"`
	Width    int     `json:"width,omitempty"`
	Height   int     `json:"height,omitempty"`
	Duration float64 `json:"duration,omitempty"`
	Video    bool    `json:"video,omitempty"`
	Voice    bool    `json:"voice,omitempty"`
	Round    bool    `json:"round,omitempty"`
	Spoiler  bool    `json:"spoiler,omitempty"`
}

// ForwardView 是转发头。FromChannelID 是本地频道 id（转发来源是另一个已克隆的频道时）。
type ForwardView struct {
	FromName      string `json:"from_name,omitempty"`
	Date          int    `json:"date,omitempty"`
	FromChannelID int64  `json:"from_channel_id,omitempty"`
	ChannelPost   int    `json:"channel_post,omitempty"`
}

// ActionView 是服务消息的动作。Type 对写入时支持的六种用同样的名字，其余给出领域名。
type ActionView struct {
	Type         string `json:"type"`
	Title        string `json:"title,omitempty"`
	Duration     int    `json:"duration,omitempty"`
	ScheduleDate int    `json:"schedule_date,omitempty"`
}

// actionViewTypes 把领域动作类型映射回写入时的名字。
var actionViewTypes = map[domain.ChannelMessageActionType]string{
	domain.ChannelActionEditTitle:          "edit_title",
	domain.ChannelActionChatEditPhoto:      "edit_photo",
	domain.ChannelActionChatDeletePhoto:    "delete_photo",
	domain.ChannelActionPinMessage:         "pin_message",
	domain.ChannelActionGroupCall:          "group_call",
	domain.ChannelActionGroupCallScheduled: "group_call_scheduled",
}

func viewMessage(stored domain.ChannelImportStoredMessage) MessageView {
	msg := stored.Message
	view := MessageView{
		ID:         msg.ID,
		Date:       msg.Date,
		EditDate:   msg.EditDate,
		Text:       msg.Body,
		GroupedID:  Int64(msg.GroupedID),
		PostAuthor: msg.PostAuthor,
		Views:      msg.ViewsCount,
		Forwards:   msg.ForwardsCount,
		Silent:     msg.Silent,
		NoForwards: msg.NoForwards,
		Pinned:     msg.Pinned,
	}
	if stored.SourceID > 0 {
		sourceID := stored.SourceID
		view.SourceID = &sourceID
	}
	for _, e := range msg.Entities {
		view.Entities = append(view.Entities, Entity{
			Type: string(e.Type), Offset: e.Offset, Length: e.Length, URL: e.URL, Language: e.Language, Collapsed: e.Collapsed,
		})
	}
	if msg.ReplyTo != nil {
		view.ReplyToMsgID = msg.ReplyTo.MessageID
	}
	if f := msg.Forward; f != nil {
		forward := &ForwardView{FromName: f.FromName, Date: f.Date, ChannelPost: f.ChannelPost}
		if f.From.Type == domain.PeerTypeChannel {
			forward.FromChannelID = f.From.ID
		}
		view.Forward = forward
	}
	if msg.ReplyMarkup != nil {
		for _, row := range msg.ReplyMarkup.Inline {
			var buttons []Button
			for _, b := range row {
				buttons = append(buttons, Button{Text: b.Text, URL: b.URL})
			}
			if len(buttons) > 0 {
				view.Buttons = append(view.Buttons, buttons)
			}
		}
	}
	if a := msg.Action; a != nil {
		name, ok := actionViewTypes[a.Type]
		if !ok {
			name = string(a.Type)
		}
		view.Action = &ActionView{Type: name, Title: a.Title, Duration: a.CallDuration, ScheduleDate: a.CallScheduleDate}
	}
	view.Media = viewMedia(msg.Media)
	return view
}

func viewMedia(media *domain.MessageMedia) *MediaView {
	if media.IsZero() {
		return nil
	}
	view := &MediaView{Kind: string(media.Kind), Video: media.Video, Voice: media.Voice, Round: media.Round, Spoiler: media.Spoiler}
	switch {
	case media.Photo != nil:
		view.MediaID = Int64(media.Photo.ID)
		for _, size := range media.Photo.Sizes {
			if size.W*size.H >= view.Width*view.Height {
				view.Width, view.Height = size.W, size.H
				view.Size = int64(size.Size)
			}
		}
	case media.Document != nil:
		doc := media.Document
		view.MediaID = Int64(doc.ID)
		view.MimeType = doc.MimeType
		view.Size = doc.Size
		for _, attr := range doc.Attributes {
			switch attr.Kind {
			case domain.DocAttrFilename:
				view.FileName = attr.FileName
			case domain.DocAttrVideo:
				view.Width, view.Height, view.Duration = attr.W, attr.H, attr.Duration
			case domain.DocAttrAudio:
				view.Duration = float64(attr.AudioDuration)
			case domain.DocAttrImageSize:
				if view.Width == 0 {
					view.Width, view.Height = attr.W, attr.H
				}
			}
		}
	}
	return view
}

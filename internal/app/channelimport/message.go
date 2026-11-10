package channelimport

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"strconv"

	"telesrv/internal/domain"
)

// Message 是克隆程序提交的一条来源消息，也是 admin API 的线上格式。
type Message struct {
	// ID 是消息在来源频道里的 id。
	ID       int      `json:"id"`
	Date     int      `json:"date"`
	EditDate int      `json:"edit_date,omitempty"`
	Text     string   `json:"text,omitempty"`
	Entities []Entity `json:"entities,omitempty"`
	Media    *Media   `json:"media,omitempty"`
	// GroupedID 相册分组 id；来源的值超出 JSON 数字的精确范围，数字和字符串都接受。
	GroupedID    Int64      `json:"grouped_id,omitempty"`
	ReplyToMsgID int        `json:"reply_to_msg_id,omitempty"`
	Forward      *Forward   `json:"fwd_from,omitempty"`
	PostAuthor   string     `json:"post_author,omitempty"`
	Views        int        `json:"views,omitempty"`
	Forwards     int        `json:"forwards,omitempty"`
	Silent       bool       `json:"silent,omitempty"`
	NoForwards   bool       `json:"noforwards,omitempty"`
	Pinned       bool       `json:"pinned,omitempty"`
	Buttons      [][]Button `json:"buttons,omitempty"`
	Action       *Action    `json:"action,omitempty"`
}

// Entity 是一个文本实体，偏移和长度按 UTF-16 码元计（与 MTProto 一致）。
type Entity struct {
	Type      string `json:"type"`
	Offset    int    `json:"offset"`
	Length    int    `json:"length"`
	URL       string `json:"url,omitempty"`
	Language  string `json:"language,omitempty"`
	Collapsed bool   `json:"collapsed,omitempty"`
}

// Media 用 PutMedia 时的 SourceKey 引用一个已上传的文件。
type Media struct {
	SourceKey string `json:"source_key"`
	Spoiler   bool   `json:"spoiler,omitempty"`
}

// Forward 是转发来源。FromSourceChannelID 指向另一个已克隆的来源频道时，转发头指向它的本地
// 频道和帖子；否则只显示 FromName。
type Forward struct {
	FromName            string `json:"from_name,omitempty"`
	Date                int    `json:"date,omitempty"`
	FromSourceChannelID int64  `json:"from_source_channel_id,omitempty"`
	ChannelPost         int    `json:"channel_post,omitempty"`
}

// Button 是一个内联按钮；只导入 URL 按钮，其余类型依赖来源的机器人，在本地没有意义。
type Button struct {
	Text string `json:"text"`
	URL  string `json:"url,omitempty"`
}

// Action 是服务消息的动作。
type Action struct {
	// Type: edit_title、edit_photo、delete_photo、pin_message、group_call、group_call_scheduled。
	Type  string `json:"type"`
	Title string `json:"title,omitempty"`
	// PhotoSourceKey 是 edit_photo 的新头像（先经 PutMedia 上传为 photo）。
	PhotoSourceKey string `json:"photo_source_key,omitempty"`
	// Duration 是 group_call 的时长（秒）；ScheduleDate 是 group_call_scheduled 的开播时间。
	Duration     int `json:"duration,omitempty"`
	ScheduleDate int `json:"schedule_date,omitempty"`
}

// Int64 是一个在 JSON 里既可以写成数字也可以写成字符串的 int64。
type Int64 int64

func (v *Int64) UnmarshalJSON(data []byte) error {
	data = bytes.TrimSpace(data)
	if len(data) == 0 || string(data) == "null" {
		*v = 0
		return nil
	}
	if data[0] == '"' {
		var s string
		if err := json.Unmarshal(data, &s); err != nil {
			return err
		}
		if s == "" {
			*v = 0
			return nil
		}
		n, err := strconv.ParseInt(s, 10, 64)
		if err != nil {
			return fmt.Errorf("invalid int64 %q", s)
		}
		*v = Int64(n)
		return nil
	}
	n, err := strconv.ParseInt(string(data), 10, 64)
	if err != nil {
		return fmt.Errorf("invalid int64 %s", data)
	}
	*v = Int64(n)
	return nil
}

func (v Int64) MarshalJSON() ([]byte, error) {
	return []byte(strconv.Quote(strconv.FormatInt(int64(v), 10))), nil
}

// importableEntities 是原样导入的实体类型。自定义表情和按用户 id 的提及指向来源服务器上的
// 对象，在本地不存在：丢弃实体，文字本身保留。
var importableEntities = map[string]domain.MessageEntityType{
	"bold":        domain.MessageEntityBold,
	"italic":      domain.MessageEntityItalic,
	"underline":   domain.MessageEntityUnderline,
	"strike":      domain.MessageEntityStrike,
	"code":        domain.MessageEntityCode,
	"pre":         domain.MessageEntityPre,
	"text_url":    domain.MessageEntityTextURL,
	"spoiler":     domain.MessageEntitySpoiler,
	"blockquote":  domain.MessageEntityBlockquote,
	"mention":     domain.MessageEntityMention,
	"hashtag":     domain.MessageEntityHashtag,
	"cashtag":     domain.MessageEntityCashtag,
	"bot_command": domain.MessageEntityBotCommand,
	"url":         domain.MessageEntityURL,
	"email":       domain.MessageEntityEmail,
	"phone":       domain.MessageEntityPhone,
	"bank_card":   domain.MessageEntityBankCard,
}

func convertEntities(in []Entity) []domain.MessageEntity {
	out := make([]domain.MessageEntity, 0, len(in))
	for _, e := range in {
		kind, ok := importableEntities[e.Type]
		if !ok || e.Offset < 0 || e.Length <= 0 {
			continue
		}
		entity := domain.MessageEntity{Type: kind, Offset: e.Offset, Length: e.Length}
		switch kind {
		case domain.MessageEntityTextURL:
			if e.URL == "" {
				continue
			}
			entity.URL = e.URL
		case domain.MessageEntityPre:
			entity.Language = e.Language
		case domain.MessageEntityBlockquote:
			entity.Collapsed = e.Collapsed
		}
		out = append(out, entity)
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

func convertButtons(rows [][]Button) *domain.MessageReplyMarkup {
	var inline [][]domain.MarkupButton
	for _, row := range rows {
		var out []domain.MarkupButton
		for _, b := range row {
			if b.Text == "" || b.URL == "" {
				continue
			}
			out = append(out, domain.MarkupButton{Type: domain.MarkupButtonURL, Text: b.Text, URL: b.URL})
		}
		if len(out) > 0 {
			inline = append(inline, out)
		}
	}
	if len(inline) == 0 {
		return nil
	}
	return &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: inline}
}

// messageConverter 把一批线上格式的消息换成领域类型；同一批里重复引用的媒体和来源频道只查一次。
type messageConverter struct {
	s       *Service
	imp     domain.ChannelImport
	media   map[string]*domain.MessageMedia
	sources map[int64]*domain.ChannelImport
}

func newMessageConverter(s *Service, imp domain.ChannelImport) *messageConverter {
	return &messageConverter{s: s, imp: imp, media: map[string]*domain.MessageMedia{}, sources: map[int64]*domain.ChannelImport{}}
}

// convert 返回领域消息；reason 非空表示这条消息被跳过（不是错误，整批继续）。
func (c *messageConverter) convert(ctx context.Context, msg Message) (domain.ImportedChannelMessage, string, error) {
	if msg.Date <= 0 {
		return domain.ImportedChannelMessage{}, "", domain.ErrChannelInvalid
	}
	out := domain.ImportedChannelMessage{
		SourceID:        msg.ID,
		Date:            msg.Date,
		EditDate:        msg.EditDate,
		Body:            msg.Text,
		Entities:        convertEntities(msg.Entities),
		GroupedID:       int64(msg.GroupedID),
		ReplyToSourceID: msg.ReplyToMsgID,
		PostAuthor:      msg.PostAuthor,
		Views:           msg.Views,
		Forwards:        msg.Forwards,
		Silent:          msg.Silent,
		NoForwards:      msg.NoForwards,
		Pinned:          msg.Pinned,
		ReplyMarkup:     convertButtons(msg.Buttons),
	}
	if msg.Action != nil {
		action, reason, err := c.action(ctx, *msg.Action)
		if err != nil || reason != "" {
			return domain.ImportedChannelMessage{}, reason, err
		}
		// 服务消息没有正文、媒体和按钮。
		return domain.ImportedChannelMessage{
			SourceID:        msg.ID,
			Date:            msg.Date,
			ReplyToSourceID: msg.ReplyToMsgID,
			Action:          action,
		}, "", nil
	}
	if msg.Media != nil {
		media, reason, err := c.messageMedia(ctx, *msg.Media)
		if err != nil || reason != "" {
			return domain.ImportedChannelMessage{}, reason, err
		}
		out.Media = media
	}
	if out.Body == "" && out.Media == nil {
		return domain.ImportedChannelMessage{}, "no importable content", nil
	}
	if msg.Forward != nil {
		forward, err := c.forward(ctx, *msg.Forward)
		if err != nil {
			return domain.ImportedChannelMessage{}, "", err
		}
		out.Forward = forward
	}
	return out, "", nil
}

func (c *messageConverter) messageMedia(ctx context.Context, ref Media) (*domain.MessageMedia, string, error) {
	base, reason, err := c.lookupMedia(ctx, ref.SourceKey)
	if err != nil || reason != "" {
		return nil, reason, err
	}
	media := *base
	media.Spoiler = ref.Spoiler
	return &media, "", nil
}

func (c *messageConverter) lookupMedia(ctx context.Context, sourceKey string) (*domain.MessageMedia, string, error) {
	if cached, ok := c.media[sourceKey]; ok {
		return cached, "", nil
	}
	missing := fmt.Sprintf("media %q was not uploaded", sourceKey)
	stored, found, err := c.s.imports.GetChannelImportMedia(ctx, c.imp.ID, sourceKey)
	if err != nil {
		return nil, "", err
	}
	if !found || c.s.files == nil {
		return nil, missing, nil
	}
	var media *domain.MessageMedia
	switch stored.Kind {
	case domain.ChannelImportMediaPhoto:
		photo, ok, err := c.s.files.GetPhoto(ctx, stored.MediaID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			return nil, missing, nil
		}
		media = &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &photo}
	case domain.ChannelImportMediaDocument:
		doc, ok, err := c.s.files.GetDocument(ctx, stored.MediaID)
		if err != nil {
			return nil, "", err
		}
		if !ok {
			return nil, missing, nil
		}
		media = documentMedia(doc)
	default:
		return nil, missing, nil
	}
	c.media[sourceKey] = media
	return media, "", nil
}

// documentMedia 由文档构造消息媒体，并从属性推导 Video/Round/Voice 标志
// （与发送路径的 messageMediaFromDocument 同一规则）。
func documentMedia(doc domain.Document) *domain.MessageMedia {
	media := &domain.MessageMedia{Kind: domain.MessageMediaKindDocument, Document: &doc}
	if doc.IsSticker() {
		media.Nopremium = true
	}
	for _, attr := range doc.Attributes {
		switch attr.Kind {
		case domain.DocAttrVideo:
			media.Video = true
			if attr.RoundMessage {
				media.Round = true
			}
		case domain.DocAttrAudio:
			if attr.Voice {
				media.Voice = true
			}
		}
	}
	return media
}

func (c *messageConverter) forward(ctx context.Context, in Forward) (*domain.MessageForward, error) {
	out := &domain.MessageForward{FromName: in.FromName, Date: in.Date}
	if in.FromSourceChannelID == 0 {
		if out.FromName == "" {
			return nil, nil
		}
		return out, nil
	}
	source, known := c.sources[in.FromSourceChannelID]
	if !known {
		found, ok, err := c.s.imports.FindChannelImportBySource(ctx, in.FromSourceChannelID)
		if err != nil {
			return nil, err
		}
		if ok {
			source = &found
		}
		c.sources[in.FromSourceChannelID] = source
	}
	if source == nil {
		// 转发来源没有克隆到本地：只显示名字。
		if out.FromName == "" {
			return nil, nil
		}
		return out, nil
	}
	out.FromName = ""
	out.From = domain.Peer{Type: domain.PeerTypeChannel, ID: source.ChannelID}
	if in.ChannelPost > 0 {
		out.ChannelPost = in.ChannelPost
		mapped, err := c.s.imports.ResolveChannelImportMessageIDs(ctx, source.ID, []int{in.ChannelPost})
		if err != nil {
			return nil, err
		}
		if local, ok := mapped[in.ChannelPost]; ok {
			out.ChannelPost = local
		}
	}
	return out, nil
}

// action 把服务消息的动作换成领域类型。导入的服务消息只是历史：改标题、换头像、置顶都不会
// 作用到频道上。不认识的类型被跳过，在本地留成空号。
func (c *messageConverter) action(ctx context.Context, in Action) (*domain.ChannelMessageAction, string, error) {
	switch in.Type {
	case "edit_title":
		if in.Title == "" {
			return nil, "edit_title action without a title", nil
		}
		return &domain.ChannelMessageAction{Type: domain.ChannelActionEditTitle, Title: in.Title}, "", nil
	case "edit_photo":
		media, reason, err := c.lookupMedia(ctx, in.PhotoSourceKey)
		if err != nil || reason != "" {
			return nil, reason, err
		}
		if media.Photo == nil {
			return nil, fmt.Sprintf("media %q is not a photo", in.PhotoSourceKey), nil
		}
		photo := *media.Photo
		return &domain.ChannelMessageAction{Type: domain.ChannelActionChatEditPhoto, Photo: &photo}, "", nil
	case "delete_photo":
		return &domain.ChannelMessageAction{Type: domain.ChannelActionChatDeletePhoto}, "", nil
	case "pin_message":
		return &domain.ChannelMessageAction{Type: domain.ChannelActionPinMessage}, "", nil
	case "group_call":
		return &domain.ChannelMessageAction{Type: domain.ChannelActionGroupCall, CallDuration: in.Duration}, "", nil
	case "group_call_scheduled":
		if in.ScheduleDate <= 0 {
			return nil, "group_call_scheduled action without a schedule date", nil
		}
		return &domain.ChannelMessageAction{Type: domain.ChannelActionGroupCallScheduled, CallScheduleDate: in.ScheduleDate}, "", nil
	default:
		return nil, fmt.Sprintf("unsupported action %q", in.Type), nil
	}
}

package botapi

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

// InputMediaItem 是一项官方 InputMedia（sendMediaGroup 的元素、editMessageMedia 的
// media）。它是匿名 struct 的别名：rpc 侧声明同构的别名，两边就是同一类型，网关接口
// 不必为此新增 domain 类型；契约测试会在两边漂移时编译失败。
type InputMediaItem = struct {
	Kind          string // photo / video / audio / document / animation
	File          domain.BotAPIFileInput
	SecondaryFile domain.BotAPIFileInput
	Caption       string
	Entities      []domain.MessageEntity
	Spoiler       bool
}

// SendExtraGateway 是 sendMediaGroup / sendPoll / sendDice / sendChatAction 的网关。
// 可选接口：网关不实现时这些方法回 METHOD_NOT_FOUND。
type SendExtraGateway interface {
	// BotAPISendMediaGroup 以一个相册发出 2–10 项媒体，按发送顺序返回各条消息。
	BotAPISendMediaGroupWithOptions(ctx context.Context, botID, chatID int64, items []InputMediaItem, silent, protectContent bool, replyToMessageID int, options domain.BotAPIMessageOptions) ([]domain.Message, error)
	BotAPISendDiceWithOptions(ctx context.Context, botID, chatID int64, emoji string, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, options domain.BotAPIMessageOptions) (domain.Message, error)
	// BotAPISendPoll 发送投票。poll 只用到定义字段（题干、答案文本、匿名/多选/quiz、
	// 关闭时间与 Closed=is_closed）；quiz 的正确答案下标与解释单独传，它们是机密。
	BotAPISendPollWithOptions(ctx context.Context, botID, chatID int64, poll domain.MessagePoll, correctOptionIDs []int, explanation string, explanationEntities []domain.MessageEntity, replyMarkup *domain.MessageReplyMarkup, silent, protectContent bool, replyToMessageID int, options domain.BotAPIMessageOptions) (domain.Message, error)
	// BotAPISendChatAction 广播一次输入状态；action 已由网关校验为 botAPIChatActions 之一。
	BotAPISendChatAction(ctx context.Context, botID, chatID int64, action string, messageThreadID int) (bool, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"sendDice":       (*handler).sendDice,
		"sendChatAction": (*handler).sendChatAction,
		"sendPoll":       (*handler).sendPoll,
		"sendMediaGroup": (*handler).sendMediaGroup,
	})
	registerMessageProjection(projectDice)
	registerMessageProjection(projectMediaGroupID)
	// 子串匹配：POLL_OPTION_INVALID 必须排在 OPTION_INVALID 之前。
	registerErrorMarkers("EMOTICON_INVALID", "ACTION_INVALID",
		"POLL_QUESTION_INVALID", "POLL_ANSWERS_INVALID", "POLL_ANSWER_INVALID", "POLL_OPTION_INVALID",
		"OPTIONS_TOO_MUCH", "OPTION_INVALID", "QUIZ_CORRECT_ANSWERS_INVALID", "POLL_TYPE_INVALID",
		"POLL_CLOSE_INVALID", "MEDIA_EMPTY", "PEER_ID_INVALID", "MEDIA_GROUP_INVALID", "PHOTO_INVALID",
		"MEDIA_CAPTION_TOO_LONG", "LIMIT_INVALID")
}

// projectMediaGroupID 输出官方 Message.media_group_id（相册分组 id，字符串）。
func projectMediaGroupID(msg domain.Message, out map[string]any) {
	if msg.GroupedID != 0 {
		out["media_group_id"] = strconv.FormatInt(msg.GroupedID, 10)
	}
}

// inputMediaFromAPI 解析一个 InputMedia 对象；attach://name 从 multipart 文件里取。
func inputMediaFromAPI(raw json.RawMessage, files map[string]uploadedFile) (InputMediaItem, error) {
	var input domain.BotAPIEphemeralEditInput
	if err := parseEphemeralEditMedia(string(raw), &input, files); err != nil {
		return InputMediaItem{}, err
	}
	if input.MediaKind == "live_photo" && input.File.RemoteURL != "" {
		return InputMediaItem{}, errors.New("FILE_ID_INVALID")
	}
	return InputMediaItem{Kind: input.MediaKind, File: input.File, SecondaryFile: input.SecondaryFile, Caption: input.Fields.Message, Entities: input.Fields.Entities, Spoiler: input.File.HasSpoiler}, nil
}

// mediaGroupFromAPI 解析 sendMediaGroup 的 media：2–10 项；照片与视频可以混排，
// 音频、文档只能各自成组。
func mediaGroupFromAPI(raw string, files map[string]uploadedFile) ([]InputMediaItem, error) {
	var list []json.RawMessage
	if json.Unmarshal([]byte(raw), &list) != nil || len(list) < 2 || len(list) > 10 {
		return nil, errors.New("MEDIA_GROUP_INVALID")
	}
	items := make([]InputMediaItem, 0, len(list))
	for _, entry := range list {
		item, err := inputMediaFromAPI(entry, files)
		if err != nil {
			return nil, err
		}
		items = append(items, item)
	}
	group := func(kind string) string {
		if kind == "photo" || kind == "video" {
			return "visual"
		}
		return kind
	}
	for _, item := range items {
		switch item.Kind {
		case "photo", "video", "audio", "document":
		default:
			return nil, errors.New("MEDIA_GROUP_INVALID")
		}
		if group(item.Kind) != group(items[0].Kind) {
			return nil, errors.New("MEDIA_GROUP_INVALID")
		}
	}
	return items, nil
}

func (h *handler) sendMediaGroup(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.sendExtraGateway(w)
	if !ok {
		return
	}
	values, files, err := requestValuesWithFiles(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	common, desc := h.parseSendCommon(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	items, err := mediaGroupFromAPI(values["media"], files)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	msgs, err := g.BotAPISendMediaGroupWithOptions(r.Context(), botID, common.chatID, items, common.silent, common.protect, common.replyTo, common.options)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeBotMessages(w, r, botID, msgs, true)
}

// botAPIChatActions 是官方 sendChatAction 接受的 action。
var botAPIChatActions = map[string]bool{
	"typing": true, "upload_photo": true, "record_video": true, "upload_video": true,
	"record_voice": true, "upload_voice": true, "upload_document": true, "choose_sticker": true,
	"find_location": true, "record_video_note": true, "upload_video_note": true,
}

// projectDice 输出官方 Message.dice。
func projectDice(msg domain.Message, out map[string]any) {
	if msg.Media == nil || msg.Media.Kind != domain.MessageMediaKindDice || msg.Media.Dice == nil {
		return
	}
	out["dice"] = map[string]any{"emoji": msg.Media.Dice.Emoticon, "value": msg.Media.Dice.Value}
}

// sendCommon 是 send* 方法共有的参数：目标会话、键盘与投递选项。
type sendCommon struct {
	chatID  int64
	markup  *domain.MessageReplyMarkup
	silent  bool
	protect bool
	replyTo int
	options domain.BotAPIMessageOptions
}

// parseSendCommon 失败时返回的是 API 错误描述。
func (h *handler) parseSendCommon(ctx context.Context, botID int64, values map[string]string) (sendCommon, string) {
	chatID, err := h.chatIDParam(ctx, botID, values["chat_id"])
	if err != nil {
		return sendCommon{}, apiErrorDescription(err)
	}
	out := sendCommon{chatID: chatID, silent: apiBool(values["disable_notification"]), protect: apiBool(values["protect_content"])}
	if raw := strings.TrimSpace(values["reply_markup"]); raw != "" {
		if out.markup, err = replyMarkupFromAPI(json.RawMessage(raw)); err != nil {
			return sendCommon{}, err.Error()
		}
	}
	out.options, err = botAPIMessageOptions(values)
	if err != nil {
		return sendCommon{}, err.Error()
	}
	if out.options.ReplyParameters != nil {
		out.replyTo = out.options.ReplyParameters.MessageID
	}
	return out, ""
}

func (h *handler) sendExtraGateway(w http.ResponseWriter) (SendExtraGateway, bool) {
	g, ok := h.gateway.(SendExtraGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
	}
	return g, ok
}

// writeBotMessages 投影机器人发出/编辑的消息；many 为 true 时输出数组。
func (h *handler) writeBotMessages(w http.ResponseWriter, r *http.Request, botID int64, msgs []domain.Message, many bool) {
	users := []domain.User(nil)
	if self, err := h.gateway.BotAPISelf(r.Context(), botID); err == nil && self.ID != 0 {
		users = append(users, self)
	}
	if !many {
		writeAPIOK(w, apiMessage(msgs[0], users))
		return
	}
	out := make([]map[string]any, 0, len(msgs))
	for _, msg := range msgs {
		out = append(out, apiMessage(msg, users))
	}
	writeAPIOK(w, out)
}

func (h *handler) sendChatAction(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.sendExtraGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	chatID, err := h.chatIDParam(r.Context(), botID, values["chat_id"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	action := strings.TrimSpace(values["action"])
	if !botAPIChatActions[action] {
		writeAPIError(w, http.StatusBadRequest, "ACTION_INVALID")
		return
	}
	result, err := g.BotAPISendChatAction(r.Context(), botID, chatID, action, apiInt(values["message_thread_id"], 0))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, result)
}

// pollOptionsFromAPI 解析 options：元素可以是字符串或 InputPollOption 对象。
func pollOptionsFromAPI(raw string) ([]domain.MessagePollAnswer, error) {
	var items []json.RawMessage
	if json.Unmarshal([]byte(raw), &items) != nil {
		return nil, errors.New("POLL_ANSWERS_INVALID")
	}
	answers := make([]domain.MessagePollAnswer, 0, len(items))
	for _, item := range items {
		var option struct {
			Text         string          `json:"text"`
			ParseMode    string          `json:"text_parse_mode"`
			TextEntities json.RawMessage `json:"text_entities"`
		}
		if json.Unmarshal(item, &option.Text) != nil && json.Unmarshal(item, &option) != nil {
			return nil, errors.New("POLL_ANSWERS_INVALID")
		}
		if strings.TrimSpace(option.Text) == "" {
			return nil, errors.New("POLL_ANSWER_INVALID")
		}
		text, entities, err := botAPIFormattedTextRaw(option.Text, option.ParseMode, string(option.TextEntities), domain.MaxPollAnswerTextLength, true)
		if err != nil {
			return nil, err
		}
		answers = append(answers, domain.MessagePollAnswer{Text: text, Entities: entities})
	}
	return answers, nil
}

func (h *handler) sendPoll(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.sendExtraGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	common, desc := h.parseSendCommon(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	poll, correct, explanation, explanationEntities, err := pollFromAPI(values)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, err.Error())
		return
	}
	msg, err := g.BotAPISendPollWithOptions(r.Context(), botID, common.chatID, poll, correct, explanation, explanationEntities, common.markup, common.silent, common.protect, common.replyTo, common.options)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeBotMessages(w, r, botID, []domain.Message{msg}, false)
}

func pollFromAPI(values map[string]string) (domain.MessagePoll, []int, string, []domain.MessageEntity, error) {
	var poll domain.MessagePoll
	if strings.TrimSpace(values["question"]) == "" {
		return poll, nil, "", nil, errors.New("POLL_QUESTION_INVALID")
	}
	var err error
	poll.Question, poll.QuestionEntities, err = botAPIFormattedTextRaw(values["question"], values["question_parse_mode"], values["question_entities"], domain.MaxPollQuestionLength, true)
	if err != nil {
		return poll, nil, "", nil, err
	}
	if poll.Answers, err = pollOptionsFromAPI(values["options"]); err != nil {
		return poll, nil, "", nil, err
	}
	switch values["type"] {
	case "", "regular":
	case "quiz":
		poll.Quiz = true
	default:
		return poll, nil, "", nil, errors.New("POLL_TYPE_INVALID")
	}
	// is_anonymous 缺省为 true。
	if raw, set := values["is_anonymous"]; set {
		poll.PublicVoters = !apiBool(raw)
	}
	poll.MultipleChoice = apiBool(values["allows_multiple_answers"])
	poll.Closed = apiBool(values["is_closed"])
	var correct []int
	if raw := strings.TrimSpace(values["correct_option_id"]); raw != "" {
		id, err := strconv.Atoi(raw)
		if err != nil || id < 0 || id >= len(poll.Answers) || !poll.Quiz {
			return poll, nil, "", nil, errors.New("QUIZ_CORRECT_ANSWERS_INVALID")
		}
		correct = []int{id}
	} else if poll.Quiz {
		return poll, nil, "", nil, errors.New("QUIZ_CORRECT_ANSWERS_INVALID")
	}
	explanation, explanationEntities, err := botAPIFormattedTextRaw(values["explanation"], values["explanation_parse_mode"], values["explanation_entities"], domain.MaxPollSolutionLength, false)
	if err != nil {
		return poll, nil, "", nil, err
	}
	poll.ClosePeriod = apiInt(values["open_period"], 0)
	poll.CloseDate = apiInt(values["close_date"], 0)
	if poll.ClosePeriod < 0 || poll.CloseDate < 0 || (poll.ClosePeriod > 0 && poll.CloseDate > 0) {
		return poll, nil, "", nil, errors.New("POLL_CLOSE_INVALID")
	}
	return poll, correct, explanation, explanationEntities, nil
}

func (h *handler) sendDice(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.sendExtraGateway(w)
	if !ok {
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	common, desc := h.parseSendCommon(r.Context(), botID, values)
	if desc != "" {
		writeAPIError(w, http.StatusBadRequest, desc)
		return
	}
	emoji := strings.TrimSpace(values["emoji"])
	if emoji == "" {
		emoji = "🎲"
	}
	msg, err := g.BotAPISendDiceWithOptions(r.Context(), botID, common.chatID, emoji, common.markup, common.silent, common.protect, common.replyTo, common.options)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	h.writeBotMessages(w, r, botID, []domain.Message{msg}, false)
}

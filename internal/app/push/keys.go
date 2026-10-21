package push

import (
	"strings"

	"telesrv/internal/domain"
)

// Key 是推送的内部分类键，同时是 iOS 语言包 "PUSH_"+Key 的后缀。
// 私聊 MESSAGE_*，超级群 CHAT_MESSAGE_*，频道 CHANNEL_MESSAGE_*。
type Key string

const (
	KeyMessageText   Key = "MESSAGE_TEXT"
	KeyMessageNoText Key = "MESSAGE_NOTEXT"
	// KeyEncryptedMessage 渲染外层通用文案（"You have a new message"），不含任何消息内容。
	KeyEncryptedMessage Key = "ENCRYPTED_MESSAGE"
)

// 消息类型后缀：与会话前缀拼成完整键（MESSAGE_PHOTO、CHAT_MESSAGE_PHOTO、CHANNEL_MESSAGE_PHOTO）。
const (
	suffixText    = "TEXT"
	suffixNoText  = "NOTEXT"
	suffixPhoto   = "PHOTO"
	suffixVideo   = "VIDEO"
	suffixDoc     = "DOC"
	suffixSticker = "STICKER"
	suffixAudio   = "AUDIO"
	suffixRound   = "ROUND"
	suffixGif     = "GIF"
	suffixContact = "CONTACT"
	suffixGeo     = "GEO"
	suffixGeoLive = "GEOLIVE"
	suffixPoll    = "POLL"
	suffixQuiz    = "QUIZ"
)

// captionEmoji 是可带文字说明的媒体在 *_TEXT 正文前加的类型标记（与 iOS 语言包里该类型文案的 emoji 一致）。
var captionEmoji = map[string]string{
	suffixPhoto: "📷",
	suffixVideo: "📹",
	suffixGif:   "📹",
	suffixDoc:   "📎",
	suffixAudio: "🎤",
	suffixRound: "📺",
}

// keyPrefix 返回会话类型对应的键前缀。
func keyPrefix(kind ChatKind) string {
	switch kind {
	case ChatGroup:
		return "CHAT_MESSAGE_"
	case ChatChannel:
		return "CHANNEL_MESSAGE_"
	default:
		return "MESSAGE_"
	}
}

// messageAlert 选出新消息的文案键与语言包参数（顺序以 iOS 语言包 PUSH_* 字符串为准）。
//
// 会话参数在前：私聊 [发送者]，群组 [发送者, 群名]，频道 [频道名]；之后是类型参数
// （正文、贴纸 emoji、联系人名、投票问题）。
//   - 文字（含链接预览）：*_TEXT，类型参数是正文；
//   - 图片、视频、文件、贴纸、语音、圆形视频、GIF、联系人、位置、实时位置、投票/测验：对应的类型键；
//   - 带文字说明的图片/视频/文件/语音/圆形视频/GIF：按 iOS 客户端的约定走 *_TEXT，
//     正文是“类型 emoji + 空格 + 说明”（如 "📷 sunset"），这样通知里既有类型也有说明；
//   - 其他类型（服务消息、骰子、清单、故事、发票……）：*_NOTEXT；
//   - showPreview=false（#96 的“关闭预览”）：一律 *_NOTEXT，参数里不出现任何消息内容。
//
// chatTitle 是群名/频道名（私聊忽略），由群组/频道推送的接入方（#98）提供。
func messageAlert(msg NewMessage, senderName, chatTitle string, showPreview bool) (Key, []string) {
	prefix := keyPrefix(msg.Chat.Kind)
	var args []string
	switch msg.Chat.Kind {
	case ChatGroup:
		args = []string{senderName, chatTitle}
	case ChatChannel:
		args = []string{chatTitle}
	default:
		args = []string{senderName}
	}
	if !showPreview {
		return Key(prefix + suffixNoText), args
	}
	text := msg.Text
	if msg.Media.IsZero() || msg.Media.Kind == domain.MessageMediaKindWebPage {
		if text == "" {
			return Key(prefix + suffixNoText), args
		}
		return Key(prefix + suffixText), append(args, text)
	}
	suffix, extra, ok := classifyMedia(msg.Media)
	if !ok {
		return Key(prefix + suffixNoText), args
	}
	if emoji, captioned := captionEmoji[suffix]; captioned && text != "" {
		return Key(prefix + suffixText), append(args, emoji+" "+text)
	}
	return Key(prefix + suffix), append(args, extra...)
}

// classifyMedia 把媒体归到文案类型后缀，并给出该类型的参数；不在覆盖范围内时 ok=false。
func classifyMedia(media *domain.MessageMedia) (suffix string, extra []string, ok bool) {
	switch media.Kind {
	case domain.MessageMediaKindPhoto:
		if media.Photo == nil {
			return "", nil, false
		}
		return suffixPhoto, nil, true
	case domain.MessageMediaKindDocument:
		if media.Document == nil {
			return "", nil, false
		}
		return classifyDocument(media)
	case domain.MessageMediaKindContact:
		if media.Contact == nil {
			return "", nil, false
		}
		return suffixContact, []string{contactName(*media.Contact)}, true
	case domain.MessageMediaKindGeo, domain.MessageMediaKindVenue:
		return suffixGeo, nil, true
	case domain.MessageMediaKindGeoLive:
		return suffixGeoLive, nil, true
	case domain.MessageMediaKindPoll:
		if media.Poll == nil {
			return "", nil, false
		}
		if media.Poll.Quiz {
			return suffixQuiz, []string{media.Poll.Question}, true
		}
		return suffixPoll, []string{media.Poll.Question}, true
	default:
		return "", nil, false
	}
}

// classifyDocument 区分文档的具体形态：贴纸、GIF、语音、圆形视频、视频，其余（含音乐）按文件。
func classifyDocument(media *domain.MessageMedia) (string, []string, bool) {
	doc := media.Document
	if doc.IsSticker() {
		alt := ""
		for _, attr := range doc.Attributes {
			if attr.Kind == domain.DocAttrSticker {
				alt = attr.Alt
				break
			}
		}
		return suffixSticker, []string{alt}, true
	}
	if doc.IsGif() {
		return suffixGif, nil, true
	}
	voice, round, video := media.Voice, media.Round, media.Video
	for _, attr := range doc.Attributes {
		switch attr.Kind {
		case domain.DocAttrAudio:
			voice = voice || attr.Voice
		case domain.DocAttrVideo:
			if attr.RoundMessage {
				round = true
			} else {
				video = true
			}
		}
	}
	switch {
	case voice:
		return suffixAudio, nil, true
	case round:
		return suffixRound, nil, true
	case video:
		return suffixVideo, nil, true
	default:
		return suffixDoc, nil, true
	}
}

// contactName 是联系人卡片的显示名：名 + 姓，都为空时退回电话号码。
func contactName(c domain.MessageContact) string {
	name := strings.TrimSpace(strings.TrimSpace(c.FirstName) + " " + strings.TrimSpace(c.LastName))
	if name == "" {
		name = strings.TrimSpace(c.PhoneNumber)
	}
	return name
}

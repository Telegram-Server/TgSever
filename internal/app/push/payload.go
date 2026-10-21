package push

import (
	"bytes"
	"encoding/json"
	"fmt"
	"strconv"
	"unicode/utf8"
)

// ChatKind 区分推送所属会话的类型，决定 payload 里的会话字段与文案键前缀。
type ChatKind uint8

const (
	// ChatPrivate 是私聊：payload 用 from_id（对端 user），文案键 MESSAGE_*。
	ChatPrivate ChatKind = iota
	// ChatGroup 是超级群：本服务器没有 basic group，payload 用 channel_id，文案键 CHAT_MESSAGE_*（#98）。
	ChatGroup
	// ChatChannel 是广播频道：payload 用 channel_id，文案键 CHANNEL_MESSAGE_*（#98）。
	ChatChannel
)

// Chat 是推送所属的会话（收件人视角）：私聊时 ID 是对端 user id，群/频道时是 channel id。
type Chat struct {
	Kind ChatKind
	ID   int64
}

// Alert 是服务端按 iOS 语言包渲染好的通知文案。iOS NSE 不做本地化，直接展示这里的字符串。
type Alert struct {
	Title    string `json:"title,omitempty"`
	Subtitle string `json:"subtitle,omitempty"`
	Body     string `json:"body"`
}

// MessagePush 是一条新消息推送的展示内容（已渲染、未加密）。
type MessagePush struct {
	Chat Chat
	// FromUserID 是群组消息的发送者（chat_from_id）；私聊、频道、匿名发送时为 0。
	FromUserID int64
	MessageID  int
	// Key 是内部分类键（MESSAGE_TEXT、CHAT_MESSAGE_PHOTO…），也是语言包 "PUSH_"+Key 的后缀。
	// 它不写进 payload：新消息的顶层 loc-key 会让 NSE 置空通知内容。
	Key    Key
	Alert  Alert
	Silent bool
}

// apsPayload 是 aps 字典。所有自定义值都是字符串（NSE 用 `as? String` 读取，数字会被忽略）；
// mutable-content 是 Apple 定义的整数字段。
type apsPayload struct {
	Alert          any    `json:"alert,omitempty"`
	Sound          string `json:"sound,omitempty"`
	MutableContent int    `json:"mutable-content,omitempty"`
	Category       string `json:"category,omitempty"`
	ThreadID       string `json:"thread-id,omitempty"`
}

// messageFields 是新消息推送在解密后 JSON（或不加密时外层）里的扁平字段。
type messageFields struct {
	FromID    string `json:"from_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	// ChatFromID 是群组消息的发送者（与官方推送的 chat_from_id 同名）；NSE 不读它，仅供识别。
	ChatFromID string      `json:"chat_from_id,omitempty"`
	MsgID      string      `json:"msg_id,omitempty"`
	Silent     string      `json:"silent,omitempty"`
	APS        *apsPayload `json:"aps,omitempty"`
}

// encryptedEnvelope 是带 secret 的外层 payload：内容全部在 p 里，外层只有通用文案。
type encryptedEnvelope struct {
	APS apsPayload `json:"aps"`
	P   string     `json:"p"`
}

// APNs payload 上限（按最终外层 JSON 的字节数计）：普通推送 4KB，VoIP 5KB。
const (
	apnsMaxPayloadBytes     = 4096
	apnsMaxVoIPPayloadBytes = 5120
)

// marshalJSON 序列化 payload，不把 < > & 转义成 \u003c 之类（每个 6 字节），以免白占 4KB 上限。
func marshalJSON(v any) ([]byte, error) {
	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(v); err != nil {
		return nil, err
	}
	return bytes.TrimSuffix(buf.Bytes(), []byte("\n")), nil
}

// errPayloadTooLarge 表示 payload 缩到最短仍超过 APNs 上限。
func errPayloadTooLarge(size, limit int) error {
	return fmt.Errorf("push payload is %d bytes, over the APNs limit of %d", size, limit)
}

// truncateBytes 把 s 截到不超过 limit 字节（按字符边界），截掉了内容时末尾加省略号（计入 limit）。
func truncateBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	const ellipsis = "…"
	limit -= len(ellipsis)
	if limit <= 0 {
		return ellipsis
	}
	for limit > 0 && !utf8.RuneStart(s[limit]) {
		limit--
	}
	return s[:limit] + ellipsis
}

func (m MessagePush) fields() messageFields {
	f := messageFields{}
	peer := strconv.FormatInt(m.Chat.ID, 10)
	switch m.Chat.Kind {
	case ChatPrivate:
		f.FromID = peer
	default:
		f.ChannelID = peer
		if m.Chat.Kind == ChatGroup && m.FromUserID != 0 {
			f.ChatFromID = strconv.FormatInt(m.FromUserID, 10)
		}
	}
	if m.MessageID != 0 {
		f.MsgID = strconv.Itoa(m.MessageID)
	}
	if m.Silent {
		f.Silent = "1"
	}
	alert := m.Alert
	f.APS = &apsPayload{
		Alert:          alert,
		Sound:          soundFor(m.Silent),
		MutableContent: 1,
		Category:       "r",
		ThreadID:       peer,
	}
	return f
}

func soundFor(silent bool) string {
	if silent {
		return ""
	}
	return "default"
}

// buildMessagePayload 生成新消息推送的 APNs payload。
//
// 有 secret（iOS 恒有）：外层 {"aps":{alert:通用文案, mutable-content:1, sound}, "p":加密内容}，
// p 解密后是 {from_id|channel_id, msg_id, silent?, aps:{alert:{title,body}, sound?, category, thread-id}}。
// 无 secret：同样的字段直接放在外层，alert 是已渲染的 title/body（不用 Apple 侧 loc-key）。
// genericBody 是外层通用文案（NSE 失败或超时时用户看到的内容），绝不能含消息正文。
func buildMessagePayload(m MessagePush, secret []byte, genericBody string) ([]byte, error) {
	// 超过 4KB 时缩短文案（先 body、再 title，按字符截断加省略号）并重新加密，直到放得下。
	// 每轮按超出的字节数截短（加密后 base64 放大约 4/3，所以按 3/4 折算，至少截 16 字节）。
	for range 64 {
		payload, err := encodeMessagePayload(m, secret, genericBody)
		if err != nil {
			return nil, err
		}
		over := len(payload) - apnsMaxPayloadBytes
		if over <= 0 {
			return payload, nil
		}
		cut := over
		if len(secret) != 0 {
			cut = over * 3 / 4
		}
		cut = max(cut, 16)
		switch {
		case len(m.Alert.Body) > len("…"):
			m.Alert.Body = truncateBytes(m.Alert.Body, len(m.Alert.Body)-cut)
		case len(m.Alert.Title) > len("…"):
			m.Alert.Title = truncateBytes(m.Alert.Title, len(m.Alert.Title)-cut)
		default:
			return nil, errPayloadTooLarge(len(payload), apnsMaxPayloadBytes)
		}
	}
	return nil, fmt.Errorf("push payload did not fit the APNs limit of %d bytes", apnsMaxPayloadBytes)
}

func encodeMessagePayload(m MessagePush, secret []byte, genericBody string) ([]byte, error) {
	fields := m.fields()
	if len(secret) == 0 {
		return marshalJSON(fields)
	}
	// 解密后 JSON 的 aps 里不需要 mutable-content（那是外层给系统看的）。
	fields.APS.MutableContent = 0
	inner, err := marshalJSON(fields)
	if err != nil {
		return nil, err
	}
	p, err := encryptPayload(secret, inner)
	if err != nil {
		return nil, err
	}
	return marshalJSON(encryptedEnvelope{
		APS: apsPayload{
			Alert:          Alert{Body: genericBody},
			Sound:          soundFor(m.Silent),
			MutableContent: 1,
		},
		P: p,
	})
}

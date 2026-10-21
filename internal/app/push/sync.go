package push

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// 已读/删除同步的 loc-key：iOS NSE 收到后清掉对应通知，处理完不展示任何内容。
const (
	KeyReadHistory    Key = "READ_HISTORY"
	KeyMessageDeleted Key = "MESSAGE_DELETED"
)

// SyncPush 是一条已读或删除同步推送的内容（收件人视角）。
//   - READ_HISTORY：Chat + MaxID，NSE 移除该会话 msg_id <= MaxID 的通知；
//   - MESSAGE_DELETED：Chat + MessageIDs，NSE 移除这些消息的通知。
//
// 同步推送只用会话字段（from_id / channel_id），Chat.Kind 只区分私聊与频道。
type SyncPush struct {
	Key        Key
	Chat       Chat
	MaxID      int
	MessageIDs []int
}

// syncFields 是同步推送解密后的 JSON：顶层 loc-key 加会话字段，没有 aps（NSE 处理后不展示）。
// 所有值都是字符串（NSE 用 `as? String` 读取）。
type syncFields struct {
	LocKey    string `json:"loc-key"`
	FromID    string `json:"from_id,omitempty"`
	ChannelID string `json:"channel_id,omitempty"`
	MaxID     string `json:"max_id,omitempty"`
	Messages  string `json:"messages,omitempty"`
}

var errSyncNeedsSecret = errors.New("sync push needs a push secret: the NSE only handles encrypted payloads")

// buildSyncNotification 生成已读/删除同步推送（未填 Token/Sandbox）。
//
// 外层 {"aps":{alert:通用文案, mutable-content:1}, "p":加密内容}：mutable-content 让系统
// 唤起 NSE，alert 只是 NSE 失败时的兜底（不含任何内容），不带提示音。优先级 5，
// collapse-id 按会话（已读）或会话 + 消息 id 范围（删除）。
//
// 必须有 secret：NSE 只解析 p，明文同步推送会被系统直接展示成一条“新消息”通知。
func buildSyncNotification(s SyncPush, secret []byte, genericBody string) (Notification, error) {
	if len(secret) == 0 {
		return Notification{}, errSyncNeedsSecret
	}
	if s.Chat.ID == 0 {
		return Notification{}, fmt.Errorf("sync push %s: missing chat", s.Key)
	}
	fields := syncFields{LocKey: string(s.Key)}
	peer := strconv.FormatInt(s.Chat.ID, 10)
	peerTag := "u" + peer
	if s.Chat.Kind == ChatPrivate {
		fields.FromID = peer
	} else {
		fields.ChannelID = peer
		peerTag = "c" + peer
	}
	var collapseID string
	switch s.Key {
	case KeyReadHistory:
		if s.MaxID <= 0 {
			return Notification{}, errors.New("read history push: missing max_id")
		}
		fields.MaxID = strconv.Itoa(s.MaxID)
		// 同一会话后到的已读覆盖先到的。
		collapseID = "read_" + peerTag
	case KeyMessageDeleted:
		ids := normalizeSyncIDs(s.MessageIDs)
		if len(ids) == 0 {
			return Notification{}, errors.New("message deleted push: missing message ids")
		}
		parts := make([]string, len(ids))
		for i, id := range ids {
			parts[i] = strconv.Itoa(id)
		}
		fields.Messages = strings.Join(parts, ",")
		// 不同批次的删除不能互相覆盖，collapse-id 带上本批的 id 范围（不超过 APNs 的 64 字节）。
		collapseID = "del_" + peerTag + "_" + parts[0] + "_" + parts[len(parts)-1]
	default:
		return Notification{}, fmt.Errorf("unsupported sync push key %q", s.Key)
	}
	inner, err := marshalJSON(fields)
	if err != nil {
		return Notification{}, err
	}
	p, err := encryptPayload(secret, inner)
	if err != nil {
		return Notification{}, err
	}
	payload, err := marshalJSON(encryptedEnvelope{
		APS: apsPayload{Alert: Alert{Body: genericBody}, MutableContent: 1},
		P:   p,
	})
	if err != nil {
		return Notification{}, err
	}
	if len(payload) > apnsMaxPayloadBytes {
		return Notification{}, errPayloadTooLarge(len(payload), apnsMaxPayloadBytes)
	}
	return Notification{
		Type:       PushTypeAlert,
		Priority:   PriorityLow,
		CollapseID: collapseID,
		Payload:    payload,
	}, nil
}

// ReadHistory 是“用户在别的设备上读到了 MaxID”：清掉该会话 msg_id <= MaxID 的通知。
type ReadHistory struct {
	UserID int64
	Chat   Chat
	MaxID  int
}

// DeletedMessages 是“用户视角下这些消息被删除了”：清掉它们的通知。MessageIDs 是 UserID 的
// message box id（即客户端看到的 msg_id）。只做私聊。
type DeletedMessages struct {
	UserID     int64
	Chat       Chat
	MessageIDs []int
}

const (
	// maxDeletedIDsPerPush 控制单条删除同步的 id 数，保证加密后 payload 在 4KB 以内。
	maxDeletedIDsPerPush = 100
	// maxDeletedPushesPerChat 限制一次删除（如清空历史）产生的推送数：只同步最新的这些 id，
	// 更早的消息基本不会还在通知栏里。
	maxDeletedPushesPerChat = 3
)

// ReadHistory 把已读同步放进队列，立即返回；参数不完整、服务停用或队列满时返回 false。
func (s *Service) ReadHistory(r ReadHistory) bool {
	if r.UserID == 0 || r.Chat.ID == 0 || r.MaxID <= 0 {
		return false
	}
	return s.enqueue("read_history", func(ctx context.Context) {
		s.deliverSync(ctx, "read_history", r.UserID, []SyncPush{{Key: KeyReadHistory, Chat: r.Chat, MaxID: r.MaxID}})
	})
}

// DeleteMessages 把删除同步放进队列，立即返回；参数不完整、服务停用或队列满时返回 false。
func (s *Service) DeleteMessages(d DeletedMessages) bool {
	ids := normalizeSyncIDs(d.MessageIDs)
	if d.UserID == 0 || d.Chat.ID == 0 || len(ids) == 0 {
		return false
	}
	if limit := maxDeletedIDsPerPush * maxDeletedPushesPerChat; len(ids) > limit {
		ids = ids[len(ids)-limit:]
	}
	pushes := make([]SyncPush, 0, (len(ids)+maxDeletedIDsPerPush-1)/maxDeletedIDsPerPush)
	for chunk := range slices.Chunk(ids, maxDeletedIDsPerPush) {
		pushes = append(pushes, SyncPush{Key: KeyMessageDeleted, Chat: d.Chat, MessageIDs: chunk})
	}
	return s.enqueue("message_deleted", func(ctx context.Context) {
		s.deliverSync(ctx, "message_deleted", d.UserID, pushes)
	})
}

// deliverSync 把同步推送发给 userID 所有不在前台的 APNs 设备。
// 不查通知设置：静音的会话同样有通知要清（同步不是新提醒，也不发声音）。
func (s *Service) deliverSync(ctx context.Context, kind string, userID int64, pushes []SyncPush) {
	devices, err := s.devices.ListPushDevicesByUser(ctx, userID)
	if err != nil {
		s.log.Warn("list push devices failed", zap.Int64("user_id", userID), zap.Error(err))
		return
	}
	render := newJobRenderer(s.render)
	for _, device := range devices {
		if device.TokenType != domain.PushTokenAPNs || device.UserID != userID {
			continue
		}
		if len(device.Secret) != SecretSize {
			// 没有 secret（或长度不对）时 NSE 处理不了，发出去只会多出一条“新消息”通知。
			continue
		}
		if s.presence != nil && s.presence.AuthorizationInForeground(device.AuthKeyID) {
			continue
		}
		generic := render.Render(ctx, device.LangCode, KeyEncryptedMessage, "").Body
		for _, p := range pushes {
			n, err := buildSyncNotification(p, device.Secret, generic)
			if err != nil {
				s.log.Warn("build sync push failed", zap.String("kind", kind), zap.Int64("user_id", userID),
					zap.String("token", RedactToken(device.Token)), zap.Error(err))
				continue
			}
			n.Token = device.Token
			n.Sandbox = device.AppSandbox
			s.send(ctx, kind, device, n)
		}
	}
}

// normalizeSyncIDs 去掉非正数与重复并升序排列。
func normalizeSyncIDs(ids []int) []int {
	out := make([]int, 0, len(ids))
	for _, id := range ids {
		if id > 0 {
			out = append(out, id)
		}
	}
	slices.Sort(out)
	return slices.Compact(out)
}

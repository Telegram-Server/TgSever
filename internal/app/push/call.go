package push

import (
	"context"
	"encoding/base64"
	"errors"
	"slices"

	"go.uber.org/zap"

	"telesrv/internal/domain"
)

// CallUpdatesEncoder 把来电的 TL Updates（constructor updates，内含 updatePhoneCall(phoneCallRequested)
// 与主叫 user）按某台设备授权绑定的 layer 序列化。TL 与 layer 属于 rpc 层，推送服务只按设备调用它。
type CallUpdatesEncoder func(ctx context.Context, authKeyID [8]byte) ([]byte, error)

// IncomingCall 是一次来电（#99）：给被叫不在前台的 VoIP 登记发 PushKit 推送，iOS 据此弹出 CallKit 来电界面。
//
// iOS 要求每条 VoIP 推送都上报一次 CallKit 来电，否则系统会终止 App 并停止投递 VoIP 推送，
// 所以只有真实来电（phone.requestCall 成功）才能调用它，不能拿 VoIP 推送做别的用途。
type IncomingCall struct {
	CallID   int64
	CallerID int64
	CalleeID int64
	Updates  CallUpdatesEncoder
}

// voipFields 是 VoIP 推送解密后的 JSON：只有 updates（base64url 编码的 TL Updates）。
// 不能带 from_id/group_call_id/msg_id：客户端见到它们会走群通话分支。
type voipFields struct {
	Updates string `json:"updates"`
}

// voipEnvelope 是 VoIP 推送的外层 payload：PushKit 不展示内容，只需要 p。
type voipEnvelope struct {
	P string `json:"p"`
}

// buildVoIPPayload 生成 VoIP 推送 payload：{"p": 加密({"updates": base64url(updates)})}。
// PushKit 处理代码只认加密的 p，没有 secret 时报错（不能退回明文）。
func buildVoIPPayload(updates []byte, secret []byte) ([]byte, error) {
	if len(secret) == 0 {
		return nil, errors.New("voip push requires a device secret")
	}
	inner, err := marshalJSON(voipFields{Updates: base64.RawURLEncoding.EncodeToString(updates)})
	if err != nil {
		return nil, err
	}
	p, err := encryptPayload(secret, inner)
	if err != nil {
		return nil, err
	}
	payload, err := marshalJSON(voipEnvelope{P: p})
	if err != nil {
		return nil, err
	}
	if len(payload) > apnsMaxVoIPPayloadBytes {
		return nil, errPayloadTooLarge(len(payload), apnsMaxVoIPPayloadBytes)
	}
	return payload, nil
}

// IncomingCall 把来电推送放进队列，立即返回；返回 false 表示事件不完整、服务停用或队列已满被丢弃。
func (s *Service) IncomingCall(call IncomingCall) bool {
	if call.Updates == nil || call.CalleeID == 0 || call.CalleeID == call.CallerID {
		return false
	}
	if capable, ok := s.sender.(VoIPCapable); ok && !capable.VoIPEnabled() {
		return false
	}
	return s.enqueue("voip", func(ctx context.Context) { s.deliverIncomingCall(ctx, call) })
}

func (s *Service) deliverIncomingCall(ctx context.Context, call IncomingCall) {
	devices, err := s.devices.ListPushDevicesByUser(ctx, call.CalleeID)
	if err != nil {
		s.log.Warn("list push devices failed", zap.Int64("user_id", call.CalleeID), zap.Error(err))
		return
	}
	for _, device := range devices {
		// 后台关闭了推送的设备不请求 APNs。
		if device.TokenType != domain.PushTokenVoIP || device.UserID != call.CalleeID || !device.PushEnabled {
			continue
		}
		// 多账号去重：主叫也登录在这台设备上（other_uids），不让同一台手机给自己响铃。
		if slices.Contains(device.OtherUIDs, call.CallerID) {
			continue
		}
		// 前台设备已经从连接上收到 updatePhoneCall。
		if s.presence != nil && s.presence.AuthorizationInForeground(device.AuthKeyID) {
			continue
		}
		if len(device.Secret) != SecretSize {
			s.log.Warn("voip push device has no usable secret; skipped",
				zap.Int64("user_id", call.CalleeID), zap.String("token", RedactToken(device.Token)), zap.Int("secret_len", len(device.Secret)))
			continue
		}
		updates, err := call.Updates(ctx, device.AuthKeyID)
		if err != nil {
			s.log.Warn("encode voip call updates failed", zap.Int64("user_id", call.CalleeID), zap.Int64("call_id", call.CallID),
				zap.String("token", RedactToken(device.Token)), zap.Error(err))
			continue
		}
		payload, err := buildVoIPPayload(updates, device.Secret)
		if err != nil {
			s.log.Warn("build voip payload failed", zap.Int64("user_id", call.CalleeID), zap.String("token", RedactToken(device.Token)), zap.Error(err))
			continue
		}
		// 来电过时即无意义：apns-expiration 0，网关只尝试投递一次、不存储补发。
		s.send(ctx, "voip", device, Notification{
			Token:             device.Token,
			Sandbox:           device.AppSandbox,
			Type:              PushTypeVoIP,
			Priority:          PriorityHigh,
			ExpireImmediately: true,
			Payload:           payload,
		})
	}
}

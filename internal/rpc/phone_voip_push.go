package rpc

import (
	"context"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap"

	"telesrv/internal/app/push"
	"telesrv/internal/domain"
)

// CallPushNotifier 是来电 VoIP 推送用到的推送服务能力（*push.Service 满足它）。方法只入队、立即返回。
type CallPushNotifier interface {
	IncomingCall(call push.IncomingCall) bool
}

// SetCallPushNotifier 注入来电 VoIP 推送（#99）。推送服务依赖 router 的 presence，只能在 router
// 创建之后注入；必须在开始处理 RPC 之前调用。nil 表示不发 VoIP 推送。
func (r *Router) SetCallPushNotifier(notifier CallPushNotifier) {
	r.callPush = notifier
}

// notifyIncomingCallPush 在来电已推给被叫在线设备之后，交给推送服务给被叫不在前台的
// VoIP 登记发 PushKit 推送。只在真实来电（requestCall 成功）时调用：iOS 要求每条 VoIP
// 推送都上报一次 CallKit 来电。
//
// payload 里的 updates 与在线推送同形（constructor updates：updatePhoneCall(phoneCallRequested)
// + 双方 user，主叫即 admin_id 必须在 users 里），按每台设备授权记下的 layer 分别序列化。
func (r *Router) notifyIncomingCallPush(ctx context.Context, call domain.PhoneCall) {
	if r.callPush == nil {
		return
	}
	updates := r.phoneCallUpdates(ctx, call, call.ParticipantID)
	r.callPush.IncomingCall(push.IncomingCall{
		CallID:   call.ID,
		CallerID: call.AdminID,
		CalleeID: call.ParticipantID,
		Updates: func(ctx context.Context, authKeyID [8]byte) ([]byte, error) {
			return encodeVoIPCallUpdates(updates, r.voipPushProfile(ctx, authKeyID))
		},
	})
}

// voipPushProfile 选出给某个授权（perm auth key）序列化 VoIP updates 的 layer：
// 授权协商过的 layer（与新连接继承的默认值同源）；没有记录、不受支持或读取失败时用规范 layer。
func (r *Router) voipPushProfile(ctx context.Context, authKeyID [8]byte) tlprofile.Profile {
	layer, found, err := r.ResolveInheritedAuthKeyLayer(ctx, authKeyID)
	if err != nil && r.log != nil {
		r.log.Debug("resolve voip push layer failed; using canonical layer", zap.Error(err))
	}
	if err == nil && found {
		if profile, ok := tlprofile.ResolveProfile(layer); ok {
			return profile
		}
	}
	return tlprofile.ProfileCanonical
}

// encodeVoIPCallUpdates 按 profile 序列化来电 Updates（不压缩；客户端也接受 gzip_packed）。
func encodeVoIPCallUpdates(updates *tg.Updates, profile tlprofile.Profile) ([]byte, error) {
	var buf bin.Buffer
	if err := tlprofile.EncodeObject(profile, updates, &buf); err != nil {
		return nil, err
	}
	return buf.Copy(), nil
}

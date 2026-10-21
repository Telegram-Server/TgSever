package push

import (
	"context"
	"time"

	"telesrv/internal/domain"
)

// ResolveNotify 组合会话自己的设置（peer）与对应默认范围（私聊/群组/频道）的设置，
// 判定此刻（now）的通知行为。纯函数。
//
// 逐字段继承：会话设置里某项为 nil 时取默认范围的值，两边都没有时取服务端默认
// （显示预览、有提示音、不静音，与 tdesktop.NotifySettings 一致）。
// mute_until 晚于 now 即静音（永久静音是 int32 最大值），到期后自然恢复。
// 本期不区分 no_muted：静音一律不推送。
func ResolveNotify(peer, defaults domain.PeerNotifySettings, now time.Time) NotifyDecision {
	muteUntil := 0
	if v := pickInt(peer.MuteUntil, defaults.MuteUntil); v != nil {
		muteUntil = *v
	}
	showPreviews := true
	if v := pickBool(peer.ShowPreviews, defaults.ShowPreviews); v != nil {
		showPreviews = *v
	}
	silent := false
	if v := pickBool(peer.Silent, defaults.Silent); v != nil {
		silent = *v
	}
	return NotifyDecision{
		Mute:        int64(muteUntil) > now.Unix(),
		HidePreview: !showPreviews,
		Silent:      silent,
	}
}

func pickInt(own, inherited *int) *int {
	if own != nil {
		return own
	}
	return inherited
}

func pickBool(own, inherited *bool) *bool {
	if own != nil {
		return own
	}
	return inherited
}

// NotifyScopes 返回收件人视角的会话对应的两条通知设置作用域：会话自身（整 peer）与默认范围。
func NotifyScopes(chat Chat) (peer, defaults domain.NotifyScope) {
	switch chat.Kind {
	case ChatGroup:
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: chat.ID}},
			domain.NotifyScope{Kind: domain.NotifyScopeChats}
	case ChatChannel:
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: chat.ID}},
			domain.NotifyScope{Kind: domain.NotifyScopeBroadcasts}
	default:
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: chat.ID}},
			domain.NotifyScope{Kind: domain.NotifyScopeUsers}
	}
}

// NotifySettingsReader 读取某作用域的通知设置（store.NotifySettingsStore 满足它）。
type NotifySettingsReader interface {
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error)
}

// SettingsPolicy 是基于存储的 NotifyPolicy：读会话设置与默认范围，交给 ResolveNotify 判定。
type SettingsPolicy struct {
	settings NotifySettingsReader
	now      func() time.Time
}

// NewSettingsPolicy 创建 SettingsPolicy；now 为 nil 时用 time.Now。
func NewSettingsPolicy(settings NotifySettingsReader, now func() time.Time) *SettingsPolicy {
	if now == nil {
		now = time.Now
	}
	return &SettingsPolicy{settings: settings, now: now}
}

// Decide 实现 NotifyPolicy。
func (p *SettingsPolicy) Decide(ctx context.Context, recipientUserID int64, chat Chat) (NotifyDecision, error) {
	if p == nil || p.settings == nil {
		return NotifyDecision{}, nil
	}
	peerScope, defaultScope := NotifyScopes(chat)
	peer, _, err := p.settings.GetNotifySettings(ctx, recipientUserID, peerScope)
	if err != nil {
		return NotifyDecision{}, err
	}
	defaults, _, err := p.settings.GetNotifySettings(ctx, recipientUserID, defaultScope)
	if err != nil {
		return NotifyDecision{}, err
	}
	return ResolveNotify(peer, defaults, p.now()), nil
}

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

// TopicNotifyScope 返回 forum 话题自己的通知设置作用域；不在话题里时 ok=false。
func TopicNotifyScope(chat Chat) (domain.NotifyScope, bool) {
	if chat.TopicID == 0 || chat.Kind == ChatPrivate {
		return domain.NotifyScope{}, false
	}
	return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: chat.ID}, TopicID: chat.TopicID}, true
}

// inheritNotify 逐字段合并：own 里为 nil 的项取 inherited 的值（话题继承群组）。
func inheritNotify(own, inherited domain.PeerNotifySettings) domain.PeerNotifySettings {
	own.MuteUntil = pickInt(own.MuteUntil, inherited.MuteUntil)
	own.ShowPreviews = pickBool(own.ShowPreviews, inherited.ShowPreviews)
	own.Silent = pickBool(own.Silent, inherited.Silent)
	return own
}

// NotifyScopes 返回收件人视角的会话对应的两条通知设置作用域：会话自身（整 peer）与默认范围。
// forum 话题自己的作用域见 TopicNotifyScope。
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

// NotifySettingsReader 读取通知设置（store 的 PasswordStore 满足它）。
type NotifySettingsReader interface {
	// GetNotifySettings 读取一个 owner 在某作用域下的设置（私聊逐人判定）。
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, bool, error)
	// GetNotifySettingsForOwners 一次读出一组 owner 在同一作用域下的设置，只返回设置过的 owner：
	// SettingsPolicy.DecideBatch 每个作用域只查一次。
	GetNotifySettingsForOwners(ctx context.Context, ownerUserIDs []int64, scope domain.NotifyScope) (map[int64]domain.PeerNotifySettings, error)
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

// Decide 实现 NotifyPolicy。forum 话题里的消息先看话题自己的设置，没设的项继承群组，再继承默认范围。
func (p *SettingsPolicy) Decide(ctx context.Context, recipientUserID int64, chat Chat) (NotifyDecision, error) {
	if p == nil || p.settings == nil {
		return NotifyDecision{}, nil
	}
	peerScope, defaultScope := NotifyScopes(chat)
	peer, _, err := p.settings.GetNotifySettings(ctx, recipientUserID, peerScope)
	if err != nil {
		return NotifyDecision{}, err
	}
	if topicScope, ok := TopicNotifyScope(chat); ok {
		topic, _, err := p.settings.GetNotifySettings(ctx, recipientUserID, topicScope)
		if err != nil {
			return NotifyDecision{}, err
		}
		peer = inheritNotify(topic, peer)
	}
	defaults, _, err := p.settings.GetNotifySettings(ctx, recipientUserID, defaultScope)
	if err != nil {
		return NotifyDecision{}, err
	}
	return ResolveNotify(peer, defaults, p.now()), nil
}

// DecideBatch 实现 BatchNotifyPolicy：每个作用域（话题、会话、默认范围）一次查询整批收件人。
func (p *SettingsPolicy) DecideBatch(ctx context.Context, recipientUserIDs []int64, chat Chat) (map[int64]NotifyDecision, error) {
	out := make(map[int64]NotifyDecision, len(recipientUserIDs))
	if p == nil || p.settings == nil || len(recipientUserIDs) == 0 {
		return out, nil
	}
	peerScope, defaultScope := NotifyScopes(chat)
	peers, err := p.settings.GetNotifySettingsForOwners(ctx, recipientUserIDs, peerScope)
	if err != nil {
		return nil, err
	}
	defaults, err := p.settings.GetNotifySettingsForOwners(ctx, recipientUserIDs, defaultScope)
	if err != nil {
		return nil, err
	}
	var topics map[int64]domain.PeerNotifySettings
	if topicScope, ok := TopicNotifyScope(chat); ok {
		if topics, err = p.settings.GetNotifySettingsForOwners(ctx, recipientUserIDs, topicScope); err != nil {
			return nil, err
		}
	}
	now := p.now()
	for _, recipient := range recipientUserIDs {
		peer := peers[recipient]
		if topics != nil {
			peer = inheritNotify(topics[recipient], peer)
		}
		out[recipient] = ResolveNotify(peer, defaults[recipient], now)
	}
	return out, nil
}

package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/compat/tdesktop"
	"telesrv/internal/domain"
)

// accountNotifySettingsService 是 per-scope 通知设置持久化的可选扩展，由
// *app/account.Service 实现。未接通时各 handler 回落历史默认（tdesktop.NotifySettings）。
type accountNotifySettingsService interface {
	GetNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope) (domain.PeerNotifySettings, error)
	UpdateNotifySettings(ctx context.Context, ownerUserID int64, scope domain.NotifyScope, slot domain.NotifySoundSlot, settings domain.PeerNotifySettings) (domain.PeerNotifySettings, error)
	ResetNotifySettings(ctx context.Context, ownerUserID int64) error
	PeerNotifySettings(ctx context.Context, ownerUserID int64, peers []domain.Peer) (map[domain.Peer]domain.PeerNotifySettings, error)
	AllPeerNotifySettings(ctx context.Context, ownerUserID int64) (map[domain.Peer]domain.PeerNotifySettings, error)
	ListNotifyExceptions(ctx context.Context, ownerUserID int64) ([]domain.NotifyException, error)
}

func (r *Router) accountNotifySvc() (accountNotifySettingsService, bool) {
	svc, ok := r.deps.Account.(accountNotifySettingsService)
	return svc, ok
}

// notifyScopeFromInput 把 InputNotifyPeer 解析为业务作用域。peer/forumTopic 需解析
// 出 domain.Peer（不校验访问权限——通知偏好是请求者自己对某 peer 的设置）。
func (r *Router) notifyScopeFromInput(userID int64, in tg.InputNotifyPeerClass) (domain.NotifyScope, bool) {
	switch p := in.(type) {
	case *tg.InputNotifyUsers:
		return domain.NotifyScope{Kind: domain.NotifyScopeUsers}, true
	case *tg.InputNotifyChats:
		return domain.NotifyScope{Kind: domain.NotifyScopeChats}, true
	case *tg.InputNotifyBroadcasts:
		return domain.NotifyScope{Kind: domain.NotifyScopeBroadcasts}, true
	case *tg.InputNotifyCommunity:
		ref, ok := inputChannelRef(p.Community)
		if !ok {
			return domain.NotifyScope{}, false
		}
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: domain.Peer{Type: domain.PeerTypeCommunity, ID: ref.ID}}, true
	case *tg.InputNotifyPeer:
		peer, ok := r.domainPeerFromInputPeer(userID, p.Peer)
		if !ok {
			return domain.NotifyScope{}, false
		}
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer}, true
	case *tg.InputNotifyForumTopic:
		peer, ok := r.domainPeerFromInputPeer(userID, p.Peer)
		if !ok {
			return domain.NotifyScope{}, false
		}
		return domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: peer, TopicID: p.TopMsgID}, true
	default:
		return domain.NotifyScope{}, false
	}
}

func (r *Router) onAccountGetNotifySettings(ctx context.Context, peer tg.InputNotifyPeerClass) (*tg.PeerNotifySettings, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	scope, ok := r.notifyScopeFromInput(userID, peer)
	svc, hasSvc := r.accountNotifySvc()
	if !ok || !hasSvc {
		return tdesktop.NotifySettings(), nil
	}
	settings, err := svc.GetNotifySettings(ctx, userID, scope)
	if err != nil {
		return nil, internalErr()
	}
	return tgScopeNotifySettings(scope, &settings), nil
}

func (r *Router) onAccountUpdateNotifySettings(ctx context.Context, req *tg.AccountUpdateNotifySettingsRequest) (bool, error) {
	if req == nil {
		return false, inputRequestInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	scope, ok := r.notifyScopeFromInput(userID, req.Peer)
	if !ok {
		return false, peerIDInvalidErr()
	}
	slot := notifySoundSlotFor(ctx)
	settings := domainPeerNotifySettings(req.Settings, slot)
	if svc, ok := r.accountNotifySvc(); ok {
		stored, err := svc.UpdateNotifySettings(ctx, userID, scope, slot, settings)
		if err != nil {
			return false, internalErr()
		}
		settings = stored
		r.notifySettings.Delete(userID)
	}
	// 推 updateNotifySettings 给本人其它在线设备（多设备静音同步）。
	r.pushUserUpdates(ctx, userID, &tg.Updates{
		Updates: []tg.UpdateClass{&tg.UpdateNotifySettings{
			Peer:           tgNotifyPeer(scope),
			NotifySettings: *tgScopeNotifySettings(scope, &settings),
		}},
		Users: []tg.UserClass{},
		Chats: []tg.ChatClass{},
		Date:  int(r.clock.Now().Unix()),
	})
	return true, nil
}

func (r *Router) onAccountResetNotifySettings(ctx context.Context) (bool, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return false, internalErr()
	}
	if svc, ok := r.accountNotifySvc(); ok {
		if err := svc.ResetNotifySettings(ctx, userID); err != nil {
			return false, internalErr()
		}
		r.notifySettings.Delete(userID)
	}
	return true, nil
}

// onAccountGetNotifyExceptions 返回"有自定义通知设置的会话"全局索引：每条异常一个
// updateNotifySettings，附带被引用 peer 的 Users/Chats。默认（无 compare 标志）只返
// 消息级异常（mute/silent/show_previews）；compare_stories 额外纳入仅 story 维度异常。
func (r *Router) onAccountGetNotifyExceptions(ctx context.Context, req *tg.AccountGetNotifyExceptionsRequest) (tg.UpdatesClass, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	empty := &tg.Updates{Updates: []tg.UpdateClass{}, Users: []tg.UserClass{}, Chats: []tg.ChatClass{}, Date: int(r.clock.Now().Unix())}
	svc, ok := r.accountNotifySvc()
	if !ok {
		return empty, nil
	}
	exceptions, err := svc.ListNotifyExceptions(ctx, userID)
	if err != nil {
		return nil, internalErr()
	}
	compareStories := req != nil && req.CompareStories
	compareSound := req != nil && req.CompareSound
	var filterPeer *domain.Peer
	if req != nil {
		if p, ok := req.GetPeer(); ok {
			if scope, ok := r.notifyScopeFromInput(userID, p); ok && scope.Kind == domain.NotifyScopePeer {
				fp := scope.Peer
				filterPeer = &fp
			}
		}
	}
	updates := make([]tg.UpdateClass, 0, len(exceptions))
	userIDs := make([]int64, 0)
	channelIDs := make([]int64, 0)
	communityIDs := make([]int64, 0)
	for _, ex := range exceptions {
		if filterPeer != nil && ex.Peer != *filterPeer {
			continue
		}
		if !notifyExceptionQualifies(ex.Settings, compareStories, compareSound) {
			continue
		}
		scope := domain.NotifyScope{Kind: domain.NotifyScopePeer, Peer: ex.Peer, TopicID: ex.TopicID}
		s := ex.Settings
		updates = append(updates, &tg.UpdateNotifySettings{
			Peer:           tgNotifyPeer(scope),
			NotifySettings: *tgScopeNotifySettings(scope, &s),
		})
		switch ex.Peer.Type {
		case domain.PeerTypeUser:
			userIDs = append(userIDs, ex.Peer.ID)
		case domain.PeerTypeChannel:
			channelIDs = append(channelIDs, ex.Peer.ID)
		case domain.PeerTypeCommunity:
			communityIDs = append(communityIDs, ex.Peer.ID)
		}
	}
	if len(updates) == 0 {
		return empty, nil
	}
	chats := r.tgChatsForChannelIDs(ctx, userID, channelIDs)
	if r.deps.Communities != nil && len(communityIDs) > 0 {
		if views, err := r.deps.Communities.GetMany(ctx, userID, communityIDs); err == nil {
			chats = appendUniqueTGChats(chats, tgCommunityChats(views)...)
		}
	}
	out := &tg.Updates{
		Updates: updates,
		Users:   r.tgUsersForIDs(ctx, userID, userIDs),
		Chats:   chats,
		Date:    int(r.clock.Now().Unix()),
	}
	r.applyPeerReadModels(ctx, userID, out.Users, out.Chats)
	return out, nil
}

// notifyExceptionQualifies 判定一条异常是否纳入 getNotifyExceptions 结果。
// compare_sound 额外纳入只改了消息声音的会话；compare_stories 同时纳入只改了 story 声音的。
func notifyExceptionQualifies(s domain.PeerNotifySettings, compareStories, compareSound bool) bool {
	if s.MuteUntil != nil || s.Silent != nil || s.ShowPreviews != nil {
		return true
	}
	if compareSound && !s.Sound.IsZero() {
		return true
	}
	if compareStories && (s.StoriesMuted != nil || s.StoriesHideSender != nil || !s.StoriesSound.IsZero()) {
		return true
	}
	return false
}

// notifySoundSlotFor 按发请求会话的客户端类型选声音槽：iOS 写 ios、Android 写
// android，其余（TDesktop、macOS、Web、未知）写 other。
func notifySoundSlotFor(ctx context.Context) domain.NotifySoundSlot {
	info, _ := ClientInfoFrom(ctx)
	switch info.Type {
	case ClientTypeIOS:
		return domain.NotifySoundSlotIOS
	case ClientTypeAndroid:
		return domain.NotifySoundSlotAndroid
	default:
		return domain.NotifySoundSlotOther
	}
}

func domainNotificationSound(in tg.NotificationSoundClass) *domain.NotificationSound {
	switch s := in.(type) {
	case *tg.NotificationSoundDefault:
		return &domain.NotificationSound{Kind: domain.NotificationSoundDefault}
	case *tg.NotificationSoundNone:
		return &domain.NotificationSound{Kind: domain.NotificationSoundNone}
	case *tg.NotificationSoundLocal:
		return &domain.NotificationSound{Kind: domain.NotificationSoundLocal, Title: s.Title, Data: s.Data}
	case *tg.NotificationSoundRingtone:
		return &domain.NotificationSound{Kind: domain.NotificationSoundRingtone, RingtoneID: s.ID}
	}
	return nil
}

func tgNotificationSound(s domain.NotificationSound) tg.NotificationSoundClass {
	switch s.Kind {
	case domain.NotificationSoundNone:
		return &tg.NotificationSoundNone{}
	case domain.NotificationSoundLocal:
		return &tg.NotificationSoundLocal{Title: s.Title, Data: s.Data}
	case domain.NotificationSoundRingtone:
		return &tg.NotificationSoundRingtone{ID: s.RingtoneID}
	default:
		return &tg.NotificationSoundDefault{}
	}
}

// tgChatsForChannelIDs 把一组 channel id 解析为 tg.Chat（无权限/已失效者跳过）。
// perf：一次批量 GetChannels，而非逐个 GetChannel（N+1）。
func (r *Router) tgChatsForChannelIDs(ctx context.Context, viewerUserID int64, channelIDs []int64) []tg.ChatClass {
	out := make([]tg.ChatClass, 0, len(channelIDs))
	if r.deps.Channels == nil || len(channelIDs) == 0 {
		return out
	}
	views, err := r.deps.Channels.GetChannels(ctx, viewerUserID, channelIDs)
	if err != nil {
		return out
	}
	for _, view := range views {
		out = append(out, tgChannelChatForView(viewerUserID, view))
	}
	return out
}

// tgPeerNotifySettings 把存储的 per-peer 设置叠加在默认通知设置上：未设置的字段
// 保留默认（声音=default、show_previews=true 等），已设置字段覆盖。nil=纯默认，
// 与历史 tdesktop.NotifySettings() 行为一致。
func tgPeerNotifySettings(s *domain.PeerNotifySettings) *tg.PeerNotifySettings {
	return applyStoredNotifySettings(tdesktop.NotifySettings(), s)
}

func tgScopeNotifySettings(scope domain.NotifyScope, s *domain.PeerNotifySettings) *tg.PeerNotifySettings {
	if scope.TopicID != 0 {
		return applyStoredNotifySettings(&tg.PeerNotifySettings{}, s)
	}
	return tgPeerNotifySettings(s)
}

func applyStoredNotifySettings(out *tg.PeerNotifySettings, s *domain.PeerNotifySettings) *tg.PeerNotifySettings {
	if s == nil {
		return out
	}
	if s.ShowPreviews != nil {
		out.SetShowPreviews(*s.ShowPreviews)
	}
	if s.Silent != nil {
		out.SetSilent(*s.Silent)
	}
	if s.MuteUntil != nil {
		out.SetMuteUntil(*s.MuteUntil)
	}
	if s.StoriesMuted != nil {
		out.SetStoriesMuted(*s.StoriesMuted)
	}
	if s.StoriesHideSender != nil {
		out.SetStoriesHideSender(*s.StoriesHideSender)
	}
	if v := s.Sound.IOS; v != nil {
		out.SetIosSound(tgNotificationSound(*v))
	}
	if v := s.Sound.Android; v != nil {
		out.SetAndroidSound(tgNotificationSound(*v))
	}
	if v := s.Sound.Other; v != nil {
		out.SetOtherSound(tgNotificationSound(*v))
	}
	if v := s.StoriesSound.IOS; v != nil {
		out.SetStoriesIosSound(tgNotificationSound(*v))
	}
	if v := s.StoriesSound.Android; v != nil {
		out.SetStoriesAndroidSound(tgNotificationSound(*v))
	}
	if v := s.StoriesSound.Other; v != nil {
		out.SetStoriesOtherSound(tgNotificationSound(*v))
	}
	return out
}

// domainPeerNotifySettings 转换入参；入参只有一个 sound/stories_sound，写进 slot 这一槽。
func domainPeerNotifySettings(in tg.InputPeerNotifySettings, slot domain.NotifySoundSlot) domain.PeerNotifySettings {
	out := domain.PeerNotifySettings{}
	if v, ok := in.GetShowPreviews(); ok {
		out.ShowPreviews = &v
	}
	if v, ok := in.GetSilent(); ok {
		out.Silent = &v
	}
	if v, ok := in.GetMuteUntil(); ok {
		out.MuteUntil = &v
	}
	if v, ok := in.GetStoriesMuted(); ok {
		out.StoriesMuted = &v
	}
	if v, ok := in.GetStoriesHideSender(); ok {
		out.StoriesHideSender = &v
	}
	if v, ok := in.GetSound(); ok {
		out.Sound = out.Sound.With(slot, domainNotificationSound(v))
	}
	if v, ok := in.GetStoriesSound(); ok {
		out.StoriesSound = out.StoriesSound.With(slot, domainNotificationSound(v))
	}
	return out
}

func tgNotifyPeer(scope domain.NotifyScope) tg.NotifyPeerClass {
	switch scope.Kind {
	case domain.NotifyScopeUsers:
		return &tg.NotifyUsers{}
	case domain.NotifyScopeChats:
		return &tg.NotifyChats{}
	case domain.NotifyScopeBroadcasts:
		return &tg.NotifyBroadcasts{}
	case domain.NotifyScopePeer:
		if scope.Peer.Type == domain.PeerTypeCommunity {
			return &tg.NotifyCommunity{CommunityID: scope.Peer.ID}
		}
		peer := tgPeer(scope.Peer)
		if scope.TopicID != 0 {
			return &tg.NotifyForumTopic{Peer: peer, TopMsgID: scope.TopicID}
		}
		return &tg.NotifyPeer{Peer: peer}
	default:
		return &tg.NotifyUsers{}
	}
}

// withDialogNotifySettings 装配 dialog 列表的 per-peer 通知设置，让静音状态在列表正确
// 显示且跨重启恢复。perf：从 per-user notify 缓存读取（命中即 0 PG），而非每次 getDialogs
// 都查 notify_settings——绝大多数用户没有任何自定义静音，缓存命中后零数据库开销。
func (r *Router) withDialogNotifySettings(ctx context.Context, viewerUserID int64, list domain.DialogList) domain.DialogList {
	if len(list.Dialogs) == 0 && len(list.Communities) == 0 {
		return list
	}
	settings := r.userNotifySettings(ctx, viewerUserID)
	if len(settings) == 0 {
		return list
	}
	for i := range list.Dialogs {
		if s, ok := settings[list.Dialogs[i].Peer]; ok {
			sc := s.Clone()
			list.Dialogs[i].NotifySettings = &sc
		}
	}
	for i := range list.Communities {
		peer := domain.Peer{Type: domain.PeerTypeCommunity, ID: list.Communities[i].Community.ID}
		if s, ok := settings[peer]; ok {
			sc := s.Clone()
			list.Communities[i].State.NotifySettings = &sc
		}
	}
	return list
}

// applyNotifySettingsToUserFull 在缓存后 overlay userFull 的 notify_settings（避免
// 投影缓存使 notify 状态陈旧）。perf：从 per-user notify 缓存读取，不再每次 getFullUser
// 单 peer 查 PG。
func (r *Router) applyNotifySettingsToUserFull(ctx context.Context, viewerUserID, ownerUserID int64, full *tg.UserFull) {
	settings := r.userNotifySettings(ctx, viewerUserID)
	s, ok := settings[domain.Peer{Type: domain.PeerTypeUser, ID: ownerUserID}]
	if !ok || s.IsZero() {
		return
	}
	full.NotifySettings = *tgPeerNotifySettings(&s)
}

// applyNotifySettingsToChannelFull 在缓存后 overlay channelFull 的 notify_settings。
func (r *Router) applyNotifySettingsToChannelFull(ctx context.Context, viewerUserID, channelID int64, full *tg.ChannelFull) {
	settings := r.userNotifySettings(ctx, viewerUserID)
	s, ok := settings[domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}]
	if !ok || s.IsZero() {
		return
	}
	full.NotifySettings = *tgPeerNotifySettings(&s)
}

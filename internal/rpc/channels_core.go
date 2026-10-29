package rpc

import (
	"context"
	"errors"
	"unicode/utf8"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func (r *Router) onChannelsCreateChannel(ctx context.Context, req *tg.ChannelsCreateChannelRequest) (tg.UpdatesClass, error) {
	if err := validateChannelsCreateChannelOptions(req); err != nil {
		return nil, err
	}
	if r.deps.Channels == nil {
		return nil, notImplementedErr()
	}
	if !domain.ValidChannelTitle(req.Title) || utf8.RuneCountInString(req.About) > maxChannelAboutLength {
		return nil, channelInvalidErr(domain.ErrChannelTitleInvalid)
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if err := r.checkPermission(ctx, userID, createChannelPermissionAction(req)); err != nil {
		return nil, err
	}
	res, err := r.deps.Channels.CreateChannel(ctx, userID, domain.CreateChannelRequest{
		CreatorUserID: userID,
		Title:         req.Title,
		About:         req.About,
		Broadcast:     req.Broadcast,
		Megagroup:     req.Megagroup,
		Forum:         req.Forum,
		ForumTabs:     req.Forum,
		TTLPeriod:     req.TTLPeriod,
		Date:          int(r.clock.Now().Unix()),
	})
	if err != nil {
		return nil, channelInvalidErr(err)
	}
	// 群已落库：先登记在线成员关系，即使下面构造响应失败，成员会话也能收到该群后续更新。
	r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
	updates, err := r.channelCreationResponseUpdates(ctx, userID, res)
	if err != nil {
		return nil, err
	}
	r.announceCreatedChannel(ctx, userID, res)
	return updates, nil
}

// announceCreatedChannel 向新群成员扇出建群的频道更新，让创建者其他在线客户端立刻看到
// 新频道。调用方须在此之前已用 addOnlineChannelMemberships 登记成员关系（建成后立即登记，
// 不依赖后续步骤是否成功）。使用者是 channels.createChannel 和后台以账号身份建群的钩子
// AdminCreateGroup；messages.createChat 不走这里，它建群后还要邀请成员，并把建群与邀请
// 两次结果合并成一份响应和推送（见 channels_legacy_chat.go），收尾自成一套。
// ctx 里带调用会话时，该会话由 RPC 响应覆盖，不重复推送；没有会话（后台钩子）时推给
// 创建者的全部在线会话。
func (r *Router) announceCreatedChannel(ctx context.Context, creatorUserID int64, res domain.CreateChannelResult) {
	r.pushChannelUpdates(ctx, creatorUserID, res.Channel.ID, res.Recipients, func(viewerUserID int64) *tg.Updates {
		return r.channelOperationUpdates(ctx, viewerUserID, res)
	})
}

// channelCreationResponseUpdates adds the response-only message mapping TDLib
// requires to recognize a channels.createChannel result. The mapping is never
// reused by fan-out or difference; the create service message remains the sole
// durable, PTS-bearing fact.
func (r *Router) channelCreationResponseUpdates(ctx context.Context, viewerUserID int64, res domain.CreateChannelResult) (*tg.Updates, error) {
	if res.Message.ID <= 0 || res.Message.Action == nil || res.Message.Action.Type != domain.ChannelActionCreate {
		return nil, internalErr()
	}
	updates := r.channelOperationUpdates(ctx, viewerUserID, res)
	if updates == nil {
		return nil, internalErr()
	}
	mapping := &tg.UpdateMessageID{ID: res.Message.ID, RandomID: randomNonZeroInt64()}
	updates.Updates = append([]tg.UpdateClass{mapping}, updates.Updates...)
	return updates, nil
}

func validateChannelsCreateChannelOptions(req *tg.ChannelsCreateChannelRequest) error {
	if req == nil {
		return inputRequestInvalidErr()
	}
	if req.ForImport {
		return chatInvalidErr()
	}
	if req.GeoPoint != nil || req.Address != "" {
		return addressInvalidErr()
	}
	if req.TTLPeriod < 0 {
		return ttlPeriodInvalidErr()
	}
	return nil
}

func (r *Router) onChannelsGetChannels(ctx context.Context, ids []tg.InputChannelClass) (tg.MessagesChatsClass, error) {
	if len(ids) > maxGetMessagesIDs {
		return nil, limitInvalidErr()
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	refs := make([]channelInputRef, 0, len(ids))
	channelIDs := make([]int64, 0, len(ids))
	for _, input := range ids {
		ref, ok := inputChannelRef(input)
		if !ok || ref.ID == 0 {
			continue
		}
		refs = append(refs, ref)
		channelIDs = append(channelIDs, ref.ID)
	}
	if len(channelIDs) == 0 || (r.deps.Channels == nil && r.deps.Communities == nil) {
		return &tg.MessagesChats{}, nil
	}
	var views []domain.ChannelView
	if r.deps.Channels != nil {
		views, err = r.deps.Channels.GetChannels(ctx, userID, channelIDs)
		if err != nil {
			return nil, internalErr()
		}
	}
	byID := make(map[int64]domain.ChannelView, len(views))
	for _, view := range views {
		byID[view.Channel.ID] = view
	}
	communityByID := make(map[int64]domain.CommunityView)
	if r.deps.Communities != nil {
		communityViews, err := r.deps.Communities.GetMany(ctx, userID, channelIDs)
		if err != nil {
			return nil, internalErr()
		}
		for _, view := range communityViews {
			communityByID[view.Community.ID] = view
		}
	}
	chats := make([]tg.ChatClass, 0, len(refs))
	for _, ref := range refs {
		if view, ok := communityByID[ref.ID]; ok {
			if !ref.CheckAccessHash || ref.AccessHash == view.Community.AccessHash {
				chats = append(chats, tgCommunityChat(view))
			}
			continue
		}
		if view, ok := byID[ref.ID]; ok && inputChannelAccessHashMatches(ref, view.Channel) {
			chats = append(chats, tgChannelChatForView(userID, view))
		}
	}
	r.applyPeerReadModels(ctx, userID, nil, chats)
	return &tg.MessagesChats{Chats: chats}, nil
}

func (r *Router) onChannelsGetFullChannel(ctx context.Context, input tg.InputChannelClass) (*tg.MessagesChatFull, error) {
	if r.deps.Channels == nil && r.deps.Communities == nil {
		return &tg.MessagesChatFull{}, nil
	}
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	ref, ok := inputChannelRef(input)
	if !ok {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	if r.deps.Communities != nil {
		view, communityErrValue := r.deps.Communities.Get(ctx, userID, ref.ID)
		if communityErrValue == nil {
			if ref.CheckAccessHash && ref.AccessHash != view.Community.AccessHash {
				return nil, channelInvalidErr(domain.ErrCommunityPrivate)
			}
			if settings := r.userNotifySettings(ctx, userID); len(settings) > 0 {
				if setting, ok := settings[domain.Peer{Type: domain.PeerTypeCommunity, ID: view.Community.ID}]; ok {
					copy := setting.Clone()
					view.State.NotifySettings = &copy
				}
			}
			out := &tg.MessagesChatFull{
				FullChat: tgCommunityFull(view),
				Chats:    tgCommunityHydratedChats(userID, view),
				Users:    tgUsers(view.Users),
			}
			r.applyPeerReadModels(ctx, userID, out.Users, out.Chats)
			return out, nil
		}
		if errors.Is(communityErrValue, domain.ErrCommunityPrivate) {
			return nil, communityErr(communityErrValue)
		}
		if !errors.Is(communityErrValue, domain.ErrCommunityInvalid) {
			return nil, communityErr(communityErrValue)
		}
	}
	if r.deps.Channels == nil {
		return nil, channelInvalidErr(domain.ErrChannelInvalid)
	}
	loadEpoch := r.channelFullProjectionCache.LoadEpoch()
	commandLang := botCommandViewerLang(ctx)
	if cached, ok := r.channelFullProjectionCache.LookupLang(userID, ref.ID, commandLang); ok {
		if !inputChannelAccessHashMatches(ref, domain.Channel{ID: ref.ID, AccessHash: cached.accessHash}) {
			return nil, channelInvalidErr(domain.ErrChannelPrivate)
		}
		full := cached.full
		if err := r.applyWelcomeMessagesToFullChat(ctx, ref.ID, &full); err != nil {
			return nil, err
		}
		if err := r.applyTranslationDisabledToChannelFull(ctx, userID, ref.ID, &full); err != nil {
			return nil, err
		}
		r.applyStarGiftsCountToChannelFull(ctx, ref.ID, &full)
		r.applyStoriesPinnedAvailableToChannelFull(ctx, userID, ref.ID, &full)
		r.applyNotifySettingsToChannelFull(ctx, userID, ref.ID, &full)
		r.applyBotVerificationToChannelFull(ctx, ref.ID, &full)
		r.applyAndroidChannelReactionEditorCompat(ctx, &full, cached.canChangeInfo)
		chats := append([]tg.ChatClass(nil), cached.chats...)
		if cached.defaultSendAsChannelID != 0 {
			view := domain.ChannelView{Channel: domain.Channel{ID: ref.ID}}
			view.Dialog.DefaultSendAs = &domain.Peer{Type: domain.PeerTypeChannel, ID: cached.defaultSendAsChannelID}
			r.applyForeignDefaultSendAsToFull(ctx, userID, view, &full, &chats)
		}
		chats = r.appendLinkedDiscussionChat(ctx, userID, ref.ID, chats)
		r.trackChannelInterest(ctx, userID, ref.ID)
		users := r.tgUsersForIDs(ctx, userID, cached.userIDs)
		r.applyPeerReadModels(ctx, userID, users, chats)
		return &tg.MessagesChatFull{
			FullChat: &full,
			Chats:    chats,
			Users:    users,
		}, nil
	}
	view, err := r.channelFullReadView(ctx, userID, input)
	if err != nil {
		return nil, err
	}
	full := tgChannelFull(view, r.cfg.PublicBaseURL)
	r.applyChannelStatsCapability(full)
	r.applyStarGiftsCountToChannelFull(ctx, view.Channel.ID, full)
	userIDs := []int64{view.Channel.CreatorUserID, view.Self.UserID}
	// 注：Bots 过滤实际会返回群内 bot（TestGroupBotRPCShape 覆盖），这里据此富化 full.BotInfo。
	// （此前审计误判为死代码，已由单测纠正——勿删。）
	botInfo := r.channelFullBotInfo(ctx, userID, view.Channel.ID, domain.BotCommandViewerIsAdmin(view.Self), commandLang)
	userIDs = append(userIDs, botInfo.userIDs...)
	full.BotInfo = append(full.BotInfo, botInfo.botInfos...)
	if canViewChannelJoinRequests(view.Self) {
		userIDs = r.applyPendingJoinRequestsToFullChannel(ctx, full, view.Channel.ID, userIDs)
	}
	r.trackChannelInterest(ctx, userID, view.Channel.ID)
	chats := []tg.ChatClass{tgChannelChatForView(userID, view)}
	chats = r.appendLinkedDiscussionChat(ctx, userID, view.Channel.ID, chats)
	if mono, ok := r.linkedMonoforumForChannelState(ctx, userID, view.Channel); ok {
		chats = appendUniqueTGChats(chats, tgChannelChat(userID, mono, nil))
	}
	// 当前频道默认已由 tgChannelFull 处理；外部频道默认（以自己拥有的别的频道身份发言）需在此投影并
	// 带上该频道对象，否则客户端拿不到默认 chip。
	var defaultSendAsChannelID int64
	if def := view.Dialog.DefaultSendAs; def != nil && def.Type == domain.PeerTypeChannel && def.ID != view.Channel.ID {
		defaultSendAsChannelID = def.ID
	}
	canChangeInfo := channelMemberCanChangeInfo(view.Self)
	r.channelFullProjectionCache.StoreIfEpoch(userID, view.Channel.ID, channelFullProjection{
		lang:                   commandLang,
		accessHash:             view.Channel.AccessHash,
		defaultSendAsChannelID: defaultSendAsChannelID,
		canChangeInfo:          canChangeInfo,
		full:                   *full,
		chats:                  append([]tg.ChatClass(nil), chats...),
		userIDs:                userIDs,
	}, loadEpoch)
	r.applyForeignDefaultSendAsToFull(ctx, userID, view, full, &chats)
	if err := r.applyWelcomeMessagesToFullChat(ctx, view.Channel.ID, full); err != nil {
		return nil, err
	}
	if err := r.applyTranslationDisabledToChannelFull(ctx, userID, view.Channel.ID, full); err != nil {
		return nil, err
	}
	r.applyStoriesPinnedAvailableToChannelFull(ctx, userID, view.Channel.ID, full)
	r.applyNotifySettingsToChannelFull(ctx, userID, view.Channel.ID, full)
	// After StoreIfEpoch on purpose: the mark stays out of the per-(viewer,channel)
	// projection cache so a revoked badge cannot outlive the change by a cache TTL.
	r.applyBotVerificationToChannelFull(ctx, view.Channel.ID, full)
	r.applyAndroidChannelReactionEditorCompat(ctx, full, canChangeInfo)
	users := r.tgUsersForIDs(ctx, userID, userIDs)
	r.applyPeerReadModels(ctx, userID, users, chats)
	return &tg.MessagesChatFull{
		FullChat: full,
		Chats:    chats,
		Users:    users,
	}, nil
}

func (r *Router) applyWelcomeMessagesToFullChat(ctx context.Context, channelID int64, full tg.ChatFullClass) error {
	if r.deps.WelcomeMessages == nil || channelID <= 0 || full == nil {
		return nil
	}
	hasAny, err := r.deps.WelcomeMessages.HasAny(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	if err != nil {
		return internalErr()
	}
	switch value := full.(type) {
	case *tg.ChannelFull:
		value.HasWelcomeMessages = hasAny
	case *tg.ChatFull:
		value.HasWelcomeMessages = hasAny
	}
	return nil
}

func (r *Router) applyStarGiftsCountToChannelFull(ctx context.Context, channelID int64, full *tg.ChannelFull) {
	if r.deps.Gifts == nil || channelID == 0 || full == nil {
		return
	}
	n, err := r.deps.Gifts.CountSaved(ctx, domain.Peer{Type: domain.PeerTypeChannel, ID: channelID})
	if err == nil && n > 0 {
		full.SetStargiftsCount(n)
	}
}

type channelReadModelResolver interface {
	GetChannelReadModel(ctx context.Context, userID, channelID int64) (domain.ChannelView, error)
}

func (r *Router) channelFullReadView(ctx context.Context, userID int64, input tg.InputChannelClass) (domain.ChannelView, error) {
	ref, ok := inputChannelRef(input)
	if !ok {
		return domain.ChannelView{}, channelInvalidErr(domain.ErrChannelInvalid)
	}
	var (
		view domain.ChannelView
		err  error
	)
	if cached, ok := r.deps.Channels.(channelReadModelResolver); ok {
		view, err = cached.GetChannelReadModel(ctx, userID, ref.ID)
	} else {
		view, err = r.deps.Channels.GetChannel(ctx, userID, ref.ID)
	}
	if err != nil {
		return domain.ChannelView{}, channelInvalidErr(err)
	}
	if !inputChannelAccessHashMatches(ref, view.Channel) {
		return domain.ChannelView{}, channelInvalidErr(domain.ErrChannelPrivate)
	}
	return view, nil
}

func (r *Router) onChannelsGetSendAs(ctx context.Context, req *tg.ChannelsGetSendAsRequest) (*tg.ChannelsSendAsPeers, error) {
	userID, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, internalErr()
	}
	if userID == 0 {
		return nil, peerIDInvalidErr()
	}
	peer, ok := r.domainPeerFromInputPeer(userID, req.Peer)
	if !ok || peer.ID == 0 {
		return nil, peerIDInvalidErr()
	}
	chats := []tg.ChatClass(nil)
	peers := []tg.SendAsPeer{{Peer: &tg.PeerUser{UserID: userID}}}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return &tg.ChannelsSendAsPeers{}, nil
		}
		view, err := r.deps.Channels.ResolveChannel(ctx, userID, peer.ID)
		if err != nil {
			return nil, channelInvalidErr(err)
		}
		if ref, ok := inputPeerChannelRef(req.Peer); ok {
			if ref.ID != view.Channel.ID || (ref.CheckAccessHash && !inputChannelAccessHashMatches(ref, view.Channel)) {
				return nil, channelInvalidErr(domain.ErrChannelPrivate)
			}
		}
		chats = []tg.ChatClass{tgChannelChatForView(userID, view)}
		owned, ownedErr := r.deps.Channels.ListSendAsChannels(ctx, userID)
		if ownedErr != nil {
			return nil, internalErr()
		}
		if req.ForPaidReactions {
			// Paid reaction identities are self plus currently owned/postable
			// broadcast channels. They are not message send-as candidates and do
			// not carry the unrelated premium_required gate.
			seen := make(map[int64]struct{}, len(owned))
			for _, ch := range owned {
				if ch.ID == 0 || ch.Deleted || !ch.Broadcast || ch.CreatorUserID != userID {
					continue
				}
				if _, ok := seen[ch.ID]; ok {
					continue
				}
				seen[ch.ID] = struct{}{}
				peers = append(peers, tg.SendAsPeer{Peer: &tg.PeerChannel{ChannelID: ch.ID}})
				if ch.ID != view.Channel.ID {
					chats = append(chats, tgChannels(userID, []domain.Channel{ch})...)
				}
			}
		} else {
			if view.Channel.Broadcast || view.Self.AdminRights.Anonymous {
				peers = nil
			}
			if view.Channel.Broadcast && view.Channel.SignatureProfiles && canSendAsForeignChannel(view) {
				peers = append(peers, tg.SendAsPeer{Peer: &tg.PeerUser{UserID: userID}})
			}
			// 以「当前频道/群本身」发言：广播频道自帖、匿名管理员等（canCurrentChannelSendAs 判定）。
			if canCurrentChannelSendAs(view) {
				peers = append(peers, tg.SendAsPeer{Peer: &tg.PeerChannel{ChannelID: view.Channel.ID}})
			}
			// Broadcast profile authors are free; in groups only linked or verified channels are exempt from Premium.
			if len(owned) > 0 && (!view.Channel.Broadcast || view.Channel.SignatureProfiles && canSendAsForeignChannel(view)) {
				extras := make([]domain.Channel, 0, len(owned))
				for _, ch := range owned {
					if ch.ID == 0 || ch.ID == view.Channel.ID {
						continue
					}
					sendAs := tg.SendAsPeer{Peer: &tg.PeerChannel{ChannelID: ch.ID}}
					if !view.Channel.Broadcast && !ch.Verified && ch.ID != view.Channel.LinkedChatID {
						sendAs.PremiumRequired = true
					}
					peers = append(peers, sendAs)
					extras = append(extras, ch)
				}
				chats = append(chats, tgChannels(userID, extras)...)
			}
		}
	}
	out := &tg.ChannelsSendAsPeers{
		Peers: peers,
		Chats: chats,
		Users: r.tgUsersForIDs(ctx, userID, []int64{userID}),
	}
	r.applyPeerReadModels(ctx, userID, out.Users, out.Chats)
	return out, nil
}

func (r *Router) applyPendingJoinRequestsToFullChannel(ctx context.Context, full *tg.ChannelFull, channelID int64, userIDs []int64) []int64 {
	if r.deps.Channels == nil || full == nil || channelID == 0 {
		return userIDs
	}
	pending, err := r.deps.Channels.PendingJoinRequests(ctx, channelID, domain.MaxChannelPendingJoinRecentRequesters)
	if err != nil || pending.Count <= 0 {
		return userIDs
	}
	full.SetRequestsPending(pending.Count)
	full.SetRecentRequesters(pending.RecentRequesters)
	return append(userIDs, pending.RecentRequesters...)
}

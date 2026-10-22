package rpc

import (
	"context"
	"errors"
	"net/url"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

// startChatJoinQuery runs only after the pending importer was committed. The
// returned query belongs to that exact request and is delivered to its guard.
func (r *Router) startChatJoinQuery(ctx context.Context, res domain.CreateChannelResult) (tg.MessagesChatInviteJoinResultClass, error) {
	if res.JoinRequest == nil {
		return nil, nil
	}
	if r.deps.Users == nil {
		return nil, botInvalidErr()
	}
	bot, err := r.deps.Channels.GetChannelGuardBot(ctx, res.Channel.ID)
	if err != nil {
		return nil, err
	}
	if bot == 0 {
		return nil, nil
	}
	query, err := r.deps.Channels.CreateChatJoinQuery(ctx, res.Channel.ID, bot, res.JoinRequest.UserID, res.JoinRequest.Date, int(r.clock.Now().Unix()))
	if err != nil {
		return nil, err
	}
	payload := res.JoinRequest.Clone()
	user, err := r.deps.Users.Self(ctx, payload.UserID)
	if err != nil {
		return nil, err
	}
	payload.About = user.About
	var invite tg.ExportedChatInviteClass = &tg.ChatInvitePublicJoinRequests{}
	if payload.Invite != nil {
		value := r.botAPIInviteURL(*payload.Invite)
		payload.Invite = &value
		invite = r.tgExportedChannelInvite(value)
	}
	update := &tg.UpdateBotChatInviteRequester{Peer: &tg.PeerChannel{ChannelID: query.ChannelID}, Date: payload.Date, UserID: payload.UserID, About: payload.About, Invite: invite}
	update.SetQueryID(query.ID)
	r.pushUserUpdates(ctx, bot, &tg.Updates{Updates: []tg.UpdateClass{update}, Users: r.tgUsersForIDs(ctx, bot, []int64{user.ID}), Chats: []tg.ChatClass{tgChannelChatMin(bot, res.Channel)}, Date: payload.Date})
	from := map[string]any{"id": user.ID, "is_bot": user.Bot, "first_name": user.FirstName}
	if user.LastName != "" {
		from["last_name"] = user.LastName
	}
	if user.Username != "" {
		from["username"] = user.Username
	}
	kind := "supergroup"
	if res.Channel.Broadcast {
		kind = "channel"
	}
	object := map[string]any{"chat": map[string]any{"id": -botAPIChannelChatIDBase - query.ChannelID, "type": kind, "title": res.Channel.Title}, "from": from, "date": payload.Date, "user_chat_id": payload.UserChatID, "query_id": strconv.FormatInt(query.ID, 10)}
	if payload.About != "" {
		object["bio"] = payload.About
	}
	if payload.Invite != nil {
		value := *payload.Invite
		creator, err := r.deps.Users.Self(ctx, value.AdminUserID)
		if err != nil {
			return nil, err
		}
		link := value.Link
		if value.AdminUserID != bot {
			link = publicLinkWithBaseURL(r.cfg.PublicBaseURL, "+...")
		}
		exported := map[string]any{"invite_link": link, "creator": map[string]any{"id": creator.ID, "is_bot": creator.Bot, "first_name": creator.FirstName}, "creates_join_request": value.RequestNeeded, "is_primary": value.Permanent, "is_revoked": value.Revoked}
		if value.Title != "" {
			exported["name"] = value.Title
		}
		if value.ExpireDate > 0 {
			exported["expire_date"] = value.ExpireDate
		}
		if value.UsageLimit > 0 {
			exported["member_limit"] = value.UsageLimit
		}
		if value.RequestNeeded {
			exported["pending_join_request_count"] = value.RequestedCount
		}
		if value.SubscriptionPeriod > 0 {
			exported["subscription_period"] = value.SubscriptionPeriod
			exported["subscription_price"] = value.SubscriptionPrice
		}
		object["invite_link"] = exported
	}
	r.enqueueBotAPIObject(ctx, bot, domain.BotAPIUpdateChatJoinRequest, object)
	return &tg.MessagesChatInviteJoinResultWebView{BotID: bot, QueryID: query.ID, Users: r.tgUsersForIDs(ctx, payload.UserID, []int64{bot})}, nil
}

func (r *Router) onBotsSetJoinChatResults(ctx context.Context, req *tg.BotsSetJoinChatResultsRequest) (bool, error) {
	bot, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	if r.deps.Channels == nil || req == nil {
		return false, tgerr400("QUERY_ID_INVALID")
	}
	result, url := "", ""
	switch value := req.Result.(type) {
	case *tg.JoinChatBotResultApproved:
		result = "approve"
	case *tg.JoinChatBotResultDeclined:
		result = "decline"
	case *tg.JoinChatBotResultQueued:
		result = "queue"
	case *tg.JoinChatBotResultWebView:
		result = "webview"
		url = value.URL
		if validateInlineWebURL(url, true) != nil {
			return false, urlInvalidErr()
		}
	default:
		return false, tgerr400("QUERY_RESULT_INVALID")
	}
	now := int(r.clock.Now().Unix())
	query, res, err := r.deps.Channels.ResolveChatJoinQuery(ctx, bot, req.QueryID, result, url, now)
	if err != nil {
		return false, chatJoinQueryErr(err)
	}
	if result == "approve" || result == "decline" {
		r.invalidateChannelFullBotInfoCacheForChannel(res.Channel.ID)
		r.addOnlineChannelMemberships(res.Channel.ID, channelMemberUserIDs(res.Members)...)
		r.prepareChannelInviteViewers(ctx, &res)
		r.pushChannelUpdates(ctx, bot, res.Channel.ID, res.Recipients, func(viewer int64) *tg.Updates { return r.channelOperationUpdates(ctx, viewer, res) })
		r.enqueueBotAPIMemberUpdates(ctx, botAPIJoinRequestMemberChanges(bot, res, now)...)
		r.pushPendingJoinRequestsToAdmins(ctx, res.Channel)
	} else if result == "queue" {
		r.deliverChatJoinRequest(ctx, res)
		r.pushPendingJoinRequestsToAdmins(ctx, res.Channel)
	}
	r.pushUserUpdates(ctx, query.UserID, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateJoinChatWebViewDecision{Peer: &tg.PeerChannel{ChannelID: query.ChannelID}, QueryID: query.ID, Result: req.Result}}, Date: now})
	return true, nil
}

func (r *Router) onMessagesRequestChatJoinWebView(ctx context.Context, req *tg.MessagesRequestChatJoinWebViewRequest) (*tg.WebViewResultURL, error) {
	user, _, err := r.currentUserID(ctx)
	if err != nil {
		return nil, err
	}
	if r.deps.Channels == nil || r.deps.Users == nil || r.deps.Bots == nil || req == nil {
		return nil, tgerr400("QUERY_ID_INVALID")
	}
	if err = validateWebViewPlatform(req.Platform); err != nil {
		return nil, err
	}
	// The client can ask immediately after import. Wait only for the guard's
	// ten-second initial response; no synthetic URL is returned while pending.
	deadline := time.NewTimer(10 * time.Second)
	defer deadline.Stop()
	tick := time.NewTicker(100 * time.Millisecond)
	defer tick.Stop()
	for {
		query, err := r.deps.Channels.GetChatJoinQuery(ctx, req.QueryID)
		if err != nil {
			return nil, chatJoinQueryErr(err)
		}
		if query.UserID != user || query.ExpiresAt <= int(r.clock.Now().Unix()) {
			return nil, tgerr400("QUERY_ID_INVALID")
		}
		bot, err := r.deps.Channels.GetChannelGuardBot(ctx, query.ChannelID)
		if err != nil {
			return nil, chatJoinQueryErr(err)
		}
		if bot != query.BotUserID {
			return nil, tgerr400("QUERY_ID_INVALID")
		}
		if query.State == "webview" {
			account, err := r.deps.Users.Self(ctx, user)
			if err != nil {
				return nil, err
			}
			botUser, err := r.deps.Users.Self(ctx, bot)
			if err != nil {
				return nil, err
			}
			_, profile, err := r.webViewBotFromInput(ctx, user, &tg.InputUser{UserID: bot, AccessHash: botUser.AccessHash})
			if err != nil {
				return nil, err
			}
			signed, err := chatJoinWebViewURL(query, profile, account, req, r.clock.Now())
			if err != nil {
				return nil, err
			}
			out := &tg.WebViewResultURL{URL: signed}
			out.SetQueryID(query.ID)
			return out, nil
		}
		if query.State != "pending" {
			return nil, tgerr400("QUERY_ID_INVALID")
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-deadline.C:
			return nil, tgerr400("QUERY_ID_INVALID")
		case <-tick.C:
		}
	}
}

func chatJoinWebViewURL(query domain.ChatJoinQuery, profile domain.BotProfile, user domain.User, req *tg.MessagesRequestChatJoinWebViewRequest, now time.Time) (string, error) {
	raw, err := webViewURLWithInitData(query.WebAppURL, strconv.FormatInt(query.ID, 10), profile, user, "", req.Platform, now)
	if err != nil {
		return "", err
	}
	parsed, err := url.Parse(raw)
	if err != nil {
		return "", err
	}
	params := parsed.Query()
	data, err := url.ParseQuery(params.Get("tgWebAppData"))
	if err != nil {
		return "", err
	}
	data.Del("query_id")
	data.Set("chat_join_request_query_id", strconv.FormatInt(query.ID, 10))
	data.Set("hash", webViewInitDataHash(data, domain.FormatBotToken(profile.BotUserID, profile.TokenSecret)))
	params.Set("tgWebAppData", data.Encode())
	params.Set("tgWebAppVersion", "10.3")
	if theme, ok := req.GetThemeParams(); ok {
		params.Set("tgWebAppThemeParams", theme.Data)
	}
	parsed.RawQuery = params.Encode()
	return parsed.String(), nil
}

// ProcessExpiredChatJoinQueries hands unanswered requests back to ordinary
// administrators. Expiry and single consumption are persisted before fan-out.
func (r *Router) ProcessExpiredChatJoinQueries(ctx context.Context, limit int) (int, error) {
	if r.deps.Channels == nil {
		return 0, nil
	}
	now := int(r.clock.Now().Unix())
	outcomes, err := r.deps.Channels.ExpireChatJoinQueries(ctx, now, limit)
	for _, outcome := range outcomes {
		r.deliverChatJoinRequest(ctx, outcome.Result)
		r.pushPendingJoinRequestsToAdmins(ctx, outcome.Result.Channel)
		query := outcome.Query
		r.pushUserUpdates(ctx, query.UserID, &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateJoinChatWebViewDecision{Peer: &tg.PeerChannel{ChannelID: query.ChannelID}, QueryID: query.ID, Result: &tg.JoinChatBotResultQueued{}}}, Date: now})
	}
	return len(outcomes), err
}

func (r *Router) applyGuardBotToChannelFull(ctx context.Context, channel int64, full *tg.ChannelFull) error {
	full.GuardBotID = 0
	full.Flags2.Unset(23)
	bot, err := r.deps.Channels.GetChannelGuardBot(ctx, channel)
	if err != nil {
		return internalErr()
	}
	if bot != 0 {
		full.SetGuardBotID(bot)
	}
	return nil
}

func chatJoinQueryErr(err error) error {
	if errors.Is(err, domain.ErrChatJoinQueryInvalid) || errors.Is(err, domain.ErrHideRequesterMissing) {
		return tgerr400("QUERY_ID_INVALID")
	}
	if errors.Is(err, domain.ErrChatJoinQueryResultInvalid) {
		return tgerr400("QUERY_RESULT_INVALID")
	}
	return channelInviteErr(err)
}

package rpc

import (
	"context"
	"strconv"
	"time"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

const botGuestQueryTTL = 5 * time.Minute

func (r *Router) BotAPIAnswerGuestQuery(ctx context.Context, botID, queryID int64, result domain.BotInlineResult) (domain.BotInlineMessageID, error) {
	if _, err := r.callerBotID(WithUserID(ctx, botID)); err != nil {
		return domain.BotInlineMessageID{}, err
	}
	if r.deps.BotGuestQueries == nil {
		return domain.BotInlineMessageID{}, queryIDInvalidErr()
	}
	if _, found, err := r.deps.BotGuestQueries.GetGuestQuery(ctx, botID, queryID, r.clock.Now()); err != nil {
		return domain.BotInlineMessageID{}, internalErr()
	} else if !found {
		return domain.BotInlineMessageID{}, queryIDInvalidErr()
	}
	if err := r.validateBotAPIInlineResult(ctx, botID, &result); err != nil {
		return domain.BotInlineMessageID{}, err
	}
	return r.sendGuestInlineResult(ctx, botID, queryID, result)
}

func (r *Router) onMessagesSetBotGuestChatResult(ctx context.Context, req *tg.MessagesSetBotGuestChatResultRequest) (tg.InputBotInlineMessageIDClass, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req == nil || req.QueryID == 0 {
		return nil, queryIDInvalidErr()
	}
	result, err := r.domainInlineResultFromTG(ctx, botID, req.Result)
	if err != nil {
		return nil, err
	}
	id, err := r.BotAPIAnswerGuestQuery(ctx, botID, req.QueryID, result)
	if err != nil {
		return nil, err
	}
	return &tg.InputBotInlineMessageID64{DCID: id.DCID, OwnerID: id.OwnerID, ID: id.ID, AccessHash: id.AccessHash}, nil
}

// enqueueGuestQueries runs only after a real, non-duplicate user send. A mention
// discloses exactly that message, not the user's chat history or membership.
func (r *Router) enqueueGuestQueries(ctx context.Context, callerID int64, peer domain.Peer, msg domain.Message) {
	if r.deps.BotGuestQueries == nil || r.deps.Bots == nil || r.deps.Users == nil || msg.ID <= 0 || msg.NoForwards || msg.ViaBotID != 0 || r.userIsBot(ctx, callerID) {
		return
	}
	identity, ok := r.deps.Users.(UserIdentityService)
	if !ok {
		return
	}
	var channels []domain.Channel
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return
		}
		view, err := r.deps.Channels.GetChannel(ctx, callerID, peer.ID)
		if err != nil || view.Channel.Broadcast || view.Channel.NoForwards {
			return
		}
		channels = []domain.Channel{view.Channel}
	} else if peer.Type != domain.PeerTypeUser || r.botAPICheckPrivateNoForwards(ctx, callerID, peer) != nil {
		return
	}
	usernames := extractMentionUsernames(msg.Body, 3, mentionScanBlockedSpansFromDomainEntities(msg.Body, msg.Entities))
	caller, found, err := r.deps.Users.ByID(ctx, callerID, callerID)
	if err != nil || !found {
		return
	}
	users := []domain.User{caller}
	if peer.Type == domain.PeerTypeUser {
		if u, ok, e := r.deps.Users.ByID(ctx, callerID, peer.ID); e == nil && ok {
			users = append(users, u)
		}
	}
	seen := make(map[int64]bool, len(usernames))
	for _, username := range usernames {
		bot, found, err := identity.ResolveUsername(ctx, callerID, username)
		if err != nil || !found || !bot.Bot || seen[bot.ID] {
			continue
		}
		seen[bot.ID] = true
		profile, found, err := r.deps.Bots.BotInfo(ctx, bot.ID)
		if err != nil || !found || !profile.GuestChat {
			continue
		}
		if err := r.ensureBotAccess(ctx, bot.ID, callerID); err != nil {
			continue
		}
		if blocked, err := r.peerBlocksUser(ctx, bot.ID, callerID); err != nil || blocked {
			continue
		}
		now := r.clock.Now()
		q := domain.BotGuestQuery{ID: randomNonZeroInt64(), BotUserID: bot.ID, CallerUserID: callerID, Peer: peer, MessageID: msg.ID, CreatedAt: now, ExpiresAt: now.Add(botGuestQueryTTL)}
		if err := r.deps.BotGuestQueries.CreateGuestQuery(ctx, q); err != nil {
			continue
		}
		payload := botapi.SnapshotMessage(msg, users, channels)
		if payload == nil {
			continue
		}
		payload["guest_query_id"] = strconv.FormatInt(q.ID, 10)
		r.enqueueBotAPIObject(ctx, bot.ID, domain.BotAPIUpdateKind("guest_message"), payload)
		r.pushUserMessage(ctx, bot.ID, "push bot guest query", &tg.Updates{Updates: []tg.UpdateClass{&tg.UpdateBotGuestChatQuery{QueryID: q.ID, Message: tgMessage(msg)}}, Date: int(now.Unix())})
	}
}

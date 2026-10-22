package rpc

import (
	"context"
	"errors"
	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

// messageDraftTyping runs behind messages.setTyping's authenticated peer/hash
// checks. The official stop constructor is already in td and the TL profiles;
// no unregistered Go-only stop API or custom wire constructor is necessary.
func (r *Router) messageDraftTyping(ctx context.Context, actorID int64, peer domain.Peer, threadID int, action tg.SendMessageActionClass) (handled bool, ok bool, err error) {
	var draftID int64
	var canStop, keepOnStop bool
	switch a := action.(type) {
	case *tg.SendMessageStopDraftAction:
		if a == nil || a.RandomID == 0 {
			return true, false, randomIDEmptyErr()
		}
		ok, err := r.stopMessageDraft(ctx, actorID, peer, threadID, a.RandomID)
		return true, ok, err
	case *tg.SendMessageTextDraftAction:
		draftID, canStop, keepOnStop = a.RandomID, a.CanStop, a.KeepOnStop
	case *tg.SendMessageRichMessageDraftAction:
		draftID, canStop, keepOnStop = a.RandomID, a.CanStop, a.KeepOnStop
	default:
		return false, false, nil
	}
	if r.deps.MessageDrafts == nil {
		return true, false, internalErr()
	}
	if r.userIsBot(ctx, peer.ID) {
		return true, false, tgerr.New(400, "USER_IS_BOT")
	}
	blocked, err := r.peerBlocksUser(ctx, actorID, peer.ID)
	if err != nil {
		return true, false, err
	}
	if blocked {
		return true, false, userIsBlockedErr()
	}
	viewerThreadID := threadID
	if threadID > 0 {
		viewerThreadID, err = r.privateForumPeerThread(ctx, actorID, peer.ID, threadID)
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return true, false, topicIDInvalidErr()
		}
		if err != nil {
			return true, false, err
		}
	}
	var buffer bin.Buffer
	if err := action.Encode(&buffer); err != nil {
		return true, false, internalErr()
	}
	now := r.clock.Now()
	draft := domain.MessageDraft{BotUserID: actorID, UserID: peer.ID, ThreadID: threadID, ID: draftID, CanStop: canStop, KeepOnStop: keepOnStop, Action: buffer.Buf, ExpiresAt: now.Add(domain.MessageDraftLifetime)}
	if err := r.deps.MessageDrafts.ReplaceMessageDraft(ctx, draft, now); err != nil {
		return true, false, messageDraftErr(err)
	}
	r.pushMessageDraftAction(ctx, actorID, peer.ID, viewerThreadID, action)
	return true, true, nil
}

func messageDraftErr(err error) error {
	switch {
	case errors.Is(err, domain.ErrMessageDraftStopped):
		return tgerr.New(400, "MESSAGE_GENERATION_STOPPED")
	case errors.Is(err, domain.ErrMessageDraftExpired):
		return tgerr.New(400, "DRAFT_ID_INVALID")
	case errors.Is(err, domain.ErrMessageDraftStopForbidden):
		return tgerr.New(403, "DRAFT_STOP_FORBIDDEN")
	case errors.Is(err, domain.ErrMessageDraftInvalid):
		return tgerr.New(400, "DRAFT_ID_INVALID")
	default:
		return internalErr()
	}
}

func (r *Router) stopMessageDraft(ctx context.Context, userID int64, peer domain.Peer, threadID int, draftID int64) (bool, error) {
	if r.userIsBot(ctx, userID) {
		return false, tgerr.New(400, "USER_BOT_INVALID")
	}
	if peer.Type != domain.PeerTypeUser || peer.ID == userID || !r.userIsBot(ctx, peer.ID) {
		return false, userBotRequiredErr()
	}
	if r.deps.MessageDrafts == nil {
		return false, internalErr()
	}
	blocked, err := r.peerBlocksUser(ctx, peer.ID, userID)
	if err != nil {
		return false, err
	}
	if blocked {
		return false, userIsBlockedErr()
	}
	viewerThreadID := threadID
	if threadID > 0 {
		threadID, err = r.privateForumCanonicalThread(ctx, peer.ID, userID, threadID, true)
		if errors.Is(err, domain.ErrMessageIDInvalid) {
			return false, topicIDInvalidErr()
		}
		if err != nil {
			return false, err
		}
	}
	var users []domain.User
	if r.deps.Users != nil {
		users, err = r.deps.Users.ByIDs(ctx, peer.ID, []int64{userID})
		if err != nil {
			return false, internalErr()
		}
	}
	draft, changed, err := r.deps.MessageDrafts.StopMessageDraft(ctx, peer.ID, userID, threadID, draftID, r.clock.Now())
	if err != nil {
		return false, messageDraftErr(err)
	}
	if !changed {
		return true, nil
	}
	// A stopped preview cannot be updated again under the same ID. Keeping it
	// only changes the transient rendering, never inserts an ordinary message.
	var action tg.SendMessageActionClass = &tg.SendMessageCancelAction{}
	if draft.KeepOnStop {
		buffer := bin.Buffer{Buf: draft.Action}
		kept, decodeErr := tg.DecodeSendMessageAction(&buffer)
		if decodeErr != nil {
			return false, internalErr()
		}
		switch kept := kept.(type) {
		case *tg.SendMessageTextDraftAction:
			action = &tg.SendMessageTextDraftAction{RandomID: kept.RandomID, KeepOnStop: true, Text: kept.Text}
		case *tg.SendMessageRichMessageDraftAction:
			action = &tg.SendMessageRichMessageDraftAction{RandomID: kept.RandomID, KeepOnStop: true, RichMessage: kept.RichMessage}
		default:
			return false, internalErr()
		}
	}
	r.pushMessageDraftAction(ctx, peer.ID, userID, viewerThreadID, action)
	// The native bot session receives the same accepted user action as HTTP's
	// persisted Bot API object, rather than an unrelated ordinary message.
	r.pushMessageDraftAction(ctx, userID, peer.ID, threadID, &tg.SendMessageStopDraftAction{RandomID: draftID})
	chat := botapi.SnapshotMessage(domain.Message{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: userID}}, users, nil)["chat"]
	payload := map[string]any{"chat": chat, "draft_id": draftID}
	if threadID > 0 {
		payload["message_thread_id"] = threadID
	}
	r.enqueueBotAPIObject(ctx, peer.ID, domain.BotAPIUpdateKind("stopped_message_generation"), payload)
	return true, nil
}

func (r *Router) pushMessageDraftAction(ctx context.Context, senderID, recipientID int64, threadID int, action tg.SendMessageActionClass) {
	r.pushTypingUpdate(ctx, recipientID, &tg.UpdateShort{Update: &tg.UpdateUserTyping{UserID: senderID, TopMsgID: threadID, Action: action}, Date: int(r.clock.Now().Unix())})
}

func (r *Router) finishMessageDraftAfterSend(ctx context.Context, botID, userID int64, threadID int) {
	if r.deps.MessageDrafts == nil || !r.userIsBot(ctx, botID) {
		return
	}
	if err := r.deps.MessageDrafts.FinishMessageDraft(ctx, botID, userID, threadID, r.clock.Now()); err != nil {
		r.log.Warn("finish bot message draft", zap.Int64("bot_id", botID), zap.Int64("user_id", userID), zap.Error(err))
	}
}

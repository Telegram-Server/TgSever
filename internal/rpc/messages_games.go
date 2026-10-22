package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/domain"
)

func (r *Router) onMessagesGetEmojiGameInfo(ctx context.Context) (tg.MessagesEmojiGameInfoClass, error) {
	if _, _, err := r.currentUserID(ctx); err != nil {
		return nil, internalErr()
	}
	return &tg.MessagesEmojiGameUnavailable{}, nil
}

func (r *Router) gameScoreUser(ctx context.Context, botID int64, in tg.InputUserClass) (int64, error) {
	u, found, err := r.userFromInput(ctx, botID, in)
	if err != nil {
		return 0, err
	}
	if !found {
		return 0, userIDInvalidErr()
	}
	return u.ID, nil
}
func gameScoreChatID(peer domain.Peer) int64 {
	if peer.Type == domain.PeerTypeChannel {
		return -botAPIChannelChatIDBase - peer.ID
	}
	return peer.ID
}
func tgGameHighScores(scores []domain.BotAPIGameHighScore) *tg.MessagesHighScores {
	out := &tg.MessagesHighScores{Scores: []tg.HighScore{}, Users: []tg.UserClass{}}
	for _, s := range scores {
		out.Scores = append(out.Scores, tg.HighScore{Pos: s.Position, UserID: s.User.ID, Score: s.Score})
		out.Users = append(out.Users, tgUser(s.User))
	}
	return out
}
func (r *Router) onMessagesGetGameHighScores(ctx context.Context, req *tg.MessagesGetGameHighScoresRequest) (*tg.MessagesHighScores, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, botID, req.Peer)
	if err != nil {
		return nil, err
	}
	userID, err := r.gameScoreUser(ctx, botID, req.UserID)
	if err != nil {
		return nil, err
	}
	scores, err := r.BotAPIGetGameHighScores(ctx, botID, gameScoreChatID(peer), req.ID, userID, nil)
	if err != nil {
		return nil, err
	}
	return tgGameHighScores(scores), nil
}
func (r *Router) onMessagesGetInlineGameHighScores(ctx context.Context, req *tg.MessagesGetInlineGameHighScoresRequest) (*tg.MessagesHighScores, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if err := validateInputBotInlineMessageID(req.ID); err != nil {
		return nil, err
	}
	userID, err := r.gameScoreUser(ctx, botID, req.UserID)
	if err != nil {
		return nil, err
	}
	id, ok := req.ID.(*tg.InputBotInlineMessageID64)
	if !ok {
		return nil, messageIDInvalidErr()
	}
	inline := domainInlineMessageID(id)
	scores, err := r.BotAPIGetGameHighScores(ctx, botID, 0, 0, userID, inline)
	if err != nil {
		return nil, err
	}
	return tgGameHighScores(scores), nil
}
func (r *Router) onMessagesSetGameScore(ctx context.Context, req *tg.MessagesSetGameScoreRequest) (tg.UpdatesClass, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return nil, err
	}
	if req.Score < 0 {
		return nil, scoreInvalidErr()
	}
	peer, err := r.checkedDomainPeerFromInputPeer(ctx, botID, req.Peer)
	if err != nil {
		return nil, err
	}
	userID, err := r.gameScoreUser(ctx, botID, req.UserID)
	if err != nil {
		return nil, err
	}
	msg, err := r.BotAPISetGameScore(ctx, botID, gameScoreChatID(peer), req.ID, userID, req.Score, req.Force, !req.EditMessage, nil)
	if err != nil {
		return nil, err
	}
	out := &tg.Updates{Date: int(r.clock.Now().Unix())}
	if req.EditMessage {
		out.Updates = []tg.UpdateClass{&tg.UpdateEditMessage{Message: tgMessage(msg), Pts: msg.Pts, PtsCount: 1}}
	}
	return out, nil
}
func (r *Router) onMessagesSetInlineGameScore(ctx context.Context, req *tg.MessagesSetInlineGameScoreRequest) (bool, error) {
	botID, err := r.callerBotID(ctx)
	if err != nil {
		return false, err
	}
	if err := validateInputBotInlineMessageID(req.ID); err != nil {
		return false, err
	}
	if req.Score < 0 {
		return false, scoreInvalidErr()
	}
	userID, err := r.gameScoreUser(ctx, botID, req.UserID)
	if err != nil {
		return false, err
	}
	id, ok := req.ID.(*tg.InputBotInlineMessageID64)
	if !ok {
		return false, messageIDInvalidErr()
	}
	inline := domainInlineMessageID(id)
	_, err = r.BotAPISetGameScore(ctx, botID, 0, 0, userID, req.Score, req.Force, !req.EditMessage, inline)
	return err == nil, err
}

func validateInputBotInlineMessageID(id tg.InputBotInlineMessageIDClass) error {
	switch v := id.(type) {
	case *tg.InputBotInlineMessageID:
		if v.ID == 0 || v.AccessHash == 0 {
			return messageIDInvalidErr()
		}
	case *tg.InputBotInlineMessageID64:
		if v.OwnerID == 0 || v.ID <= 0 || v.AccessHash == 0 {
			return messageIDInvalidErr()
		}
	default:
		return messageIDInvalidErr()
	}
	return nil
}

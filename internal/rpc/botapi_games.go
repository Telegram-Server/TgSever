package rpc

import (
	"context"
	"errors"
	"fmt"
	"github.com/iamxvbaba/td/tg"
	"strconv"
	"strings"
	"telesrv/internal/domain"
)

func (r *Router) resolveBotAPIInlineGame(ctx context.Context, botID int64, name string) (*domain.MessageMedia, error) {
	if r.deps.GamesPassport == nil {
		return nil, errors.New("GAME_SHORTNAME_INVALID")
	}
	game, found, err := r.deps.GamesPassport.GetBotGame(ctx, botID, name)
	if err != nil {
		return nil, err
	}
	if !found {
		return nil, errors.New("GAME_SHORTNAME_INVALID")
	}
	return &domain.MessageMedia{Kind: domain.MessageMediaKindGame, Game: &game}, nil
}
func gameReplyMarkup(g domain.BotGame, markup *domain.MessageReplyMarkup) (*domain.MessageReplyMarkup, error) {
	if markup == nil || markup.IsZero() {
		markup = &domain.MessageReplyMarkup{Type: domain.MessageReplyMarkupInline, Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonGame, Text: "Play " + g.Title}}}}
	}
	if markup.Kind() != domain.MessageReplyMarkupInline || len(markup.Inline) == 0 || len(markup.Inline[0]) == 0 || markup.Inline[0][0].Type != domain.MarkupButtonGame {
		return nil, errors.New("BUTTON_TYPE_INVALID")
	}
	if err := domain.ValidateReplyMarkup(markup); err != nil {
		return nil, replyMarkupErr(err)
	}
	return markup, nil
}
func (r *Router) BotAPISendGame(ctx context.Context, botID int64, in domain.BotAPISendGameInput) (domain.Message, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return domain.Message{}, err
	}
	media, err := r.resolveBotAPIInlineGame(ctx, botID, in.ShortName)
	if err != nil {
		return domain.Message{}, err
	}
	markup, err := gameReplyMarkup(*media.Game, in.ReplyMarkup)
	if err != nil {
		return domain.Message{}, err
	}
	peer, ok := botAPIPeerFromChatID(in.ChatID)
	if !ok {
		return domain.Message{}, errors.New("CHAT_ID_INVALID")
	}
	if in.Options.EffectID != 0 && (peer.Type != domain.PeerTypeUser || r.messageEffectInvalid(ctx, in.Options.EffectID)) {
		return domain.Message{}, effectIDInvalidErr()
	}
	if peer.Type == domain.PeerTypeChannel {
		if r.deps.Channels == nil {
			return domain.Message{}, errors.New("CHAT_ID_INVALID")
		}
		view, e := r.deps.Channels.ResolveChannel(ctx, botID, peer.ID)
		if e != nil {
			return domain.Message{}, e
		}
		if !view.Channel.Megagroup || view.Channel.Monoforum {
			return domain.Message{}, errors.New("CHAT_SEND_GAMES_FORBIDDEN")
		}
	}
	return r.botAPISendContent(ctx, botID, in.ChatID, "", nil, media, nil, markup, in.Silent, in.Options.ProtectContent, in.ReplyTo, in.Options.EffectID, in.Options)
}

type gameMessageTarget struct {
	message domain.Message
	scope   domain.GameScoreScope
	inline  *domain.BotInlineMessageID
}

func (r *Router) gameMessageTarget(ctx context.Context, botID, chatID int64, messageID int, inline *domain.BotInlineMessageID) (gameMessageTarget, error) {
	if _, err := r.BotAPISelf(ctx, botID); err != nil {
		return gameMessageTarget{}, err
	}
	if r.deps.GamesPassport == nil {
		return gameMessageTarget{}, messageIDInvalidErr()
	}
	var msg domain.Message
	if inline != nil {
		id := tgInputBotInlineMessageID(*inline)
		private, found, err := r.privateMessageFromInlineID(ctx, botID, id)
		if err != nil {
			return gameMessageTarget{}, err
		}
		if found {
			msg = private
		} else {
			_, cm, found, err := r.channelMessageFromInlineID(ctx, botID, id)
			if err != nil {
				return gameMessageTarget{}, err
			}
			if !found {
				return gameMessageTarget{}, messageIDInvalidErr()
			}
			msg = botAPIMessageFromChannel(botID, cm)
		}
	} else {
		peer, ok := botAPIPeerFromChatID(chatID)
		if !ok || messageID <= 0 {
			return gameMessageTarget{}, messageIDInvalidErr()
		}
		if peer.Type == domain.PeerTypeUser {
			var err error
			msg, err = r.privateGameMessage(ctx, botID, peer, messageID)
			if err != nil {
				return gameMessageTarget{}, err
			}
		} else {
			if r.deps.Channels == nil {
				return gameMessageTarget{}, messageIDInvalidErr()
			}
			h, err := r.deps.Channels.GetMessages(ctx, botID, peer.ID, []int{messageID})
			if err != nil {
				return gameMessageTarget{}, err
			}
			if len(h.Messages) != 1 || h.Messages[0].SenderUserID != botID {
				return gameMessageTarget{}, messageIDInvalidErr()
			}
			msg = botAPIMessageFromChannel(botID, h.Messages[0])
		}
	}
	if msg.Media == nil || msg.Media.Game == nil || msg.Media.Game.BotUserID != botID {
		return gameMessageTarget{}, errors.New("BOT_SCORE_FORBIDDEN")
	}
	scope := domain.GameScoreScope{GameID: msg.Media.Game.ID, ChatID: chatID}
	if inline != nil {
		scope.ChatID = 0
		scope.InlineOwnerID = inline.OwnerID
		scope.InlineMessageID = inline.ID
	} else if msg.BusinessBotID != 0 {
		scope.InlineOwnerID = -msg.OwnerUserID
	}
	return gameMessageTarget{message: msg, scope: scope, inline: inline}, nil
}
func (r *Router) BotAPISetGameScore(ctx context.Context, botID, chatID int64, messageID int, userID int64, score int, force, disableEdit bool, inline *domain.BotInlineMessageID) (domain.Message, error) {
	if score < 0 || int64(score) > 2147483647 {
		return domain.Message{}, scoreInvalidErr()
	}
	target, err := r.gameMessageTarget(ctx, botID, chatID, messageID, inline)
	if err != nil {
		return domain.Message{}, err
	}
	u, found, err := r.deps.Users.ByID(ctx, botID, userID)
	if err != nil {
		return domain.Message{}, err
	}
	if !found || u.Deleted || u.Bot {
		return domain.Message{}, errors.New("USER_ID_INVALID")
	}
	if err = r.deps.GamesPassport.SetBotGameScore(ctx, target.scope, userID, score, force); err != nil {
		return domain.Message{}, err
	}
	if disableEdit {
		return target.message, nil
	}
	scores, err := r.deps.GamesPassport.BotGameScores(ctx, target.scope)
	if err != nil {
		return domain.Message{}, err
	}
	var text strings.Builder
	text.WriteString(target.message.Media.Game.Title)
	for _, s := range scores {
		if s.Position > 10 {
			break
		}
		player, found, e := r.deps.Users.ByID(ctx, botID, s.UserID)
		if e != nil {
			return domain.Message{}, e
		}
		name := strconv.FormatInt(s.UserID, 10)
		if found {
			name = strings.TrimSpace(player.FirstName + " " + player.LastName)
		}
		fmt.Fprintf(&text, "\n%d. %s — %d", s.Position, name, s.Score)
	}
	if text.String() == target.message.Body {
		return target.message, nil
	}
	if inline != nil {
		_, err = r.BotAPIEditInlineMessageText(ctx, botID, *inline, text.String(), nil, false, nil, false)
		return target.message, err
	}
	return r.BotAPIEditMessageTextWithOptions(ctx, botID, chatID, messageID, text.String(), nil, false, nil, false, domain.BotAPIMessageOptions{BusinessConnectionID: target.message.BusinessConnectionID})
}
func (r *Router) BotAPIGetGameHighScores(ctx context.Context, botID, chatID int64, messageID int, userID int64, inline *domain.BotInlineMessageID) ([]domain.BotAPIGameHighScore, error) {
	target, err := r.gameMessageTarget(ctx, botID, chatID, messageID, inline)
	if err != nil {
		return nil, err
	}
	if _, found, err := r.deps.Users.ByID(ctx, botID, userID); err != nil {
		return nil, err
	} else if !found {
		return nil, errors.New("USER_ID_INVALID")
	}
	scores, err := r.deps.GamesPassport.BotGameScores(ctx, target.scope)
	if err != nil {
		return nil, err
	}
	out := make([]domain.BotAPIGameHighScore, 0, 8)
	for _, s := range domain.GameHighScoreWindow(scores, userID) {
		u, found, err := r.deps.Users.ByID(ctx, botID, s.UserID)
		if err != nil {
			return nil, err
		}
		if found {
			out = append(out, domain.BotAPIGameHighScore{Position: s.Position, User: u, Score: s.Score})
		}
	}
	return out, nil
}

func (r *Router) resolveInputGame(ctx context.Context, userID int64, input tg.InputGameClass) (*domain.MessageMedia, error) {
	if !r.userIsBot(ctx, userID) || r.deps.GamesPassport == nil {
		return nil, botInvalidErr()
	}
	switch in := input.(type) {
	case *tg.InputGameShortName:
		bot, found, err := r.userFromInput(ctx, userID, in.BotID)
		if err != nil {
			return nil, err
		}
		if !found || bot.ID != userID {
			return nil, botInvalidErr()
		}
		return r.resolveBotAPIInlineGame(ctx, userID, in.ShortName)
	case *tg.InputGameID:
		game, found, err := r.deps.GamesPassport.GetBotGameByID(ctx, in.ID)
		if err != nil {
			return nil, err
		}
		if !found || game.BotUserID != userID || game.AccessHash != in.AccessHash {
			return nil, mediaInvalidErr()
		}
		return &domain.MessageMedia{Kind: domain.MessageMediaKindGame, Game: &game}, nil
	default:
		return nil, mediaInvalidErr()
	}
}

func (r *Router) privateGameMessage(ctx context.Context, botID int64, peer domain.Peer, id int) (domain.Message, error) {
	own, found, err := r.lookupOwnerMessage(ctx, botID, id)
	if err != nil {
		return domain.Message{}, err
	}
	validOwn := found && own.Peer == peer && own.Out && own.Media != nil && own.Media.Game != nil && own.Media.Game.BotUserID == botID
	refs, err := r.deps.GamesPassport.BusinessGameMessageRefs(ctx, botID, peer.ID, id)
	if err != nil {
		return domain.Message{}, err
	}
	if len(refs) == 0 {
		if validOwn {
			return own, nil
		}
		return domain.Message{}, messageIDInvalidErr()
	}
	if validOwn || len(refs) != 1 {
		return domain.Message{}, messageIDInvalidErr()
	}
	ref := refs[0]
	owner, err := r.botAPIBusinessPeer(ctx, botID, ref.ConnectionID, "can_reply", peer)
	if err != nil {
		return domain.Message{}, err
	}
	if owner != ref.OwnerUserID {
		return domain.Message{}, messageIDInvalidErr()
	}
	msg, found, err := r.lookupOwnerMessage(ctx, owner, id)
	if err != nil {
		return domain.Message{}, err
	}
	if !found || msg.Peer != peer || !msg.Out || msg.BusinessBotID != botID || msg.BusinessConnectionID != ref.ConnectionID {
		return domain.Message{}, messageIDInvalidErr()
	}
	return msg, nil
}

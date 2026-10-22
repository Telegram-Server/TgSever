package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"telesrv/internal/domain"
)

type GamesPassportGateway interface {
	BotAPISendGame(context.Context, int64, domain.BotAPISendGameInput) (domain.Message, error)
	BotAPISetGameScore(context.Context, int64, int64, int, int64, int, bool, bool, *domain.BotInlineMessageID) (domain.Message, error)
	BotAPIGetGameHighScores(context.Context, int64, int64, int, int64, *domain.BotInlineMessageID) ([]domain.BotAPIGameHighScore, error)
	BotAPISetPassportDataErrors(context.Context, int64, int64, []domain.PassportError) (bool, error)
}

func init() {
	registerMethods(map[string]methodFunc{"sendGame": (*handler).sendGame, "setGameScore": (*handler).setGameScore, "getGameHighScores": (*handler).getGameHighScores, "setPassportDataErrors": (*handler).setPassportDataErrors})
	registerErrorMarkers("GAME_SHORTNAME_INVALID", "GAME_SHORTNAME_OCCUPIED", "SCORE_NOT_MODIFIED", "SCORE_INVALID", "PASSPORT_DATA_INVALID", "SECURE_VALUE_INVALID", "BOT_SCORE_FORBIDDEN", "CHAT_SEND_GAMES_FORBIDDEN")
	registerMessageProjection(projectGame)
}
func (h *handler) gamesPassportGateway(w http.ResponseWriter) (GamesPassportGateway, bool) {
	g, ok := h.gateway.(GamesPassportGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
	}
	return g, ok
}
func (h *handler) sendGame(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gamesPassportGateway(w)
	if !ok {
		return
	}
	v, e := requestValues(r)
	if e != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return
	}
	c, desc := h.parseSendCommon(r.Context(), botID, v)
	if desc != "" {
		writeAPIError(w, 400, desc)
		return
	}
	options, e := botAPIMessageOptions(v)
	if e != nil {
		writeAPIError(w, 400, apiErrorDescription(e))
		return
	}
	msg, e := g.BotAPISendGame(r.Context(), botID, domain.BotAPISendGameInput{ChatID: c.chatID, ShortName: v["game_short_name"], ReplyMarkup: c.markup, Silent: c.silent, ReplyTo: c.replyTo, Options: options})
	if e != nil {
		writeAPIError(w, 400, apiErrorDescription(e))
		return
	}
	h.writeBotMessages(w, r, botID, []domain.Message{msg}, false)
}
func (h *handler) setGameScore(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gamesPassportGateway(w)
	if !ok {
		return
	}
	v, e := requestValues(r)
	if e != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return
	}
	target, desc := h.parseEditTarget(r.Context(), botID, v)
	if desc != "" {
		writeAPIError(w, 400, desc)
		return
	}
	user, e := strconv.ParseInt(v["user_id"], 10, 64)
	score, se := strconv.ParseInt(v["score"], 10, 32)
	if e != nil || user <= 0 {
		writeAPIError(w, 400, "USER_ID_INVALID")
		return
	}
	if se != nil || score < 0 {
		writeAPIError(w, 400, "SCORE_INVALID")
		return
	}
	var inline *domain.BotInlineMessageID
	if target.inline {
		inline = &target.inlineID
	}
	msg, e := g.BotAPISetGameScore(r.Context(), botID, target.chatID, target.messageID, user, int(score), apiBool(v["force"]), apiBool(v["disable_edit_message"]), inline)
	if e != nil {
		writeAPIError(w, 400, apiErrorDescription(e))
		return
	}
	if inline != nil {
		writeAPIOK(w, true)
	} else {
		h.writeBotMessages(w, r, botID, []domain.Message{msg}, false)
	}
}
func (h *handler) getGameHighScores(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gamesPassportGateway(w)
	if !ok {
		return
	}
	v, e := requestValues(r)
	if e != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return
	}
	target, desc := h.parseEditTarget(r.Context(), botID, v)
	if desc != "" {
		writeAPIError(w, 400, desc)
		return
	}
	user, e := strconv.ParseInt(v["user_id"], 10, 64)
	if e != nil || user <= 0 {
		writeAPIError(w, 400, "USER_ID_INVALID")
		return
	}
	var inline *domain.BotInlineMessageID
	if target.inline {
		inline = &target.inlineID
	}
	scores, e := g.BotAPIGetGameHighScores(r.Context(), botID, target.chatID, target.messageID, user, inline)
	if e != nil {
		writeAPIError(w, 400, apiErrorDescription(e))
		return
	}
	out := make([]map[string]any, 0, len(scores))
	for _, s := range scores {
		out = append(out, map[string]any{"position": s.Position, "user": apiUser(s.User), "score": s.Score})
	}
	writeAPIOK(w, out)
}
func (h *handler) setPassportDataErrors(w http.ResponseWriter, r *http.Request, botID int64) {
	g, ok := h.gamesPassportGateway(w)
	if !ok {
		return
	}
	v, e := requestValues(r)
	if e != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return
	}
	user, e := strconv.ParseInt(v["user_id"], 10, 64)
	if e != nil || user <= 0 {
		writeAPIError(w, 400, "USER_ID_INVALID")
		return
	}
	var errs []domain.PassportError
	if json.Unmarshal([]byte(v["errors"]), &errs) != nil || len(errs) == 0 {
		writeAPIError(w, 400, "PASSPORT_DATA_INVALID")
		return
	}
	result, e := g.BotAPISetPassportDataErrors(r.Context(), botID, user, errs)
	if e != nil {
		writeAPIError(w, 400, apiErrorDescription(e))
		return
	}
	writeAPIOK(w, result)
}
func projectGame(msg domain.Message, out map[string]any) {
	if msg.Media == nil || msg.Media.Game == nil {
		return
	}
	g := msg.Media.Game
	game := map[string]any{"title": g.Title, "description": g.Description, "photo": apiPhotoSizes(g.Photo)}
	if msg.Body != "" {
		game["text"] = msg.Body
		if len(msg.Entities) > 0 {
			game["text_entities"] = apiMessageEntities(msg.Entities, nil)
		}
	}
	if g.Animation != nil {
		game["animation"] = apiDocumentMedia(*g.Animation)["animation"]
	}
	out["game"] = game
	delete(out, "text")
	delete(out, "entities")
}

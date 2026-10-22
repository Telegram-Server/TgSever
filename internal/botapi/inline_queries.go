package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

type InlineQueryGateway interface {
	BotAPIAnswerInlineQuery(context.Context, int64, int64, domain.BotInlineResults) error
	BotAPIAnswerGuestQuery(context.Context, int64, int64, domain.BotInlineResult) (domain.BotInlineMessageID, error)
	BotAPISavePreparedKeyboardButton(context.Context, int64, int64, domain.BotRequestedWebViewButton) (string, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"answerInlineQuery":          (*handler).answerInlineQuery,
		"answerGuestQuery":           (*handler).answerGuestQuery,
		"savePreparedKeyboardButton": (*handler).savePreparedKeyboardButton,
	})
	registerErrorMarkers("QUERY_ID_INVALID", "RESULT_ID_DUPLICATE", "RESULTS_TOO_MUCH", "NEXT_OFFSET_INVALID", "CACHE_TIME_INVALID")
}

func (h *handler) inlineQueryRequest(w http.ResponseWriter, r *http.Request) (InlineQueryGateway, map[string]string, bool) {
	gateway, ok := h.gateway.(InlineQueryGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return nil, nil, false
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return nil, nil, false
	}
	return gateway, values, true
}

func (h *handler) answerInlineQuery(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.inlineQueryRequest(w, r)
	if !ok {
		return
	}
	queryID, err := strconv.ParseInt(values["inline_query_id"], 10, 64)
	if err != nil || queryID == 0 {
		writeAPIError(w, 400, "QUERY_ID_INVALID")
		return
	}
	var raw []json.RawMessage
	if json.Unmarshal([]byte(values["results"]), &raw) != nil || raw == nil {
		writeAPIError(w, 400, "RESULT_TYPE_INVALID")
		return
	}
	if len(raw) > domain.MaxBotInlineResults {
		writeAPIError(w, 400, "RESULTS_TOO_MUCH")
		return
	}
	out := domain.BotInlineResults{CacheTime: 300, Private: apiBool(values["is_personal"]), NextOffset: values["next_offset"], Results: make([]domain.BotInlineResult, 0, len(raw))}
	if v, exists := values["cache_time"]; exists {
		out.CacheTime, err = strconv.Atoi(v)
		if err != nil || out.CacheTime < 0 {
			writeAPIError(w, 400, "CACHE_TIME_INVALID")
			return
		}
	}
	if len(out.NextOffset) > domain.MaxBotInlineNextOffsetLen {
		writeAPIError(w, 400, "NEXT_OFFSET_INVALID")
		return
	}
	seen := make(map[string]bool, len(raw))
	for _, item := range raw {
		result, err := inlineResultFromAPI(string(item))
		if err != nil {
			writeAPIError(w, 400, err.Error())
			return
		}
		if seen[result.ID] {
			writeAPIError(w, 400, "RESULT_ID_DUPLICATE")
			return
		}
		seen[result.ID] = true
		out.Results = append(out.Results, result)
	}
	if text := values["button"]; text != "" {
		var button struct {
			Text           string `json:"text"`
			StartParameter string `json:"start_parameter"`
			WebApp         *struct {
				URL string `json:"url"`
			} `json:"web_app"`
		}
		if json.Unmarshal([]byte(text), &button) != nil || strings.TrimSpace(button.Text) == "" || ((button.StartParameter != "") == (button.WebApp != nil)) {
			writeAPIError(w, 400, "BUTTON_INVALID")
			return
		}
		if button.WebApp != nil {
			if !validBotAPIHTTPSURL(button.WebApp.URL) {
				writeAPIError(w, 400, "BUTTON_URL_INVALID")
				return
			}
			out.SwitchWeb = &domain.BotInlineSwitchWebView{Text: button.Text, URL: button.WebApp.URL}
		} else {
			out.SwitchPM = &domain.BotInlineSwitchPM{Text: button.Text, StartParam: button.StartParameter}
		}
	}
	if err := gateway.BotAPIAnswerInlineQuery(r.Context(), botID, queryID, out); err != nil {
		writeAPIError(w, 400, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) answerGuestQuery(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.inlineQueryRequest(w, r)
	if !ok {
		return
	}
	queryID, err := strconv.ParseInt(values["guest_query_id"], 10, 64)
	if err != nil || queryID == 0 {
		writeAPIError(w, 400, "QUERY_ID_INVALID")
		return
	}
	result, err := inlineResultFromAPI(values["result"])
	if err != nil {
		writeAPIError(w, 400, err.Error())
		return
	}
	id, err := gateway.BotAPIAnswerGuestQuery(r.Context(), botID, queryID, result)
	if err != nil {
		writeAPIError(w, 400, apiErrorDescription(err))
		return
	}
	encoded, ok := encodeBotAPIInlineMessageID(id)
	if !ok {
		writeAPIError(w, 500, "INTERNAL")
		return
	}
	writeAPIOK(w, map[string]any{"inline_message_id": encoded})
}

func (h *handler) savePreparedKeyboardButton(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, values, ok := h.inlineQueryRequest(w, r)
	if !ok {
		return
	}
	userID, err := strconv.ParseInt(values["user_id"], 10, 64)
	if err != nil || userID <= 0 {
		writeAPIError(w, 400, "USER_ID_INVALID")
		return
	}
	button, err := preparedKeyboardButtonFromAPI(values["button"])
	if err != nil {
		writeAPIError(w, 400, apiErrorDescription(err))
		return
	}
	id, err := gateway.BotAPISavePreparedKeyboardButton(r.Context(), botID, userID, button)
	if err != nil {
		writeAPIError(w, 400, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, map[string]any{"id": id})
}

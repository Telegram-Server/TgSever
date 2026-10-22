package botapi

import (
	"context"
	"crypto/rand"
	"errors"
	"net/http"
	"strconv"
	"telesrv/internal/domain"
	"time"
)

type botInstanceService interface {
	AdmitBotAPIInstance(context.Context, domain.BotAPIInstanceKey, bool, time.Time) (domain.BotAPIInstance, error)
	StopBotAPIInstance(context.Context, domain.BotAPIInstanceKey, bool, time.Time) error
}

func newBotAPIInstanceID() string { return rand.Text() }
func init() {
	registerMethods(map[string]methodFunc{"logOut": (*handler).logOutInstance, "close": (*handler).closeInstance})
}
func writeBotInstanceError(w http.ResponseWriter, err error) {
	var wait *domain.BotAPIInstanceWaitError
	if errors.As(err, &wait) {
		w.Header().Set("Retry-After", strconv.Itoa(wait.Seconds))
		writeJSON(w, 429, map[string]any{"ok": false, "error_code": 429, "description": "Too Many Requests: retry after " + strconv.Itoa(wait.Seconds), "parameters": map[string]any{"retry_after": wait.Seconds}})
		return
	}
	if errors.Is(err, domain.ErrBotInstanceClosed) {
		writeAPIError(w, 409, "BOT_INSTANCE_CLOSED")
		return
	}
	writeAPIError(w, 500, "INTERNAL_SERVER_ERROR")
}
func (h *handler) admitBotInstance(w http.ResponseWriter, r *http.Request, botID int64) bool {
	service, ok := h.bots.(botInstanceService)
	if !ok {
		return true
	}
	if h.instanceID == "" {
		writeAPIError(w, 500, "INTERNAL_SERVER_ERROR")
		return false
	}
	_, err := service.AdmitBotAPIInstance(r.Context(), domain.BotAPIInstanceKey{BotUserID: botID, InstanceID: h.instanceID}, h.cloud, time.Now())
	if err != nil {
		writeBotInstanceError(w, err)
		return false
	}
	return true
}
func (h *handler) stopBotInstance(w http.ResponseWriter, r *http.Request, botID int64, logout bool) {
	service, ok := h.bots.(botInstanceService)
	if !ok {
		writeAPIError(w, 501, "METHOD_NOT_FOUND")
		return
	}
	if err := service.StopBotAPIInstance(r.Context(), domain.BotAPIInstanceKey{BotUserID: botID, InstanceID: h.instanceID}, logout, time.Now()); err != nil {
		writeBotInstanceError(w, err)
		return
	}
	writeAPIOK(w, true)
}
func (h *handler) logOutInstance(w http.ResponseWriter, r *http.Request, botID int64) {
	h.stopBotInstance(w, r, botID, true)
}
func (h *handler) closeInstance(w http.ResponseWriter, r *http.Request, botID int64) {
	h.stopBotInstance(w, r, botID, false)
}
func (h *handler) admitWebhookInstance(ctx context.Context, botID int64) error {
	service, ok := h.bots.(botInstanceService)
	if !ok {
		return nil
	}
	if h.instanceID == "" {
		return domain.ErrBotInstanceClosed
	}
	_, err := service.AdmitBotAPIInstance(ctx, domain.BotAPIInstanceKey{BotUserID: botID, InstanceID: h.instanceID}, h.cloud, time.Now())
	return err
}

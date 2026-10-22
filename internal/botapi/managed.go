package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"telesrv/internal/domain"
)

type ManagedBotsGateway interface {
	BotAPIManagedBotToken(context.Context, int64, int64, bool) (string, error)
	BotAPIGetManagedBotAccessSettings(context.Context, int64, int64) (domain.BotManagement, []domain.User, error)
	BotAPISetManagedBotAccessSettings(context.Context, int64, int64, bool, []int64) error
}

func init() {
	registerMethods(map[string]methodFunc{
		"getManagedBotToken":          func(h *handler, w http.ResponseWriter, r *http.Request, id int64) { h.managedBotToken(w, r, id, false) },
		"replaceManagedBotToken":      func(h *handler, w http.ResponseWriter, r *http.Request, id int64) { h.managedBotToken(w, r, id, true) },
		"getManagedBotAccessSettings": (*handler).getManagedBotAccessSettings,
		"setManagedBotAccessSettings": (*handler).setManagedBotAccessSettings,
	})
}
func (h *handler) managedRequest(w http.ResponseWriter, r *http.Request) (ManagedBotsGateway, map[string]string, int64, bool) {
	g, ok := h.gateway.(ManagedBotsGateway)
	if !ok {
		writeAPIError(w, 501, "METHOD_NOT_FOUND")
		return nil, nil, 0, false
	}
	v, err := requestValues(r)
	if err != nil {
		writeAPIError(w, 400, "BAD_REQUEST")
		return nil, nil, 0, false
	}
	id, err := profileUserID(v)
	if err != nil {
		profileError(w, err)
		return nil, nil, 0, false
	}
	return g, v, id, true
}
func (h *handler) managedBotToken(w http.ResponseWriter, r *http.Request, botID int64, revoke bool) {
	g, _, id, ok := h.managedRequest(w, r)
	if !ok {
		return
	}
	token, err := g.BotAPIManagedBotToken(r.Context(), botID, id, revoke)
	if err != nil {
		profileError(w, err)
		return
	}
	writeAPIOK(w, token)
}
func (h *handler) getManagedBotAccessSettings(w http.ResponseWriter, r *http.Request, botID int64) {
	g, _, id, ok := h.managedRequest(w, r)
	if !ok {
		return
	}
	m, users, err := g.BotAPIGetManagedBotAccessSettings(r.Context(), botID, id)
	if err != nil {
		profileError(w, err)
		return
	}
	out := map[string]any{"is_access_restricted": m.IsAccessRestricted}
	if m.IsAccessRestricted {
		added := make([]map[string]any, 0, len(users))
		for _, u := range users {
			added = append(added, apiUser(u))
		}
		out["added_users"] = added
	}
	writeAPIOK(w, out)
}
func (h *handler) setManagedBotAccessSettings(w http.ResponseWriter, r *http.Request, botID int64) {
	g, v, id, ok := h.managedRequest(w, r)
	if !ok {
		return
	}
	if v["is_access_restricted"] == "" {
		writeAPIError(w, 400, "BAD_REQUEST")
		return
	}
	restricted, err := profileBool(v["is_access_restricted"])
	if err != nil {
		profileError(w, err)
		return
	}
	var added []int64
	if restricted && v["added_user_ids"] != "" {
		if json.Unmarshal([]byte(v["added_user_ids"]), &added) != nil || len(added) > 10 {
			writeAPIError(w, 400, "USER_ID_INVALID")
			return
		}
		for _, uid := range added {
			if uid <= 0 {
				writeAPIError(w, 400, "USER_ID_INVALID")
				return
			}
		}
	}
	if err = g.BotAPISetManagedBotAccessSettings(r.Context(), botID, id, restricted, added); err != nil {
		profileError(w, err)
		return
	}
	writeAPIOK(w, true)
}

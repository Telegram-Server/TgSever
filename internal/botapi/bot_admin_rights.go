package botapi

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"

	"telesrv/internal/domain"
)

// BotDefaultAdminRightsGateway 读写机器人被加为群 / 频道管理员时建议的默认权限，
// 对应 MTProto bots.setBotGroupDefaultAdminRights / setBotBroadcastDefaultAdminRights
// 与 userFull.bot_group_admin_rights / bot_broadcast_admin_rights。
// 可选接口：网关不实现时这组方法回 501 METHOD_NOT_FOUND。
type BotDefaultAdminRightsGateway interface {
	// BotAPISetMyDefaultAdministratorRights 写入默认权限，零值即清空；与当前值相同也算成功。
	BotAPISetMyDefaultAdministratorRights(ctx context.Context, botID int64, forChannels bool, rights domain.ChannelAdminRights) error
	// BotAPIGetMyDefaultAdministratorRights 读回默认权限；未设置时返回零值。
	BotAPIGetMyDefaultAdministratorRights(ctx context.Context, botID int64, forChannels bool) (domain.ChannelAdminRights, error)
}

func init() {
	registerMethods(map[string]methodFunc{
		"setMyDefaultAdministratorRights": (*handler).setMyDefaultAdministratorRights,
		"getMyDefaultAdministratorRights": (*handler).getMyDefaultAdministratorRights,
	})
}

// parseAdministratorRights 解析官方 ChatAdministratorRights 对象：缺省字段为 false，
// 不认识的字段忽略；已知字段不是布尔值时报错。空值或 null 表示不传，即全部为 false。
func parseAdministratorRights(raw string) (domain.ChannelAdminRights, error) {
	var rights domain.ChannelAdminRights
	raw = strings.TrimSpace(raw)
	if raw == "" || raw == "null" {
		return rights, nil
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(raw), &fields); err != nil {
		return domain.ChannelAdminRights{}, err
	}
	for _, field := range adminRightFields {
		value, ok := fields[field.name]
		if !ok {
			continue
		}
		if err := json.Unmarshal(value, field.get(&rights)); err != nil {
			return domain.ChannelAdminRights{}, err
		}
	}
	return rights, nil
}

func (h *handler) setMyDefaultAdministratorRights(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BotDefaultAdminRightsGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	rights, err := parseAdministratorRights(values["rights"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	if err := gateway.BotAPISetMyDefaultAdministratorRights(r.Context(), botID, apiBool(values["for_channels"]), rights); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func (h *handler) getMyDefaultAdministratorRights(w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BotDefaultAdminRightsGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	rights, err := gateway.BotAPIGetMyDefaultAdministratorRights(r.Context(), botID, apiBool(values["for_channels"]))
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, apiAdministratorRights(rights))
}

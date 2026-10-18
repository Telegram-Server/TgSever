package botapi

import (
	"context"
	"net/http"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// BotProfileGateway 读写机器人自己的 name / description / short description，
// 对应 MTProto bots.setBotInfo / bots.getBotInfo（short description 即 about）。
// 可选接口：网关不实现时这组方法回 501 METHOD_NOT_FOUND。
type BotProfileGateway interface {
	BotAPISetBotInfo(ctx context.Context, botID int64, languageCode string, upd domain.BotInfoUpdate) (bool, error)
	BotAPIGetBotInfo(ctx context.Context, botID int64, languageCode string) (name, about, description string, err error)
}

// botInfoField 描述 bots.setBotInfo 的一个字段在 Bot API 里的参数名、长度上限
// 与读写方式；三对 setMy*/getMy* 方法只差在这里。
type botInfoField struct {
	param   string // Bot API 参数名，也是 get 返回对象的字段名
	maxLen  int
	tooLong string
	set     func(upd *domain.BotInfoUpdate, value string)
	pick    func(name, about, description string) string
}

var (
	botNameField = botInfoField{
		param: "name", maxLen: domain.MaxBotNameLength, tooLong: "NAME_TOO_LONG",
		set:  func(upd *domain.BotInfoUpdate, v string) { upd.SetName, upd.Name = true, v },
		pick: func(name, _, _ string) string { return name },
	}
	botDescriptionField = botInfoField{
		param: "description", maxLen: domain.MaxBotDescriptionLen, tooLong: "DESCRIPTION_TOO_LONG",
		set:  func(upd *domain.BotInfoUpdate, v string) { upd.SetDescription, upd.Description = true, v },
		pick: func(_, _, description string) string { return description },
	}
	// short description 就是 MTProto 的 about。
	botShortDescriptionField = botInfoField{
		param: "short_description", maxLen: domain.MaxBotAboutLen, tooLong: "ABOUT_TOO_LONG",
		set:  func(upd *domain.BotInfoUpdate, v string) { upd.SetAbout, upd.About = true, v },
		pick: func(_, about, _ string) string { return about },
	}
)

func init() {
	registerMethods(map[string]methodFunc{
		"setMyName":        botNameField.setMethod,
		"getMyName":        botNameField.getMethod,
		"setMyDescription": botDescriptionField.setMethod,
		"getMyDescription": botDescriptionField.getMethod,

		"setMyShortDescription": botShortDescriptionField.setMethod,
		"getMyShortDescription": botShortDescriptionField.getMethod,
	})
}

func (f botInfoField) setMethod(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BotProfileGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	// 本服只存默认语言的资料；bots.setBotInfo 对非空 lang_code 静默丢弃，这里明确报错。
	if strings.TrimSpace(values["language_code"]) != "" {
		writeAPIError(w, http.StatusBadRequest, "LANG_CODE_NOT_SUPPORTED")
		return
	}
	value := values[f.param]
	if utf8.RuneCountInString(value) > f.maxLen {
		writeAPIError(w, http.StatusBadRequest, f.tooLong)
		return
	}
	var upd domain.BotInfoUpdate
	f.set(&upd, value)
	if _, err := gateway.BotAPISetBotInfo(r.Context(), botID, "", upd); err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, true)
}

func (f botInfoField) getMethod(h *handler, w http.ResponseWriter, r *http.Request, botID int64) {
	gateway, ok := h.gateway.(BotProfileGateway)
	if !ok {
		writeAPIError(w, http.StatusNotImplemented, "METHOD_NOT_FOUND")
		return
	}
	values, err := requestValues(r)
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, "BAD_REQUEST")
		return
	}
	name, about, description, err := gateway.BotAPIGetBotInfo(r.Context(), botID, values["language_code"])
	if err != nil {
		writeAPIError(w, http.StatusBadRequest, apiErrorDescription(err))
		return
	}
	writeAPIOK(w, map[string]any{f.param: f.pick(name, about, description)})
}

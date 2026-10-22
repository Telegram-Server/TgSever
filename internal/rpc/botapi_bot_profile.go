package rpc

import (
	"context"
	"errors"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

// BotAPISetBotInfo 是 Bot API setMyName / setMyDescription / setMyShortDescription
// 的网关：组装 bots.setBotInfo（不带 bot 参数，即机器人改自己），校验与资料缓存
// 失效都继承 MTProto 处理器。
func (r *Router) BotAPISetBotInfo(ctx context.Context, botID int64, languageCode string, upd domain.BotInfoUpdate) (bool, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return false, errors.New("BOT_INVALID")
	}
	// 处理器对非空 lang_code 只回 true 不存储；Bot API 这里明确报错，免得机器人
	// 以为本地化文案已生效。
	if languageCode != "" {
		return false, errors.New("LANG_CODE_NOT_SUPPORTED")
	}
	req := &tg.BotsSetBotInfoRequest{}
	if upd.SetName {
		req.SetName(upd.Name)
	}
	if upd.SetAbout {
		req.SetAbout(upd.About)
	}
	if upd.SetDescription {
		req.SetDescription(upd.Description)
	}
	return r.onBotsSetBotInfo(WithUserID(ctx, botID), req)
}

// BotAPIGetBotInfo 是 getMyName / getMyDescription / getMyShortDescription 的网关，
// 返回 bots.getBotInfo 的 name / about / description。
func (r *Router) BotAPIGetBotInfo(ctx context.Context, botID int64, languageCode string) (name, about, description string, err error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) {
		return "", "", "", errors.New("BOT_INVALID")
	}
	info, err := r.onBotsGetBotInfo(WithUserID(ctx, botID), &tg.BotsGetBotInfoRequest{LangCode: languageCode})
	if err != nil {
		return "", "", "", err
	}
	return info.Name, info.About, info.Description, nil
}

// BotAPIGetMyDefaultAdministratorRights returns the same persisted scope used by
// the MTProto administrator-rights suggestions.
func (r *Router) BotAPIGetMyDefaultAdministratorRights(ctx context.Context, botID int64, forChannels bool) (domain.ChannelAdminRights, error) {
	if r == nil || botID == 0 || !r.userIsBot(ctx, botID) || r.deps.Bots == nil {
		return domain.ChannelAdminRights{}, errors.New("BOT_INVALID")
	}
	return r.deps.Bots.GetBotDefaultAdminRights(WithUserID(ctx, botID), botID, forChannels)
}

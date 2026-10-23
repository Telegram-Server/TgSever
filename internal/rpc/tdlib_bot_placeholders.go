package rpc

import (
	"context"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tlprofile"
)

// registerTDLibBotPlaceholders 注册 TDLib（telegram-bot-api）作为 bot 会调用、本服务
// 尚未支持的方法。它们返回类型正确的空结果：未注册时落 500 NOT_IMPLEMENTED，
// telegram-bot-api 会把错误回给 bot 开发者或反复重试（#143）。
func (r *Router) registerTDLibBotPlaceholders(d *tlprofile.Dispatcher) {
	registerRPC[*tg.BotsSetJoinChatResultsRequest](d, tlprofile.SemanticMethodBotsSetJoinChatResults, func(ctx context.Context, _ *tg.BotsSetJoinChatResultsRequest) (any, error) {
		// TODO(#142): 入群申请由 bot 审核（join chat bot query）尚未实现；服务端从不下发
		// 对应 query，这里只确认收到。
		if err := r.requireBotCaller(ctx); err != nil {
			return nil, err
		}
		return true, nil
	})
	registerRPC[*tg.MessagesSetBotGuestChatResultRequest](d, tlprofile.SemanticMethodMessagesSetBotGuestChatResult, func(ctx context.Context, _ *tg.MessagesSetBotGuestChatResultRequest) (any, error) {
		// TODO(#142): guest chat query（updateBotGuestChatQuery）尚未实现；服务端从不下发
		// 该 query，返回一个不指向任何消息的 inline message id。
		if err := r.requireBotCaller(ctx); err != nil {
			return nil, err
		}
		return &tg.InputBotInlineMessageID{DCID: r.cfg.DC}, nil
	})
	registerRPC[*tg.MessagesGetPersonalChannelHistoryRequest](d, tlprofile.SemanticMethodMessagesGetPersonalChannelHistory, func(ctx context.Context, _ *tg.MessagesGetPersonalChannelHistoryRequest) (any, error) {
		// TODO(#142): 个人频道（userFull.personal_channel_id）的历史尚未实现，返回空历史。
		return &tg.MessagesMessages{
			Messages: []tg.MessageClass{},
			Topics:   []tg.ForumTopicClass{},
			Chats:    []tg.ChatClass{},
			Users:    []tg.UserClass{},
		}, nil
	})
	registerRPC[*tg.UsersSetSecureValueErrorsRequest](d, tlprofile.SemanticMethodUsersSetSecureValueErrors, func(ctx context.Context, _ *tg.UsersSetSecureValueErrorsRequest) (any, error) {
		// TODO(#142): Telegram Passport 尚未实现，错误回报不保存。
		if err := r.requireBotCaller(ctx); err != nil {
			return nil, err
		}
		return true, nil
	})
}

// requireBotCaller 校验调用方是已登录的 bot：上面几个方法在官方 API 里只对 bot 开放。
func (r *Router) requireBotCaller(ctx context.Context) error {
	userID, authorized, err := r.currentUserID(ctx)
	if err != nil {
		return internalErr()
	}
	if !authorized || userID == 0 {
		return authKeyUnregisteredErr()
	}
	if !r.userIsBot(ctx, userID) {
		return userBotRequiredErr()
	}
	return nil
}

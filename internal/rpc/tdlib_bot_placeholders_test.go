package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// TDLib 会调用、本服务尚未支持的方法：bot 拿到类型正确的空结果；只对 bot 开放的方法
// 拒绝普通用户。线上请求形状由 mtprotoedge 的 TestTDLibBotContract 覆盖。
func TestTDLibBotPlaceholderMethods(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	person, err := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "+15550143101", FirstName: "Person"})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	bot, err := userStore.Create(ctx, domain.User{AccessHash: 12, FirstName: "Bot", Bot: true})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	r := New(Config{DC: 2}, Deps{Users: appusers.NewService(userStore)}, zaptest.NewLogger(t), clock.System)
	dispatch := func(userID int64, req bin.Encoder) (any, error) {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatalf("encode %T: %v", req, err)
		}
		out, err := r.Dispatch(WithUserID(ctx, userID), [8]byte{}, 0, &buf)
		if err != nil {
			return nil, err
		}
		return dispatchCanonicalValue(out), nil
	}
	personInput := &tg.InputUser{UserID: person.ID, AccessHash: person.AccessHash}

	botOnly := []struct {
		name string
		req  bin.Encoder
		want func(any) bool
	}{
		{"help.setBotUpdatesStatus", &tg.HelpSetBotUpdatesStatusRequest{PendingUpdatesCount: 1, Message: "x"}, isBoolTrue},
		{"bots.setJoinChatResults", &tg.BotsSetJoinChatResultsRequest{QueryID: 1, Result: &tg.JoinChatBotResultDeclined{}}, isBoolTrue},
		{"messages.setBotGuestChatResult", &tg.MessagesSetBotGuestChatResultRequest{QueryID: 2, Result: &tg.InputBotInlineResult{
			ID: "1", Type: "article", SendMessage: &tg.InputBotInlineMessageText{Message: "guest"},
		}}, func(v any) bool {
			id, ok := v.(*tg.InputBotInlineMessageID)
			return ok && id.DCID == 2
		}},
		{"users.setSecureValueErrors", &tg.UsersSetSecureValueErrorsRequest{ID: personInput, Errors: []tg.SecureValueErrorClass{}}, isBoolTrue},
	}
	for _, tc := range botOnly {
		got, err := dispatch(bot.ID, tc.req)
		if err != nil || !tc.want(got) {
			t.Errorf("%s by bot = %#v, %v; want legal empty result", tc.name, got, err)
		}
		if _, err := dispatch(person.ID, tc.req); !tgerr.Is(err, "USER_BOT_REQUIRED") {
			t.Errorf("%s by user error = %v, want USER_BOT_REQUIRED", tc.name, err)
		}
	}

	got, err := dispatch(bot.ID, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: personInput, Limit: 20})
	history, ok := got.(*tg.MessagesMessages)
	if err != nil || !ok || history.Messages == nil || len(history.Messages) != 0 || history.Users == nil || history.Chats == nil || history.Topics == nil {
		t.Fatalf("messages.getPersonalChannelHistory = %#v, %v; want messages.messages with empty vectors", got, err)
	}

	cdn, err := dispatch(bot.ID, &tg.HelpGetCDNConfigRequest{})
	if cfg, ok := cdn.(*tg.CDNConfig); err != nil || !ok || cfg.PublicKeys == nil || len(cfg.PublicKeys) != 0 {
		t.Fatalf("help.getCdnConfig = %#v, %v; want empty cdnConfig", cdn, err)
	}
}

func isBoolTrue(v any) bool {
	b, ok := v.(bool)
	return ok && b
}

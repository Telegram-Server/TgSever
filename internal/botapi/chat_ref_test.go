package botapi

import (
	"context"
	"errors"
	"testing"
)

type fakeChatRefGateway struct {
	*fakeBotAPIGateway
	resolved     map[string]int64
	resolveBotID int64
	resolveErr   error
}

func (f *fakeChatRefGateway) BotAPIResolveChatUsername(_ context.Context, botID int64, username string) (int64, error) {
	f.resolveBotID = botID
	if f.resolveErr != nil {
		return 0, f.resolveErr
	}
	id, ok := f.resolved[username]
	if !ok {
		return 0, errors.New("CHAT_ID_INVALID")
	}
	return id, nil
}

func TestChatIDParamAcceptsNumericIDs(t *testing.T) {
	h := &handler{gateway: &fakeBotAPIGateway{}}
	for raw, want := range map[string]int64{
		"777":           777,
		" -1001234567 ": -1001234567,
	} {
		got, err := h.chatIDParam(context.Background(), 1, raw)
		if err != nil || got != want {
			t.Fatalf("chatIDParam(%q) = %d, %v; want %d", raw, got, err, want)
		}
	}
}

func TestChatIDParamResolvesUsernameThroughGateway(t *testing.T) {
	gateway := &fakeChatRefGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, resolved: map[string]int64{"publicgroup": -1000000000042}}
	h := &handler{gateway: gateway}

	got, err := h.chatIDParam(context.Background(), 1001, "@publicgroup")
	if err != nil || got != -1000000000042 {
		t.Fatalf("chatIDParam(@publicgroup) = %d, %v; want -1000000000042", got, err)
	}
	if gateway.resolveBotID != 1001 {
		t.Fatalf("resolved as bot %d, want the calling bot 1001", gateway.resolveBotID)
	}
}

func TestChatIDParamRejectsMalformedInput(t *testing.T) {
	h := &handler{gateway: &fakeChatRefGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}}}
	for _, raw := range []string{"", "0", "abc", "@", "@ spaced"} {
		if _, err := h.chatIDParam(context.Background(), 1, raw); err == nil || apiErrorDescription(err) != "CHAT_ID_INVALID" {
			t.Fatalf("chatIDParam(%q) err = %v, want CHAT_ID_INVALID", raw, err)
		}
	}
}

func TestChatIDParamWithoutResolverRejectsUsernames(t *testing.T) {
	h := &handler{gateway: &fakeBotAPIGateway{}}
	if _, err := h.chatIDParam(context.Background(), 1, "@publicgroup"); err == nil || apiErrorDescription(err) != "CHAT_ID_INVALID" {
		t.Fatalf("err = %v, want CHAT_ID_INVALID when the gateway cannot resolve usernames", err)
	}
}

func TestChatIDParamPropagatesResolverErrors(t *testing.T) {
	gateway := &fakeChatRefGateway{fakeBotAPIGateway: &fakeBotAPIGateway{}, resolveErr: errors.New("rpc error code 400: USERNAME_NOT_OCCUPIED")}
	h := &handler{gateway: gateway}
	if _, err := h.chatIDParam(context.Background(), 1, "@ghost"); err == nil || apiErrorDescription(err) != "USERNAME_NOT_OCCUPIED" {
		t.Fatalf("err = %v, want USERNAME_NOT_OCCUPIED surfaced", err)
	}
}

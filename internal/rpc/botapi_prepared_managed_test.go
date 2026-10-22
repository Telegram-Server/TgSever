package rpc

import (
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
	"testing"
)

func TestPreparedManagedButtonUsesNativeCreateAndAuthorizedShare(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := inlineHTTP(t, f)
	fields := map[string]any{"user_id": f.owner.ID, "button": map[string]any{"text": "Create a bot", "request_managed_bot": map[string]any{"request_id": -42, "suggested_name": "New Managed", "suggested_username": "suggested_child_bot"}}}
	call("savePreparedKeyboardButton", fields, false)
	if err := f.bots.SetCanManageBots(f.ctx, f.owner.ID, f.bot.ID, true); err != nil {
		t.Fatal(err)
	}
	saved := call("savePreparedKeyboardButton", fields, true).(map[string]any)
	id := saved["id"].(string)
	button, err := f.router.onBotsGetRequestedWebViewButton(WithUserID(f.ctx, f.owner.ID), &tg.BotsGetRequestedWebViewButtonRequest{Bot: inputUser(f.bot), WebappReqID: id})
	if err != nil {
		t.Fatal(err)
	}
	peer, ok := button.Type.(*tg.ButtonTypeRequestPeer)
	if !ok {
		t.Fatalf("button type=%T", button.Type)
	}
	create, ok := peer.PeerType.(*tg.RequestPeerTypeCreateBot)
	if !ok || !create.BotManaged || create.SuggestedName != "New Managed" || create.SuggestedUsername != "suggested_child_bot" {
		t.Fatalf("native create prompt=%+v", peer.PeerType)
	}
	created, err := f.router.onBotsCreateBot(WithUserID(f.ctx, f.owner.ID), &tg.BotsCreateBotRequest{Name: "New Managed", Username: "actual_child_test_bot", ManagerID: inputUser(f.bot)})
	if err != nil {
		t.Fatal(err)
	}
	child, ok := created.(*tg.User)
	if !ok {
		t.Fatalf("created=%T", created)
	}
	wrong := &tg.MessagesSendBotRequestedPeerRequest{Peer: inputPeerUser(f.bot), ButtonID: -42, RequestedPeers: []tg.InputPeerClass{inputPeerUser(f.bot)}}
	wrong.SetWebappReqID(id)
	if _, err := f.router.onMessagesSendBotRequestedPeer(WithUserID(f.ctx, f.owner.ID), wrong); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("unrelated bot share=%v", err)
	}
	share := &tg.MessagesSendBotRequestedPeerRequest{Peer: inputPeerUser(f.bot), ButtonID: -42, RequestedPeers: []tg.InputPeerClass{&tg.InputPeerUser{UserID: child.ID, AccessHash: child.AccessHash}}}
	share.SetWebappReqID(id)
	if _, err := f.router.onMessagesSendBotRequestedPeer(WithUserID(f.ctx, f.owner.ID), share); err != nil {
		t.Fatal(err)
	}
	history, err := f.messages.GetHistory(f.ctx, f.bot.ID, domain.MessageFilter{HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, Limit: 1})
	if err != nil || len(history.Messages) != 1 {
		t.Fatalf("history=%+v err=%v", history, err)
	}
	action := history.Messages[0].Media.ServiceAction.RequestedPeer
	if action.ManagedBot == nil || action.ManagedBot.ID != child.ID || action.ManagedBot.Username != "actual_child_test_bot" || action.ManagedBot.AccessHash != 0 || action.ManagedBot.Phone != "" {
		t.Fatalf("managed bot public snapshot=%+v", action.ManagedBot)
	}
	if _, err := f.router.onBotsGetRequestedWebViewButton(WithUserID(f.ctx, f.owner.ID), &tg.BotsGetRequestedWebViewButtonRequest{Bot: inputUser(f.bot), WebappReqID: id}); !tgerr.Is(err, "BUTTON_DATA_INVALID") {
		t.Fatalf("consumed button read=%v", err)
	}
}

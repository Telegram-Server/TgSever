package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"
	appaccount "telesrv/internal/app/account"
	appstars "telesrv/internal/app/stars"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func newBusinessFixture(t *testing.T, rights domain.BusinessBotRights) (botAPIBridgeFixture, *appaccount.Service, domain.ConnectedBusinessBot) {
	t.Helper()
	f := newBotAPIBridgeFixture(t)
	passwords := memory.NewPasswordStore()
	service := appaccount.NewService(passwords, appaccount.WithBusinessAutomation(passwords), appaccount.WithAccountSettings(passwords))
	f.router.deps.Account = service
	request := &tg.AccountUpdateConnectedBotRequest{Bot: &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}, Recipients: tg.InputBusinessBotRecipients{ExistingChats: true, NewChats: true, Contacts: true, NonContacts: true}}
	request.SetRights(tgBusinessBotRights(rights))
	if _, err := f.router.onAccountUpdateConnectedBot(WithUserID(f.ctx, f.owner.ID), request); err != nil {
		t.Fatal(err)
	}
	connection, found, err := service.GetConnectedBusinessBot(f.ctx, f.owner.ID)
	if err != nil || !found {
		t.Fatalf("connect: %+v %v %v", connection, found, err)
	}
	return f, service, connection
}

func TestBotAPIBusinessHTTPDelegationAndRevocation(t *testing.T) {
	f, _, connection := newBusinessFixture(t, domain.BusinessBotRights{EditName: true, EditBio: true, ChangeGiftSettings: true})
	ctx, cancel := context.WithCancel(f.ctx)
	defer cancel()
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer server.Close()
	call := func(method string, fields map[string]any) map[string]any {
		t.Helper()
		fields["business_connection_id"] = connection.ConnectionID
		body, err := json.Marshal(fields)
		if err != nil {
			t.Fatal(err)
		}
		request := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), strings.NewReader(string(body)))
		request.Header.Set("Content-Type", "application/json")
		response := httptest.NewRecorder()
		server.Handler.ServeHTTP(response, request)
		var object map[string]any
		if err := json.Unmarshal(response.Body.Bytes(), &object); err != nil {
			t.Fatal(err)
		}
		return object
	}
	got := call("getBusinessConnection", map[string]any{})
	object, ok := got["result"].(map[string]any)
	if got["ok"] != true || !ok || object["id"] != connection.ConnectionID || object["is_enabled"] != true || object["user_chat_id"] != float64(f.owner.ID) {
		t.Fatalf("getconnection=%+v", got)
	}
	if got := call("setBusinessAccountName", map[string]any{"first_name": "Delegated", "last_name": "Owner"}); got["ok"] != true {
		t.Fatalf("setname=%+v", got)
	}
	owner, _, err := f.users.ByID(f.ctx, f.member.ID, f.owner.ID)
	bot, _, botErr := f.users.ByID(f.ctx, f.owner.ID, f.bot.ID)
	if err != nil || botErr != nil || owner.FirstName != "Delegated" || owner.LastName != "Owner" || bot.FirstName != "BridgeBot" {
		t.Fatalf("wrong profile changed: owner=%+v bot=%+v %v %v", owner, bot, err, botErr)
	}
	bio := strings.Repeat("界", 140)
	if got := call("setBusinessAccountBio", map[string]any{"bio": bio}); got["ok"] != true {
		t.Fatalf("business140bio=%+v", got)
	}
	owner, _, err = f.users.ByID(f.ctx, f.member.ID, f.owner.ID)
	if err != nil || owner.About != bio {
		t.Fatalf("business bio not visible: %+v %v", owner, err)
	}
	if got := call("setBusinessAccountName", map[string]any{"first_name": "DelegatedAgain", "last_name": "Owner"}); got["ok"] != true {
		t.Fatalf("name edit after 140-character bio=%+v", got)
	}
	owner, _, err = f.users.ByID(f.ctx, f.member.ID, f.owner.ID)
	if err != nil || owner.FirstName != "DelegatedAgain" || owner.About != bio {
		t.Fatalf("name edit lost business bio: %+v %v", owner, err)
	}
	if _, err := f.users.UpdateProfile(f.ctx, f.member.ID, domain.UserProfileUpdate{About: strings.Repeat("x", 71), HasAbout: true}); err == nil {
		t.Fatal("business path widened ordinary user bio limit")
	}
	if got := call("setBusinessAccountUsername", map[string]any{"username": "no_right"}); got["ok"] != false {
		t.Fatalf("missing right accepted=%+v", got)
	}
	if got := call("setBusinessAccountGiftSettings", map[string]any{"show_gift_button": true, "accepted_gift_types": map[string]bool{"unlimited_gifts": false, "limited_gifts": true, "unique_gifts": false, "premium_subscription": true, "gifts_from_channels": false}}); got["ok"] != true {
		t.Fatalf("giftsettings=%+v", got)
	}
	settings, err := f.router.cachedAccountSettings(f.ctx, f.owner.ID)
	if err != nil || !settings.GlobalPrivacy.DisplayGiftsButton || !settings.GlobalPrivacy.DisallowedGifts.UnlimitedStargifts || settings.GlobalPrivacy.DisallowedGifts.LimitedStargifts || !settings.GlobalPrivacy.DisallowedGifts.StargiftsFromChannel {
		t.Fatalf("gift settings not persisted=%+v %v", settings, err)
	}
	if _, _, err := f.router.BotAPIGetBusinessConnection(f.ctx, f.member.ID, connection.ConnectionID); err == nil {
		t.Fatal("another caller read business connection")
	}
	unrelatedBot, _, err := memory.NewBotStore(f.userStore).CreateBotAccount(f.ctx, domain.User{AccessHash: 9321, FirstName: "OtherBusinessBot", Username: "other_business_bot"}, domain.BotProfile{OwnerUserID: f.member.ID, TokenSecret: "other"})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.router.BotAPIGetBusinessConnection(f.ctx, unrelatedBot.ID, connection.ConnectionID); err == nil {
		t.Fatal("foreign bot read another bot's connection")
	}
	if _, err := f.router.botAPIBusinessOwner(f.ctx, f.bot.ID, fmt.Sprint(f.owner.ID), "can_edit_name"); err == nil {
		t.Fatal("owner numeric id accepted as opaque connection")
	}
	if _, err := f.router.onAccountUpdateConnectedBot(WithUserID(f.ctx, f.owner.ID), &tg.AccountUpdateConnectedBotRequest{Deleted: true, Bot: &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}}); err != nil {
		t.Fatal(err)
	}
	got = call("getBusinessConnection", map[string]any{})
	object, _ = got["result"].(map[string]any)
	if got["ok"] != true || object["is_enabled"] != false {
		t.Fatalf("revoked connection snapshot=%+v", got)
	}
	if got := call("setBusinessAccountName", map[string]any{"first_name": "ShouldNotChange"}); got["ok"] != false {
		t.Fatalf("revoked edit accepted=%+v", got)
	}
	owner, _, err = f.users.ByID(f.ctx, f.member.ID, f.owner.ID)
	if err != nil || owner.FirstName != "DelegatedAgain" {
		t.Fatalf("revoked edit mutated owner=%+v %v", owner, err)
	}
}

func TestBotAPIBusinessPeerPauseExclusionAndActivity(t *testing.T) {
	f, service, connection := newBusinessFixture(t, domain.BusinessBotRights{Reply: true, ReadMessages: true})
	peer := domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}
	now := int(f.router.clock.Now().Unix())
	old, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 9101, Message: "old incoming", Date: now - 86401})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.router.BotAPIReadBusinessMessage(f.ctx, f.bot.ID, connection.ConnectionID, peer.ID, old.RecipientMessage.ID); err == nil || ok {
		t.Fatal("inactive business chat accepted")
	}
	recent, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 9102, Message: "recent incoming", Date: now})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.onAccountToggleConnectedBotPaused(WithUserID(f.ctx, f.owner.ID), &tg.AccountToggleConnectedBotPausedRequest{Paused: true, Peer: &tg.InputPeerUser{UserID: peer.ID, AccessHash: f.member.AccessHash}}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.botAPIBusinessPeer(f.ctx, f.bot.ID, connection.ConnectionID, "can_reply", peer); err == nil {
		t.Fatal("paused peer accepted")
	}
	if _, err := service.SetConnectedBusinessBotPaused(f.ctx, f.owner.ID, peer.ID, false); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.router.BotAPIReadBusinessMessage(f.ctx, f.bot.ID, connection.ConnectionID, peer.ID, recent.RecipientMessage.ID); err != nil || !ok {
		t.Fatalf("read=%v %v", ok, err)
	}
	readDate, err := f.messages.GetOutboxReadDate(f.ctx, f.member.ID, domain.OutboxReadDateRequest{OwnerUserID: f.member.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, ID: recent.SenderMessage.ID})
	if err != nil || readDate < now {
		t.Fatalf("sender did not see read receipt: %d %v", readDate, err)
	}
	connection.Recipients.ExcludeUsers = []int64{peer.ID}
	if _, err := service.SaveConnectedBusinessBot(f.ctx, f.owner.ID, connection); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.botAPIBusinessPeer(f.ctx, f.bot.ID, connection.ConnectionID, "can_reply", peer); err == nil {
		t.Fatal("excluded recipient accepted")
	}
}

func TestBotAPIBusinessDeleteAuthorAndChatIsolation(t *testing.T) {
	f, _, connection := newBusinessFixture(t, domain.BusinessBotRights{DeleteSentMessages: true})
	now := int(f.router.clock.Now().Unix())
	send := func(to int64, random int64, business bool) domain.SendPrivateTextResult {
		t.Helper()
		request := domain.SendPrivateTextRequest{SenderUserID: f.owner.ID, RecipientUserID: to, RandomID: random, Message: "business deletion", Date: now}
		if business {
			request.BusinessConnectionID, request.BusinessBotID = connection.ConnectionID, f.bot.ID
		}
		result, err := f.messages.SendPrivateText(f.ctx, f.owner.ID, request)
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	own := send(f.member.ID, 9201, true)
	owner := send(f.member.ID, 9202, false)
	otherChat := send(f.bot.ID, 9203, true)
	if ok, err := f.router.BotAPIDeleteBusinessMessages(f.ctx, f.bot.ID, connection.ConnectionID, []int{owner.SenderMessage.ID}); err == nil || ok {
		t.Fatal("sent right deleted owner's own message")
	}
	if ok, err := f.router.BotAPIDeleteBusinessMessages(f.ctx, f.bot.ID, connection.ConnectionID, []int{own.SenderMessage.ID, otherChat.SenderMessage.ID}); err == nil || ok {
		t.Fatal("mixed chat delete accepted")
	}
	if ok, err := f.router.BotAPIDeleteBusinessMessages(f.ctx, f.bot.ID, connection.ConnectionID, []int{own.SenderMessage.ID}); err != nil || !ok {
		t.Fatalf("business delete=%v %v", ok, err)
	}
	list, err := f.messages.GetMessages(f.ctx, f.owner.ID, []int{own.SenderMessage.ID, owner.SenderMessage.ID, otherChat.SenderMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	kept := map[int]bool{}
	for _, message := range list.Messages {
		if !message.Deleted {
			kept[message.ID] = true
		}
	}
	if kept[own.SenderMessage.ID] || !kept[owner.SenderMessage.ID] || !kept[otherChat.SenderMessage.ID] {
		t.Fatalf("wrong messages deleted: %+v", kept)
	}
	peer, err := f.messages.GetMessages(f.ctx, f.member.ID, []int{own.RecipientMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range peer.Messages {
		if !message.Deleted {
			t.Fatalf("business revoke not visible to recipient: %+v", message)
		}
	}
}

func TestBotAPIBusinessDeleteAllAndAgeLimit(t *testing.T) {
	f, _, connection := newBusinessFixture(t, domain.BusinessBotRights{DeleteReceivedMessages: true})
	now := int(f.router.clock.Now().Unix())
	old, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 9301, Message: "too old", Date: now - 48*60*60})
	if err != nil {
		t.Fatal(err)
	}
	recent, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 9302, Message: "incoming", Date: now})
	if err != nil {
		t.Fatal(err)
	}
	if ok, err := f.router.BotAPIDeleteBusinessMessages(f.ctx, f.bot.ID, connection.ConnectionID, []int{recent.RecipientMessage.ID, old.RecipientMessage.ID}); err == nil || ok {
		t.Fatal("48-hour message allowed in batch")
	}
	list, err := f.messages.GetMessages(f.ctx, f.owner.ID, []int{recent.RecipientMessage.ID})
	if err != nil || len(list.Messages) != 1 || list.Messages[0].Deleted {
		t.Fatalf("failed batch partially deleted recent message: %+v %v", list, err)
	}
	if ok, err := f.router.BotAPIDeleteBusinessMessages(f.ctx, f.bot.ID, connection.ConnectionID, []int{recent.RecipientMessage.ID}); err != nil || !ok {
		t.Fatalf("delete-all right did not delete incoming: %v %v", ok, err)
	}
	list, err = f.messages.GetMessages(f.ctx, f.member.ID, []int{recent.SenderMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range list.Messages {
		if !message.Deleted {
			t.Fatalf("incoming message not revoked from sender: %+v", message)
		}
	}
}

func TestBotAPIBusinessMessageEventSnapshots(t *testing.T) {
	f, _, connection := newBusinessFixture(t, domain.BusinessBotRights{Reply: true})
	now := int(f.router.clock.Now().Unix())
	sent, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 9401, Message: "original business incoming", Date: now})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIPrivateMessageUpdate(f.ctx, sent)
	edited, err := f.messages.EditMessage(f.ctx, f.member.ID, domain.EditMessageRequest{OwnerUserID: f.member.ID, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.owner.ID}, ID: sent.SenderMessage.ID, Message: "edited business incoming", EditDate: now})
	if err != nil {
		t.Fatal(err)
	}
	f.router.enqueueBotAPIPrivateEditUpdates(f.ctx, edited)
	deleteRequest := &tg.MessagesDeleteMessagesRequest{ID: []int{sent.SenderMessage.ID}}
	deleteRequest.SetRevoke(true)
	if _, err := f.router.onMessagesDeleteMessages(WithUserID(f.ctx, f.member.ID), deleteRequest); err != nil {
		t.Fatal(err)
	}
	remaining, err := f.messages.GetMessages(f.ctx, f.owner.ID, []int{sent.RecipientMessage.ID})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range remaining.Messages {
		if !message.Deleted {
			t.Fatalf("business owner still sees the supposedly revoked message: %+v", message)
		}
	}
	if _, err := f.users.UpdateProfile(f.ctx, f.member.ID, domain.UserProfileUpdate{FirstName: "RenamedAfterEvents", HasFirstName: true}); err != nil {
		t.Fatal(err)
	}
	counts := map[string]int{}
	for _, update := range joinHTTPUpdates(t, f) {
		for _, kind := range []string{"business_message", "edited_business_message", "deleted_business_messages"} {
			object, found := update[kind].(map[string]any)
			if !found {
				continue
			}
			counts[kind]++
			if object["business_connection_id"] != connection.ConnectionID {
				t.Fatalf("wrong connection snapshot: %+v", object)
			}
			if kind == "deleted_business_messages" {
				ids, ok := object["message_ids"].([]any)
				if !ok || len(ids) != 1 || ids[0] != float64(sent.RecipientMessage.ID) {
					t.Fatalf("delete leaked wrong owner's ids: %+v", object)
				}
				continue
			}
			from, ok := object["from"].(map[string]any)
			if !ok || from["first_name"] != "Ann" {
				t.Fatalf("event was rehydrated from mutable user: %+v", object)
			}
			if _, exists := object["sender_business_bot"]; exists {
				t.Fatalf("incoming message attributed to business bot: %+v", object)
			}
			want := "original business incoming"
			if kind == "edited_business_message" {
				want = "edited business incoming"
			}
			if object["text"] != want || object["message_id"] != float64(sent.RecipientMessage.ID) {
				t.Fatalf("message snapshot changed: %+v", object)
			}
		}
	}
	for _, kind := range []string{"business_message", "edited_business_message", "deleted_business_messages"} {
		if counts[kind] != 1 {
			t.Fatalf("%s count=%d, want one authoritative event", kind, counts[kind])
		}
	}
}

func TestBotAPIBusinessQueuedContentRechecksDelegation(t *testing.T) {
	for _, state := range []string{"paused", "revoked"} {
		t.Run(state, func(t *testing.T) {
			f, service, connection := newBusinessFixture(t, domain.BusinessBotRights{Reply: true})
			sent, err := f.messages.SendPrivateText(f.ctx, f.member.ID, domain.SendPrivateTextRequest{SenderUserID: f.member.ID, RecipientUserID: f.owner.ID, RandomID: 9501, Message: "queued private business content", Date: int(f.router.clock.Now().Unix())})
			if err != nil {
				t.Fatal(err)
			}
			f.router.enqueueBotAPIPrivateMessageUpdate(f.ctx, sent)
			if state == "paused" {
				if _, err := service.SetConnectedBusinessBotPaused(f.ctx, f.owner.ID, f.member.ID, true); err != nil {
					t.Fatal(err)
				}
			} else {
				if _, err := f.router.onAccountUpdateConnectedBot(WithUserID(f.ctx, f.owner.ID), &tg.AccountUpdateConnectedBotRequest{Deleted: true, Bot: &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash}}); err != nil {
					t.Fatal(err)
				}
			}
			sawDisabled := false
			for _, update := range joinHTTPUpdates(t, f) {
				if _, found := update["business_message"]; found {
					t.Fatalf("%s leaked queued private content: %+v", state, update)
				}
				if object, ok := update["business_connection"].(map[string]any); ok && object["id"] == connection.ConnectionID && object["is_enabled"] == false {
					sawDisabled = true
				}
			}
			if state == "revoked" && !sawDisabled {
				t.Fatal("revocation filtered its own connection-disabled notification")
			}
		})
	}
}

func TestBotAPIBusinessStarsDelegation(t *testing.T) {
	f, service, connection := newBusinessFixture(t, domain.BusinessBotRights{ViewGifts: true, TransferStars: true})
	stars := memory.NewStarsStore()
	starService := appstars.NewService(stars, appstars.WithStartingGrant(0))
	commerce := memory.NewBotCommerceStore(stars, nil, memory.NewMessageStore(memory.NewDialogStore()), nil)
	f.router.deps.Stars, f.router.deps.BotCommerce = starService, commerce
	if _, err := starService.Credit(f.ctx, f.owner.ID, 80, domain.StarsReasonAdjust, domain.Peer{}, "Business funds", ""); err != nil {
		t.Fatal(err)
	}
	balance, err := f.router.BotAPIGetBusinessAccountStarBalance(f.ctx, f.bot.ID, connection.ConnectionID)
	if err != nil || balance.Balance != 80 {
		t.Fatalf("business balance=%+v %v", balance, err)
	}
	if ok, err := f.router.BotAPITransferBusinessAccountStars(f.ctx, f.bot.ID, connection.ConnectionID, 30); err != nil || !ok {
		t.Fatalf("transfer=%v %v", ok, err)
	}
	assertBalances := func() {
		t.Helper()
		owner, err := starService.GetBalance(f.ctx, f.owner.ID)
		bot, botErr := commerce.BotStarsBalance(f.ctx, f.bot.ID)
		if err != nil || botErr != nil || owner.Balance != 50 || bot != 30 {
			t.Fatalf("transfer not conserved: owner=%+v bot=%d %v %v", owner, bot, err, botErr)
		}
	}
	assertBalances()
	if ok, err := f.router.BotAPITransferBusinessAccountStars(f.ctx, f.bot.ID, connection.ConnectionID, 100); err == nil || ok {
		t.Fatal("insufficient transfer accepted")
	}
	assertBalances()
	connection.Rights.TransferStars = false
	if _, err := service.SaveConnectedBusinessBot(f.ctx, f.owner.ID, connection); err != nil {
		t.Fatal(err)
	}
	if ok, err := f.router.BotAPITransferBusinessAccountStars(f.ctx, f.bot.ID, connection.ConnectionID, 1); err == nil || ok {
		t.Fatal("revoked transfer right accepted")
	}
	assertBalances()
	if _, err := service.DeleteConnectedBusinessBot(f.ctx, f.owner.ID, f.bot.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.BotAPIGetBusinessAccountStarBalance(f.ctx, f.bot.ID, connection.ConnectionID); err == nil {
		t.Fatal("revoked connection read owner's funds")
	}
}

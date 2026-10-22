package rpc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func chatInviteHTTP(t *testing.T, f botAPIBridgeFixture) func(string, map[string]any) map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	t.Cleanup(func() { cancel(); server.Close() })
	return func(method string, params map[string]any) map[string]any {
		t.Helper()
		raw, err := json.Marshal(params)
		if err != nil {
			t.Fatal(err)
		}
		req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/%s", f.bot.ID, method), strings.NewReader(string(raw)))
		req.Header.Set("Content-Type", "application/json")
		rec := httptest.NewRecorder()
		server.Handler.ServeHTTP(rec, req)
		var out map[string]any
		if err = json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
			t.Fatalf("%s: %s %v", method, rec.Body, err)
		}
		return out
	}
}
func requireChatInviteOK(t *testing.T, out map[string]any) any {
	t.Helper()
	if out["ok"] != true {
		t.Fatalf("Bot API failed: %+v", out)
	}
	return out["result"]
}
func requireChatInviteError(t *testing.T, out map[string]any) {
	t.Helper()
	if out["ok"] != false {
		t.Fatalf("Bot API accepted invalid action: %+v", out)
	}
}

func TestBotAPIInviteOwnershipApprovalAndRevocationHTTP(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := chatInviteHTTP(t, f)
	requireChatInviteError(t, call("createChatInviteLink", map[string]any{"chat_id": f.chatID}))
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	ownerInvite, err := f.channels.ExportInvite(f.ctx, f.owner.ID, domain.ExportChannelInviteRequest{ChannelID: f.channel.ID, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	foreign := publicLinkWithBaseURL(f.router.cfg.PublicBaseURL, "+"+ownerInvite.Invite.Hash)
	requireChatInviteError(t, call("editChatInviteLink", map[string]any{"chat_id": f.chatID, "invite_link": foreign, "name": "stolen"}))
	requireChatInviteError(t, call("revokeChatInviteLink", map[string]any{"chat_id": f.chatID, "invite_link": foreign}))
	primary := requireChatInviteOK(t, call("exportChatInviteLink", map[string]any{"chat_id": f.chatID})).(string)
	next := requireChatInviteOK(t, call("exportChatInviteLink", map[string]any{"chat_id": f.chatID})).(string)
	if primary == next {
		t.Fatal("primary was not rotated")
	}
	hash, _ := channelInviteHashFromLink(primary)
	if _, err = f.channels.CheckInvite(f.ctx, f.member.ID, hash, int(time.Now().Unix())); err == nil {
		t.Fatal("rotated primary remained usable")
	}
	requireChatInviteError(t, call("editChatInviteLink", map[string]any{"chat_id": f.chatID, "invite_link": next, "name": "primary edit"}))
	requireChatInviteError(t, call("createChatInviteLink", map[string]any{"chat_id": f.chatID, "creates_join_request": true, "member_limit": 2}))
	link := requireChatInviteOK(t, call("createChatInviteLink", map[string]any{"chat_id": f.chatID, "name": "审批", "creates_join_request": true})).(map[string]any)["invite_link"].(string)
	edited := requireChatInviteOK(t, call("editChatInviteLink", map[string]any{"chat_id": f.chatID, "invite_link": link, "name": "changed"})).(map[string]any)
	if edited["name"] != "changed" || edited["creates_join_request"] != true {
		t.Fatalf("edited invite=%+v", edited)
	}
	leaveJoinApplicant(t, f)
	hash, _ = channelInviteHashFromLink(link)
	if _, err = f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash); err == nil {
		t.Fatal("request joined without approval")
	}
	requireChatInviteOK(t, call("approveChatJoinRequest", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID}))
	if got := f.participant(t, f.member.ID); got.Status != domain.ChannelMemberActive {
		t.Fatalf("approval did not join: %+v", got)
	}
	pending, err := f.channels.PendingJoinRequests(f.ctx, f.channel.ID, 10)
	if err != nil || pending.Count != 0 {
		t.Fatalf("approval pending=%+v %v", pending, err)
	}
	requireChatInviteError(t, call("declineChatJoinRequest", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID}))
	leaveJoinApplicant(t, f)
	if _, err = f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash); err == nil {
		t.Fatal("request unexpectedly joined")
	}
	requireChatInviteOK(t, call("declineChatJoinRequest", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID}))
	revoked := requireChatInviteOK(t, call("revokeChatInviteLink", map[string]any{"chat_id": f.chatID, "invite_link": link})).(map[string]any)
	if revoked["is_revoked"] != true {
		t.Fatalf("revocation=%+v", revoked)
	}
	if _, err = f.channels.CheckInvite(f.ctx, f.member.ID, hash, int(time.Now().Unix())); err == nil {
		t.Fatal("revoked link usable")
	}
}

func TestBotAPIChatMemberTagUnicodeHTTP(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := chatInviteHTTP(t, f)
	params := map[string]any{"chat_id": f.chatID, "user_id": f.member.ID, "tag": strings.Repeat("界", 16)}
	requireChatInviteError(t, call("setChatMemberTag", params))
	f.promoteBot(t, domain.ChannelAdminRights{ManageRanks: true})
	requireChatInviteOK(t, call("setChatMemberTag", params))
	if got := f.participant(t, f.member.ID).Rank; got != params["tag"] {
		t.Fatalf("unicode tag=%q", got)
	}
	projected := requireChatInviteOK(t, call("getChatMember", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID})).(map[string]any)
	if projected["tag"] != params["tag"] {
		t.Fatalf("public member tag=%+v", projected)
	}
	for _, tag := range []string{strings.Repeat("界", 17), "tag😀", "family👨‍👩‍👧", "key1️⃣"} {
		params["tag"] = tag
		requireChatInviteError(t, call("setChatMemberTag", params))
	}
	if got := f.participant(t, f.member.ID).Rank; got != strings.Repeat("界", 16) {
		t.Fatalf("invalid tag mutated member: %q", got)
	}
	params["user_id"] = f.owner.ID
	params["tag"] = "owner"
	requireChatInviteError(t, call("setChatMemberTag", params))
	params["user_id"] = f.member.ID
	delete(params, "tag")
	requireChatInviteOK(t, call("setChatMemberTag", params))
	if got := f.participant(t, f.member.ID).Rank; got != "" {
		t.Fatalf("tag removal=%q", got)
	}
	projected = requireChatInviteOK(t, call("getChatMember", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID})).(map[string]any)
	if _, present := projected["tag"]; present {
		t.Fatalf("removed tag still projected: %+v", projected)
	}
	f.promoteBot(t, domain.ChannelAdminRights{ManageRanks: true, AddAdmins: true})
	requireChatInviteOK(t, call("promoteChatMember", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID, "can_manage_tags": true}))
	projected = requireChatInviteOK(t, call("getChatMember", map[string]any{"chat_id": f.chatID, "user_id": f.member.ID})).(map[string]any)
	if projected["status"] != "administrator" || projected["can_manage_tags"] != true {
		t.Fatalf("tag permission not round-tripped: %+v", projected)
	}
	params["tag"] = "not-admin-title"
	requireChatInviteError(t, call("setChatMemberTag", params))
}

func newGuardQuery(t *testing.T, f botAPIBridgeFixture) *tg.MessagesChatInviteJoinResultWebView {
	t.Helper()
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	req := &tg.ChannelsToggleJoinRequestRequest{Channel: f.inputChannel(), Enabled: true}
	req.SetGuardBot(inputUser(f.bot))
	if _, err := f.router.onChannelsToggleJoinRequest(WithUserID(f.ctx, f.owner.ID), req); err != nil {
		t.Fatal(err)
	}
	leaveJoinApplicant(t, f)
	result, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel())
	if err != nil {
		t.Fatal(err)
	}
	query, ok := result.(*tg.MessagesChatInviteJoinResultWebView)
	if !ok || query.QueryID <= 0 || query.BotID != f.bot.ID {
		t.Fatalf("real join query=%T %+v", result, result)
	}
	return query
}
func TestBotAPIChatJoinQueryWebAppAndSingleDecisionHTTP(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatJoinRequest, domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatal(err)
	}
	query := newGuardQuery(t, f)
	call := chatInviteHTTP(t, f)
	id := strconv.FormatInt(query.QueryID, 10)
	bodies := joinRequestBodies(t, f)
	if len(bodies) != 1 || bodies[0]["query_id"] != id {
		t.Fatalf("guard event snapshot=%+v", bodies)
	}
	requireChatInviteError(t, call("answerChatJoinRequestQuery", map[string]any{"chat_join_request_query_id": id, "result": "unknown"}))
	requireChatInviteError(t, call("sendChatJoinRequestWebApp", map[string]any{"chat_join_request_query_id": id, "web_app_url": "http://guard.example/verify"}))
	requireChatInviteOK(t, call("sendChatJoinRequestWebApp", map[string]any{"chat_join_request_query_id": id, "web_app_url": "https://guard.example/verify"}))
	request := &tg.MessagesRequestChatJoinWebViewRequest{QueryID: query.QueryID, Platform: "android"}
	if _, err := f.router.onMessagesRequestChatJoinWebView(WithUserID(f.ctx, f.owner.ID), request); err == nil {
		t.Fatal("other user opened applicant webapp")
	}
	webview, err := f.router.onMessagesRequestChatJoinWebView(WithUserID(f.ctx, f.member.ID), request)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := url.Parse(webview.URL)
	if err != nil {
		t.Fatal(err)
	}
	data, err := url.ParseQuery(parsed.Query().Get("tgWebAppData"))
	if err != nil {
		t.Fatal(err)
	}
	if data.Get("chat_join_request_query_id") != id || data.Get("query_id") != "" || data.Get("hash") != webViewInitDataHash(data, domain.FormatBotToken(f.bot.ID, "secret")) {
		t.Fatalf("join Mini App init data=%v", data)
	}
	requireChatInviteOK(t, call("answerChatJoinRequestQuery", map[string]any{"chat_join_request_query_id": id, "result": "approve"}))
	if member := f.participant(t, f.member.ID); member.Status != domain.ChannelMemberActive {
		t.Fatalf("guard approval failed: %+v", member)
	}
	updates, ok := f.sessions.lastUserPush().(*tg.Updates)
	if !ok {
		t.Fatalf("native decision=%T", f.sessions.lastUserPush())
	}
	decided := false
	for _, update := range updates.Updates {
		if decision, ok := update.(*tg.UpdateJoinChatWebViewDecision); ok {
			_, approved := decision.Result.(*tg.JoinChatBotResultApproved)
			decided = decision.QueryID == query.QueryID && approved
		}
	}
	if !decided {
		t.Fatalf("native approval decision missing: %+v", updates)
	}
	joined := 0
	for _, update := range joinHTTPUpdates(t, f) {
		if change, ok := update["chat_member"].(map[string]any); ok && change["via_join_request"] == true {
			member := change["new_chat_member"].(map[string]any)
			if member["status"] == "member" {
				joined++
			}
		}
	}
	if joined != 1 {
		t.Fatalf("guard approval membership events=%d", joined)
	}
	requireChatInviteError(t, call("answerChatJoinRequestQuery", map[string]any{"chat_join_request_query_id": id, "result": "decline"}))
	if _, err = f.router.onMessagesRequestChatJoinWebView(WithUserID(f.ctx, f.member.ID), request); err == nil {
		t.Fatal("consumed query reopened")
	}
}

func TestChatJoinQueryExpiryHandoffAndGuardRevocation(t *testing.T) {
	for _, webapp := range []bool{false, true} {
		t.Run(fmt.Sprint(webapp), func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			now := int(time.Now().Unix())
			f.router.clock = fixedClock{now: time.Unix(int64(now), 0)}
			query := newGuardQuery(t, f)
			initial := joinRequestBodies(t, f)
			if len(initial) != 1 || initial[0]["query_id"] == nil {
				t.Fatalf("initial guard query=%+v", initial)
			}
			expiry := now + 10
			if webapp {
				if err := f.router.BotAPISendChatJoinRequestWebApp(f.ctx, f.bot.ID, strconv.FormatInt(query.QueryID, 10), "https://guard.example"); err != nil {
					t.Fatal(err)
				}
				expiry = now + 300
			}
			f.router.clock = fixedClock{now: time.Unix(int64(expiry), 0)}
			if count, err := f.router.ProcessExpiredChatJoinQueries(f.ctx, 100); err != nil || count != 1 {
				t.Fatalf("expiry=%d %v", count, err)
			}
			bodies := joinRequestBodies(t, f)
			if len(bodies) != 1 || bodies[0]["query_id"] != nil {
				t.Fatalf("queued handoff snapshots=%+v", bodies)
			}
			if pending, err := f.channels.PendingJoinRequests(f.ctx, f.channel.ID, 10); err != nil || pending.Count != 1 {
				t.Fatalf("queued request missing: %+v %v", pending, err)
			}
			if err := f.router.BotAPIAnswerChatJoinRequestQuery(f.ctx, f.bot.ID, strconv.FormatInt(query.QueryID, 10), "approve"); err == nil {
				t.Fatal("expired query resolved")
			}
			full, err := f.router.onChannelsGetFullChannel(WithUserID(f.ctx, f.owner.ID), f.inputChannel())
			if err != nil {
				t.Fatal(err)
			}
			if guard, ok := full.FullChat.(*tg.ChannelFull).GetGuardBotID(); !ok || guard != f.bot.ID {
				t.Fatalf("full guard=%d %v", guard, ok)
			}
			if _, err = f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{ChannelID: f.channel.ID, MemberID: f.bot.ID, Date: expiry}); err != nil {
				t.Fatal(err)
			}
			full, err = f.router.onChannelsGetFullChannel(WithUserID(f.ctx, f.owner.ID), f.inputChannel())
			if err != nil {
				t.Fatal(err)
			}
			if _, ok := full.FullChat.(*tg.ChannelFull).GetGuardBotID(); ok {
				t.Fatal("cached full exposed revoked guard")
			}
		})
	}
}

func TestBotAPIChatSenderBansAndStickerSetHTTP(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	call := chatInviteHTTP(t, f)
	source, err := f.channels.CreateChannel(f.ctx, f.member.ID, domain.CreateChannelRequest{Title: "Private speaking identity", Broadcast: true, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	senderID := -botAPIChannelChatIDBase - source.Channel.ID
	params := map[string]any{"chat_id": f.chatID, "sender_chat_id": senderID}
	requireChatInviteError(t, call("banChatSenderChat", params))
	f.promoteBot(t, domain.ChannelAdminRights{BanUsers: true, ChangeInfo: true})
	requireChatInviteOK(t, call("banChatSenderChat", params))
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: source.Channel.ID}
	if _, err = f.channels.SendMessage(f.ctx, f.member.ID, domain.SendChannelMessageRequest{ChannelID: f.channel.ID, Message: "banned channel", SendAs: &peer, Date: int(time.Now().Unix())}); err == nil {
		t.Fatal("banned sender sent message")
	}
	requireChatInviteOK(t, call("unbanChatSenderChat", params))
	if _, err = f.channels.SendMessage(f.ctx, f.member.ID, domain.SendChannelMessageRequest{ChannelID: f.channel.ID, Message: "restored channel", SendAs: &peer, Date: int(time.Now().Unix())}); err != nil {
		t.Fatal(err)
	}
	f.files.sets = map[domain.StickerSetKind][]domain.StickerSet{domain.StickerSetKindStickers: {{ID: 7131, AccessHash: 7132, ShortName: "group_set", Kind: domain.StickerSetKindStickers}}}
	stickerParams := map[string]any{"chat_id": f.chatID, "sticker_set_name": "group_set"}
	requireChatInviteError(t, call("setChatStickerSet", stickerParams))
	var members []int64
	for i := range domain.MinChannelStickerSetParticipants - 3 {
		user, err := f.userStore.Create(f.ctx, domain.User{FirstName: "Member", Phone: fmt.Sprintf("1555888%04d", i), AccessHash: int64(i + 9000)})
		if err != nil {
			t.Fatal(err)
		}
		members = append(members, user.ID)
	}
	if _, err = f.channels.InviteToChannel(f.ctx, f.owner.ID, f.channel.ID, members, int(time.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	requireChatInviteOK(t, call("setChatStickerSet", stickerParams))
	view, err := f.channels.GetChannel(f.ctx, f.owner.ID, f.channel.ID)
	if err != nil || view.Channel.StickerSetID != 7131 {
		t.Fatalf("sticker set not stored: %+v %v", view.Channel, err)
	}
	requireChatInviteOK(t, call("deleteChatStickerSet", map[string]any{"chat_id": f.chatID}))
	view, err = f.channels.GetChannel(f.ctx, f.owner.ID, f.channel.ID)
	if err != nil || view.Channel.StickerSetID != 0 {
		t.Fatalf("sticker set not removed: %+v %v", view.Channel, err)
	}
}

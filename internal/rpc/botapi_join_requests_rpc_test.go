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
	"telesrv/internal/botapi"
	"telesrv/internal/domain"
)

func requireRPCError(t *testing.T, err error, code int, name string) {
	t.Helper()
	if err == nil || !strings.Contains(err.Error(), name) || !strings.Contains(err.Error(), fmt.Sprint(code)) {
		t.Fatalf("RPC error=%v, want %d %s", err, code, name)
	}
}

func joinHTTPUpdates(t *testing.T, f botAPIBridgeFixture) []map[string]any {
	t.Helper()
	ctx, cancel := context.WithCancel(f.ctx)
	server, err := botapi.Start(ctx, "127.0.0.1:0", f.bots, f.users, nil, nil, f.router, nil)
	if err != nil {
		cancel()
		t.Fatal(err)
	}
	defer cancel()
	defer server.Close()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, fmt.Sprintf("/bot%d:secret/getUpdates", f.bot.ID), strings.NewReader(`{}`))
	req.Header.Set("Content-Type", "application/json")
	server.Handler.ServeHTTP(rec, req)
	var response struct {
		OK     bool             `json:"ok"`
		Result []map[string]any `json:"result"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &response); err != nil || !response.OK {
		t.Fatalf("getUpdates: %s (%v)", rec.Body.String(), err)
	}
	return response.Result
}

func joinRequestBodies(t *testing.T, f botAPIBridgeFixture) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, update := range joinHTTPUpdates(t, f) {
		if body, ok := update["chat_join_request"].(map[string]any); ok {
			out = append(out, body)
		}
	}
	return out
}

func leaveJoinApplicant(t *testing.T, f botAPIBridgeFixture) {
	t.Helper()
	if _, err := f.router.onChannelsLeaveChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel()); err != nil {
		t.Fatal(err)
	}
}

func TestBotAPIJoinRequestEntrypointsAndSnapshot(t *testing.T) {
	for _, public := range []bool{false, true} {
		t.Run(fmt.Sprintf("public=%v", public), func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
			leaveJoinApplicant(t, f)
			if _, err := f.users.UpdateProfile(f.ctx, f.member.ID, domain.UserProfileUpdate{About: "Please let me join", HasAbout: true}); err != nil {
				t.Fatal(err)
			}
			hash := ""
			if public {
				if _, err := f.channels.SetJoinRequest(f.ctx, f.owner.ID, f.channel.ID, true); err != nil {
					t.Fatal(err)
				}
			} else {
				hash = f.exportInvite(t, true)
			}
			f.sessions.clearMessages()
			var err error
			if public {
				_, err = f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel())
			} else {
				_, err = f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash)
			}
			requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
			pushed, ok := f.sessions.lastUserPush().(*tg.Updates)
			if !ok {
				t.Fatalf("requester push = %T", f.sessions.lastUserPush())
			}
			var requester *tg.UpdateBotChatInviteRequester
			for _, update := range pushed.Updates {
				if u, ok := update.(*tg.UpdateBotChatInviteRequester); ok {
					requester = u
				}
			}
			if requester == nil || requester.UserID != f.member.ID || requester.About != "Please let me join" {
				t.Fatalf("requester=%+v", requester)
			}
			if public {
				if _, ok := requester.Invite.(*tg.ChatInvitePublicJoinRequests); !ok {
					t.Fatalf("public invite=%T", requester.Invite)
				}
			} else {
				invite, ok := requester.Invite.(*tg.ChatInviteExported)
				if !ok || !strings.HasSuffix(invite.Link, "+"+hash) {
					t.Fatalf("link invite=%+v", requester.Invite)
				}
			}
			// Remove the pending row and change the bio before polling: the queued request remains intact.
			if _, err := f.router.onMessagesHideChatJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideChatJoinRequestRequest{Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}, UserID: &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}}); err != nil {
				t.Fatal(err)
			}
			if _, err := f.users.UpdateProfile(f.ctx, f.member.ID, domain.UserProfileUpdate{About: "changed", HasAbout: true}); err != nil {
				t.Fatal(err)
			}
			bodies := joinRequestBodies(t, f)
			if len(bodies) != 1 {
				t.Fatalf("requests=%+v", bodies)
			}
			body := bodies[0]
			if body["bio"] != "Please let me join" || body["from"].(map[string]any)["id"] != float64(f.member.ID) || body["user_chat_id"] != float64(f.member.ID) || body["chat"].(map[string]any)["id"] != float64(f.chatID) {
				t.Fatalf("request JSON=%+v", body)
			}
			if public {
				if _, ok := body["invite_link"]; ok {
					t.Fatalf("public request has invite=%+v", body)
				}
			} else {
				link := body["invite_link"].(map[string]any)
				if !strings.HasSuffix(link["invite_link"].(string), "+"+hash) || link["creates_join_request"] != true || link["creator"].(map[string]any)["id"] != float64(f.owner.ID) {
					t.Fatalf("invite=%+v", link)
				}
			}
		})
	}
}

func TestBotAPIJoinRequestPermissionAndAllowedUpdates(t *testing.T) {
	for _, rights := range []domain.ChannelAdminRights{{}, {ChangeInfo: true}, {InviteUsers: true}} {
		t.Run(fmt.Sprintf("rights=%+v", rights), func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			if rights != (domain.ChannelAdminRights{}) {
				f.promoteBot(t, rights)
			}
			leaveJoinApplicant(t, f)
			hash := f.exportInvite(t, true)
			f.sessions.clearMessages()
			_, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash)
			requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
			got := joinRequestBodies(t, f)
			if rights.InviteUsers {
				if len(got) != 1 {
					t.Fatalf("authorized requests=%+v", got)
				}
			} else {
				if len(got) != 0 {
					t.Fatalf("unauthorized requests=%+v", got)
				}
				for _, id := range f.sessions.pushedUserIDs() {
					if id == f.bot.ID {
						t.Fatal("unauthorized bot received MTProto request")
					}
				}
			}
		})
	}
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	leaveJoinApplicant(t, f)
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateMessage}); err != nil {
		t.Fatal(err)
	}
	_, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), f.exportInvite(t, true))
	requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
	if got := joinRequestBodies(t, f); len(got) != 0 {
		t.Fatalf("excluded requests=%+v", got)
	}
}

func TestBotAPIPublicJoinApprovalHasNoInvite(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	leaveJoinApplicant(t, f)
	if _, err := f.channels.SetJoinRequest(f.ctx, f.owner.ID, f.channel.ID, true); err != nil {
		t.Fatal(err)
	}
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatal(err)
	}
	_, err := f.router.onChannelsJoinChannel(WithUserID(f.ctx, f.member.ID), f.inputChannel())
	requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
	result, err := f.router.onMessagesHideChatJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideChatJoinRequestRequest{Approved: true, Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}, UserID: &tg.InputUser{UserID: f.member.ID, AccessHash: f.member.AccessHash}})
	if err != nil {
		t.Fatal(err)
	}
	var participant *tg.UpdateChannelParticipant
	var action tg.MessageActionClass
	for _, update := range result.(*tg.Updates).Updates {
		switch u := update.(type) {
		case *tg.UpdateChannelParticipant:
			participant = u
		case *tg.UpdateNewChannelMessage:
			action = u.Message.(*tg.MessageService).Action
		}
	}
	if participant == nil || participant.Invite != nil || participant.ActorID != f.owner.ID {
		t.Fatalf("public participant=%+v", participant)
	}
	if _, ok := action.(*tg.MessageActionChatJoinedByRequest); !ok {
		t.Fatalf("public action=%T", action)
	}
	var member map[string]any
	for _, update := range joinHTTPUpdates(t, f) {
		if body, ok := update["chat_member"].(map[string]any); ok {
			member = body
		}
	}
	if member == nil || member["via_join_request"] != true {
		t.Fatalf("public member=%+v", member)
	}
	if _, present := member["invite_link"]; present {
		t.Fatalf("public approval leaked invite=%+v", member)
	}
}

func TestBotAPIInviteJoinAndBatchApprovalsKeepEachLink(t *testing.T) {
	for _, approved := range []bool{false, true} {
		t.Run(fmt.Sprintf("approved=%v", approved), func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
			leaveJoinApplicant(t, f)
			second, err := f.userStore.Create(f.ctx, domain.User{AccessHash: 9911, FirstName: "Second", Phone: "15559910001"})
			if err != nil {
				t.Fatal(err)
			}
			if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
				t.Fatal(err)
			}
			hashes := map[int64]string{f.member.ID: f.exportInvite(t, approved), second.ID: f.exportInvite(t, approved)}
			var last tg.UpdatesClass
			for _, user := range []domain.User{f.member, second} {
				result, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, user.ID), hashes[user.ID])
				if approved {
					requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
				} else {
					if err != nil {
						t.Fatal(err)
					}
					last = result.(*tg.MessagesChatInviteJoinResultOk).Updates
					assertJoinedInviteUpdates(t, last, map[int64]string{user.ID: hashes[user.ID]}, false)
				}
			}
			if approved {
				last, err = f.router.onMessagesHideAllChatJoinRequests(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideAllChatJoinRequestsRequest{Approved: true, Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}})
				if err != nil {
					t.Fatal(err)
				}
				assertJoinedInviteUpdates(t, last, hashes, true)
			}
			bodies := joinHTTPUpdates(t, f)
			seen := make(map[int64]bool)
			for _, update := range bodies {
				body, ok := update["chat_member"].(map[string]any)
				if !ok {
					continue
				}
				member := body["new_chat_member"].(map[string]any)
				id := int64(member["user"].(map[string]any)["id"].(float64))
				if member["status"] != "member" {
					t.Fatalf("new member=%+v", member)
				}
				link := body["invite_link"].(map[string]any)["invite_link"].(string)
				if !strings.HasSuffix(link, "+"+hashes[id]) || (body["via_join_request"] == true) != approved {
					t.Fatalf("member=%+v", body)
				}
				seen[id] = true
			}
			if len(seen) != 2 {
				t.Fatalf("member updates=%+v", bodies)
			}
		})
	}
}

func assertJoinedInviteUpdates(t *testing.T, updates tg.UpdatesClass, hashes map[int64]string, approved bool) {
	t.Helper()
	full, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("updates=%T", updates)
	}
	seen, messages := make(map[int64]bool), 0
	for _, update := range full.Updates {
		switch u := update.(type) {
		case *tg.UpdateChannelParticipant:
			invite, ok := u.Invite.(*tg.ChatInviteExported)
			if !ok || !strings.HasSuffix(invite.Link, "+"+hashes[u.UserID]) {
				t.Fatalf("participant=%+v invite=%+v", u, u.Invite)
			}
			seen[u.UserID] = true
		case *tg.UpdateNewChannelMessage:
			message, ok := u.Message.(*tg.MessageService)
			if !ok {
				t.Fatalf("service=%T", u.Message)
			}
			if approved {
				if _, ok := message.Action.(*tg.MessageActionChatJoinedByRequest); !ok {
					t.Fatalf("approved action=%T", message.Action)
				}
			} else {
				if _, ok := message.Action.(*tg.MessageActionChatJoinedByLink); !ok {
					t.Fatalf("direct action=%T", message.Action)
				}
			}
			messages++
		}
	}
	if len(seen) != len(hashes) || messages != len(hashes) {
		t.Fatalf("participants=%v messages=%d updates=%+v", seen, messages, full.Updates)
	}
}

func TestBotAPIJoinRequestDirectInviteThenBatchNotifiesEveryJoin(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	leaveJoinApplicant(t, f)
	second, err := f.userStore.Create(f.ctx, domain.User{FirstName: "Second", Phone: "15559110111", AccessHash: 8811})
	if err != nil {
		t.Fatal(err)
	}
	hash := f.exportInvite(t, true)
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatal(err)
	}
	for _, user := range []domain.User{f.member, second} {
		_, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, user.ID), hash)
		requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
	}
	if _, err := f.router.onChannelsInviteToChannel(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsInviteToChannelRequest{Channel: f.inputChannel(), Users: []tg.InputUserClass{&tg.InputUser{UserID: second.ID, AccessHash: second.AccessHash}}}); err != nil {
		t.Fatal(err)
	}
	pending, err := f.channels.PendingJoinRequests(f.ctx, f.channel.ID, 5)
	if err != nil || pending.Count != 1 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
	result, err := f.router.onMessagesHideAllChatJoinRequests(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideAllChatJoinRequestsRequest{Approved: true, Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}})
	if err != nil {
		t.Fatal(err)
	}
	assertJoinedInviteUpdates(t, result, map[int64]string{f.member.ID: hash}, true)
	events := botMemberEvents(t, f, domain.UpdateEventBotChatMember)
	if len(events) != 2 {
		t.Fatalf("membership events=%+v", events)
	}
	found := make(map[int64]bool)
	for _, event := range events {
		member := event.BotChatMember
		found[member.New.UserID] = true
		if member.New.Status != domain.ChannelMemberActive {
			t.Fatalf("member=%+v", member)
		}
		if member.New.UserID == second.ID {
			if member.Invite != nil || member.ViaJoinRequest {
				t.Fatalf("direct invitation=%+v", member)
			}
		} else if member.New.UserID == f.member.ID {
			if member.Invite == nil || member.Invite.Hash != hash || !member.ViaJoinRequest {
				t.Fatalf("approved request=%+v", member)
			}
		} else {
			t.Fatalf("unexpected member=%+v", member)
		}
	}
	if !found[f.member.ID] || !found[second.ID] {
		t.Fatalf("notified users=%v", found)
	}
}

func TestBotAPIJoinRequestFailedBatchHasNoPartialNotifications(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
	leaveJoinApplicant(t, f)
	second, err := f.userStore.Create(f.ctx, domain.User{FirstName: "Second", Phone: "15559110112", AccessHash: 8812})
	if err != nil {
		t.Fatal(err)
	}
	hash := f.exportInvite(t, true)
	for _, user := range []domain.User{f.member, second} {
		_, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, user.ID), hash)
		requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
	}
	if _, err := f.channels.EditExportedInvite(f.ctx, f.owner.ID, domain.EditChannelInviteRequest{UserID: f.owner.ID, ChannelID: f.channel.ID, Hash: hash, HasRequestNeeded: true, RequestNeeded: false, HasUsageLimit: true, UsageLimit: 1, Date: int(f.router.clock.Now().Unix())}); err != nil {
		t.Fatal(err)
	}
	if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
		t.Fatal(err)
	}
	f.sessions.clearMessages()
	_, err = f.router.onMessagesHideAllChatJoinRequests(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideAllChatJoinRequestsRequest{Approved: true, Peer: &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}})
	requireRPCError(t, err, 400, "USERS_TOO_MUCH")
	if events := botMemberEvents(t, f, domain.UpdateEventBotChatMember); len(events) != 0 {
		t.Fatalf("partial notifications=%+v", events)
	}
	if pushes := f.sessions.pushedUserIDs(); len(pushes) != 0 {
		t.Fatalf("failed batch pushed users=%v", pushes)
	}
	pending, err := f.channels.PendingJoinRequests(f.ctx, f.channel.ID, 5)
	if err != nil || pending.Count != 2 {
		t.Fatalf("pending=%+v err=%v", pending, err)
	}
}

func TestBotAPIDeletingInviteClearsPendingAndRefreshesAdmins(t *testing.T) {
	for _, bulk := range []bool{false, true} {
		t.Run(fmt.Sprintf("bulk=%v", bulk), func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			f.promoteBot(t, domain.ChannelAdminRights{InviteUsers: true})
			leaveJoinApplicant(t, f)
			hash := f.exportInvite(t, true)
			_, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), hash)
			requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
			if _, err := f.channels.EditExportedInvite(f.ctx, f.owner.ID, domain.EditChannelInviteRequest{UserID: f.owner.ID, ChannelID: f.channel.ID, Hash: hash, Revoked: true, Date: int(f.router.clock.Now().Unix())}); err != nil {
				t.Fatal(err)
			}
			peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
			f.sessions.clearMessages()
			if bulk {
				_, err = f.router.onMessagesDeleteRevokedExportedChatInvites(WithUserID(f.ctx, f.owner.ID), &tg.MessagesDeleteRevokedExportedChatInvitesRequest{Peer: peer, AdminID: &tg.InputUserSelf{}})
			} else {
				_, err = f.router.onMessagesDeleteExportedChatInvite(WithUserID(f.ctx, f.owner.ID), &tg.MessagesDeleteExportedChatInviteRequest{Peer: peer, Link: publicLinkWithBaseURL(f.router.cfg.PublicBaseURL, "+"+hash)})
			}
			if err != nil {
				t.Fatal(err)
			}
			pending, err := f.channels.PendingJoinRequests(f.ctx, f.channel.ID, 5)
			if err != nil || pending.Count != 0 {
				t.Fatalf("orphan pending=%+v err=%v", pending, err)
			}
			updates, ok := f.sessions.lastUserPush().(*tg.Updates)
			if !ok {
				t.Fatalf("admin push=%T", f.sessions.lastUserPush())
			}
			summary, ok := updates.Updates[0].(*tg.UpdatePendingJoinRequests)
			if !ok || summary.RequestsPending != 0 || len(summary.RecentRequesters) != 0 {
				t.Fatalf("summary=%+v", updates)
			}
			_, err = f.router.onMessagesImportChatInvite(WithUserID(f.ctx, f.member.ID), f.exportInvite(t, true))
			requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
		})
	}
}

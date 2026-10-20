package rpc

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/proto"
	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

type inviteVisibilityDelivery struct {
	participants []*tg.UpdateChannelParticipant
	wire         []byte
}

type inviteVisibilitySessions struct {
	*captureSessions
	recordMu   sync.Mutex
	deliveries map[int64][]inviteVisibilityDelivery
}

func (s *inviteVisibilitySessions) PushToUserExceptAuthKeySession(ctx context.Context, userID int64, key [8]byte, sessionID int64, kind proto.MessageType, box tg.UpdatesClass) (int, error) {
	if updates, ok := box.(*tg.Updates); ok {
		var participants []*tg.UpdateChannelParticipant
		for _, update := range updates.Updates {
			if participant, ok := update.(*tg.UpdateChannelParticipant); ok {
				participants = append(participants, participant)
			}
		}
		if len(participants) > 0 {
			var buffer bin.Buffer
			if err := box.Encode(&buffer); err != nil {
				return 0, err
			}
			s.recordMu.Lock()
			s.deliveries[userID] = append(s.deliveries[userID], inviteVisibilityDelivery{participants: participants, wire: append([]byte(nil), buffer.Buf...)})
			s.recordMu.Unlock()
		}
	}
	return s.captureSessions.PushToUserExceptAuthKeySession(ctx, userID, key, sessionID, kind, box)
}

func TestInviteJoinVisibilityAcrossRecipientsRPC(t *testing.T) {
	for _, flow := range []string{"direct", "approve", "batch"} {
		t.Run(flow, func(t *testing.T) {
			f := newBotAPIBridgeFixture(t)
			newUser := func(index int, name string) domain.User {
				t.Helper()
				user, err := f.userStore.Create(f.ctx, domain.User{AccessHash: int64(4800 + index), Phone: fmt.Sprintf("155500148%02d", index), FirstName: name})
				if err != nil {
					t.Fatal(err)
				}
				return user
			}
			creator := newUser(1, "Link creator")
			ordinary := newUser(2, "Ordinary member")
			first := newUser(3, "First applicant")
			second := newUser(4, "Second applicant")
			peer := &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
			input := f.inputChannel()
			if _, err := f.router.onChannelsInviteToChannel(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsInviteToChannelRequest{Channel: input, Users: []tg.InputUserClass{&tg.InputUser{UserID: creator.ID, AccessHash: creator.AccessHash}, &tg.InputUser{UserID: ordinary.ID, AccessHash: ordinary.AccessHash}}}); err != nil {
				t.Fatal(err)
			}
			promote := func(user domain.User, rights tg.ChatAdminRights) {
				t.Helper()
				if _, err := f.router.onChannelsEditAdmin(WithUserID(f.ctx, f.owner.ID), &tg.ChannelsEditAdminRequest{Channel: input, UserID: &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash}, AdminRights: rights}); err != nil {
					t.Fatal(err)
				}
			}
			promote(creator, tg.ChatAdminRights{InviteUsers: true})
			promote(f.member, tg.ChatAdminRights{ChangeInfo: true})
			promote(f.bot, tg.ChatAdminRights{InviteUsers: true})
			if err := f.router.BotAPISetAllowedUpdates(f.ctx, f.bot.ID, []domain.BotAPIUpdateKind{domain.BotAPIUpdateChatMember}); err != nil {
				t.Fatal(err)
			}
			type invitation struct {
				hash, link, title string
				creator           int64
			}
			links := map[int64]invitation{}
			export := func(by domain.User, applicant domain.User, title string) {
				t.Helper()
				result, err := f.router.onMessagesExportChatInvite(WithUserID(f.ctx, by.ID), &tg.MessagesExportChatInviteRequest{Peer: peer, Title: title, RequestNeeded: flow != "direct"})
				if err != nil {
					t.Fatal(err)
				}
				link := result.(*tg.ChatInviteExported).Link
				hash, err := channelInviteHashFromLink(link)
				if err != nil {
					t.Fatal(err)
				}
				links[applicant.ID] = invitation{hash: hash, link: link, title: title, creator: by.ID}
			}
			export(creator, first, "Foreign campaign")
			applicants := []domain.User{first}
			if flow == "batch" {
				export(f.bot, second, "Bot campaign")
				applicants = append(applicants, second)
			}
			// The link creator remains entitled to their own link after demotion,
			// not to a different administrator's link in the same batch.
			promote(creator, tg.ChatAdminRights{})
			viewers := []domain.User{f.owner, f.bot, creator, f.member, ordinary}
			f.sessions.mu.Lock()
			f.sessions.channelMembers[f.channel.ID] = []int64{f.owner.ID, f.bot.ID, creator.ID, f.member.ID, ordinary.ID}
			f.sessions.onlineUserIDs = append([]int64(nil), f.sessions.channelMembers[f.channel.ID]...)
			f.sessions.mu.Unlock()
			sessions := &inviteVisibilitySessions{captureSessions: f.sessions, deliveries: make(map[int64][]inviteVisibilityDelivery)}
			f.router.deps.Sessions = sessions
			for _, applicant := range applicants {
				result, err := f.router.onMessagesImportChatInvite(WithUserID(f.ctx, applicant.ID), links[applicant.ID].hash)
				if flow == "direct" {
					if err != nil {
						t.Fatal(err)
					}
					var wire bin.Buffer
					if err := result.(*tg.MessagesChatInviteJoinResultOk).Updates.Encode(&wire); err != nil {
						t.Fatal(err)
					}
					if bytes.Contains(wire.Buf, []byte(links[applicant.ID].hash)) {
						t.Fatal("ordinary joining user's response exposed private invite")
					}
				} else {
					requireRPCError(t, err, 400, "INVITE_REQUEST_SENT")
				}
			}
			if flow == "approve" {
				if _, err := f.router.onMessagesHideChatJoinRequest(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideChatJoinRequestRequest{Peer: peer, Approved: true, UserID: &tg.InputUser{UserID: first.ID, AccessHash: first.AccessHash}}); err != nil {
					t.Fatal(err)
				}
			} else if flow == "batch" {
				if _, err := f.router.onMessagesHideAllChatJoinRequests(WithUserID(f.ctx, f.owner.ID), &tg.MessagesHideAllChatJoinRequestsRequest{Peer: peer, Approved: true}); err != nil {
					t.Fatal(err)
				}
			}
			sessions.recordMu.Lock()
			deliveries := make(map[int64][]inviteVisibilityDelivery, len(sessions.deliveries))
			for viewerID, items := range sessions.deliveries {
				deliveries[viewerID] = append([]inviteVisibilityDelivery(nil), items...)
			}
			sessions.recordMu.Unlock()
			for _, viewer := range viewers {
				seen := make(map[int64]bool)
				for _, delivery := range deliveries[viewer.ID] {
					for _, participant := range delivery.participants {
						link, exists := links[participant.UserID]
						if !exists {
							continue
						}
						seen[participant.UserID] = true
						allowed := viewer.ID == f.owner.ID || viewer.ID == f.bot.ID || viewer.ID == link.creator
						invite, present := participant.GetInvite()
						if present != allowed {
							t.Fatalf("viewer=%s member=%d invite present=%v, want %v", viewer.FirstName, participant.UserID, present, allowed)
						}
						if allowed && invite.(*tg.ChatInviteExported).Link != link.link {
							t.Fatalf("authorized viewer got wrong invite: %+v", invite)
						}
					}
					for _, link := range links {
						allowed := viewer.ID == f.owner.ID || viewer.ID == f.bot.ID || viewer.ID == link.creator
						if !allowed && bytes.Contains(delivery.wire, []byte(link.hash)) {
							t.Fatalf("private hash leaked in encoded update to %s", viewer.FirstName)
						}
					}
				}
				for id := range links {
					if !seen[id] {
						t.Fatalf("viewer %s received no participant transition for %d", viewer.FirstName, id)
					}
				}
			}
			seenHTTP := make(map[int64]bool)
			for _, update := range joinHTTPUpdates(t, f) {
				body, ok := update["chat_member"].(map[string]any)
				if !ok {
					continue
				}
				id := int64(body["new_chat_member"].(map[string]any)["user"].(map[string]any)["id"].(float64))
				link, exists := links[id]
				if !exists {
					continue
				}
				projected := body["invite_link"].(map[string]any)
				want := publicLinkWithBaseURL(f.router.cfg.PublicBaseURL, "+...")
				if link.creator == f.bot.ID {
					want = link.link
				}
				if projected["invite_link"] != want || projected["name"] != link.title || projected["creator"].(map[string]any)["id"] != float64(link.creator) {
					t.Fatalf("bot invite metadata=%+v, want link %q", projected, want)
				}
				encoded, err := json.Marshal(body)
				if err != nil {
					t.Fatal(err)
				}
				for _, secret := range links {
					if secret.creator != f.bot.ID && bytes.Contains(encoded, []byte(secret.hash)) {
						t.Fatal("HTTP update leaked another administrator's invite hash")
					}
				}
				seenHTTP[id] = true
			}
			for id := range links {
				if !seenHTTP[id] {
					t.Fatalf("missing HTTP member update for %d", id)
				}
			}
		})
	}
}

package storetest

import (
	"context"
	"errors"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
)

func ChannelSenderBans(t *testing.T, s store.ChannelStore, reopen func() store.ChannelStore, owner, member int64) {
	t.Helper()
	ctx := context.Background()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Sender ban group", Megagroup: true, MemberUserIDs: []int64{member}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	source, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: member, Title: "Sender identity", Broadcast: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: source.Channel.ID}
	ban := domain.EditChannelBannedRequest{UserID: member, ChannelID: created.Channel.ID, Participant: peer, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: baseNow()}
	if _, err := s.EditChannelBanned(ctx, ban); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("ordinary member ban = %v", err)
	}
	unauthorizedUnban := ban
	unauthorizedUnban.BannedRights = domain.ChannelBannedRights{}
	if _, err := s.EditChannelBanned(ctx, unauthorizedUnban); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("ordinary member empty unban = %v", err)
	}
	ban.UserID = owner
	assertEmptyUnban := func(participant domain.Peer) {
		t.Helper()
		before, err := s.GetChannel(ctx, owner, created.Channel.ID)
		if err != nil {
			t.Fatal(err)
		}
		logsBefore, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: created.Channel.ID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		res, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: created.Channel.ID, Participant: participant, Date: baseNow()})
		if err != nil || !res.NoChange || len(res.Recipients) != 0 || res.Message.ID != 0 || res.Event.Pts != 0 || res.ServiceEvent.Pts != 0 {
			t.Fatalf("empty unban transition = %+v, %v", res, err)
		}
		s = reopen()
		after, err := s.GetChannel(ctx, owner, created.Channel.ID)
		if err != nil {
			t.Fatal(err)
		}
		if after.Channel.Pts != before.Channel.Pts || after.Channel.TopMessageID != before.Channel.TopMessageID || after.Channel.ParticipantsCount != before.Channel.ParticipantsCount {
			t.Fatalf("empty unban changed channel: before=%+v after=%+v", before.Channel, after.Channel)
		}
		logsAfter, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: created.Channel.ID, Limit: 100})
		if err != nil || len(logsAfter.Events) != len(logsBefore.Events) {
			t.Fatalf("empty unban added audit: before=%+v after=%+v, %v", logsBefore.Events, logsAfter.Events, err)
		}
	}
	assertEmptyUnban(peer)
	rejectedBefore, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: created.Channel.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, until := range []int{baseNow() + 86400, -1} {
		temporary := ban
		temporary.BannedRights.UntilDate = until
		if _, err := s.EditChannelBanned(ctx, temporary); !errors.Is(err, domain.ErrChannelInvalid) {
			t.Fatalf("temporary sender ban until=%d: %v", until, err)
		}
		s = reopen()
		if _, err := s.GetChannelSenderBan(ctx, owner, created.Channel.ID, peer.ID); !errors.Is(err, domain.ErrUserNotParticipant) {
			t.Fatalf("temporary ban persisted: %v", err)
		}
		logs, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: created.Channel.ID, Limit: 100})
		if err != nil || len(logs.Events) != len(rejectedBefore.Events) {
			t.Fatalf("rejected ban audit = %+v, %v", logs.Events, err)
		}
	}
	if _, err := s.EditChannelBanned(ctx, ban); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	got, err := s.GetChannelSenderBan(ctx, owner, created.Channel.ID, peer.ID)
	if err != nil || got.Peer != peer || !got.BannedRights.ViewMessages || got.InviterUserID != owner || got.LeftAt != ban.Date {
		t.Fatalf("persisted sender ban = %+v, %v", got, err)
	}
	if got.SenderChannelIdentity.Title != source.Channel.Title || got.SenderChannelIdentity.AccessHash != source.Channel.AccessHash {
		t.Fatalf("private ban identity = %+v", got.SenderChannelIdentity)
	}
	ordinary, err := s.GetChannels(ctx, owner, []int64{source.Channel.ID})
	if err != nil || len(ordinary) != 0 {
		t.Fatalf("ordinary private channel lookup = %+v, %v", ordinary, err)
	}
	if _, err := s.GetChannelSenderBan(ctx, member, created.Channel.ID, peer.ID); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("non-admin ban identity lookup = %v", err)
	}
	list, err := s.GetParticipants(ctx, owner, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Participants) != 1 || list.Participants[0].Peer != peer {
		t.Fatalf("kicked list = %+v", list)
	}
	if list.Participants[0].SenderChannelIdentity.Title != source.Channel.Title || list.Participants[0].SenderChannelIdentity.AccessHash != source.Channel.AccessHash {
		t.Fatalf("kicked private identity = %+v", list.Participants[0].SenderChannelIdentity)
	}
	for _, tc := range []struct {
		name    string
		request domain.SendChannelMessageRequest
	}{
		{"text", domain.SendChannelMessageRequest{Message: "blocked"}},
		{"media", domain.SendChannelMessageRequest{Message: "media", Media: &domain.MessageMedia{Kind: domain.MessageMediaKindGeo, Geo: &domain.MessageGeoPoint{Lat: 1, Long: 1}}}},
		{"album", domain.SendChannelMessageRequest{Message: "album", GroupedID: 5501}},
		{"forward", domain.SendChannelMessageRequest{Message: "forward", Forward: &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: owner}}}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			req := tc.request
			req.UserID = member
			req.ChannelID = created.Channel.ID
			req.SendAs = &peer
			req.Date = baseNow()
			if _, err := s.SendChannelMessage(ctx, req); !errors.Is(err, domain.ErrChannelSenderBanned) {
				t.Fatalf("banned send = %v", err)
			}
		})
	}
	alternate, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: member, Title: "Other owned identity", Broadcast: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	alternatePeer := domain.Peer{Type: domain.PeerTypeChannel, ID: alternate.Channel.ID}
	if _, err = s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: created.Channel.ID, Message: "owner bypass", SendAs: &alternatePeer, Date: baseNow()}); !errors.Is(err, domain.ErrChannelSenderBanned) {
		t.Fatalf("same-owner alternate channel bypassed ban: %v", err)
	}
	ban.BannedRights = domain.ChannelBannedRights{}
	if _, err := s.EditChannelBanned(ctx, ban); err != nil {
		t.Fatal(err)
	}
	s = reopen()
	if _, err := s.GetChannelSenderBan(ctx, owner, created.Channel.ID, peer.ID); !errors.Is(err, domain.ErrUserNotParticipant) {
		t.Fatalf("unban query = %v", err)
	}
	assertEmptyUnban(peer)
	sent, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: created.Channel.ID, Message: "restored", SendAs: &peer, Date: baseNow()})
	if err != nil || sent.Message.SendAs == nil || *sent.Message.SendAs != peer {
		t.Fatalf("unbanned send = %+v, %v", sent.Message, err)
	}
	if _, err = s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: member, ChannelID: created.Channel.ID, Message: "other identity restored", SendAs: &alternatePeer, Date: baseNow()}); err != nil {
		t.Fatalf("same-owner alternate identity stayed banned: %v", err)
	}
	logs, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: created.Channel.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	var kicked, unkicked bool
	for _, event := range logs.Events {
		if event.NewParticipant == nil || event.NewParticipant.Peer != peer {
			continue
		}
		if event.Type == domain.ChannelAdminLogParticipantKick {
			kicked = true
		}
		if event.Type == domain.ChannelAdminLogParticipantUnkick {
			unkicked = true
		}
	}
	if !kicked || !unkicked {
		t.Fatalf("ban audit missing: %+v", logs.Events)
	}
	t.Run("mixed kicked pagination", func(t *testing.T) {
		ban.BannedRights = domain.ChannelBannedRights{ViewMessages: true}
		if _, err := s.EditChannelBanned(ctx, ban); err != nil {
			t.Fatal(err)
		}
		if _, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: created.Channel.ID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: member}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: baseNow()}); err != nil {
			t.Fatal(err)
		}
		page, err := s.GetParticipants(ctx, owner, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if page.Count != 2 || len(page.Participants) != 1 || page.Participants[0].UserID != member {
			t.Fatalf("mixed page = %+v", page)
		}
		filtered, err := s.GetParticipants(ctx, owner, created.Channel.ID, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked, Query: "Sender identity"}, 1, 1)
		if err != nil {
			t.Fatal(err)
		}
		if filtered.Count != 1 || len(filtered.Participants) != 0 {
			t.Fatalf("filtered empty page = %+v", filtered)
		}
	})
}

func ChannelSenderBanLinkedDiscussion(t *testing.T, s store.ChannelStore, owner int64) {
	t.Helper()
	ctx := context.Background()
	broadcast, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Linked ban broadcast", Broadcast: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	group, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Linked ban comments", Megagroup: true, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.SetDiscussionGroup(ctx, owner, broadcast.Channel.ID, group.Channel.ID); err != nil {
		t.Fatal(err)
	}
	send := func(body string, randomID int64) domain.SendChannelMessageResult {
		t.Helper()
		result, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: owner, ChannelID: broadcast.Channel.ID, Message: body, RandomID: randomID, Date: baseNow()})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	initial := send("before sender ban", 5491)
	if initial.Message.Discussion == nil || initial.Discussion == nil {
		t.Fatalf("initial linked post = %+v", initial.Message)
	}
	ban := domain.EditChannelBannedRequest{UserID: owner, ChannelID: group.Channel.ID, Participant: domain.Peer{Type: domain.PeerTypeChannel, ID: broadcast.Channel.ID}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: baseNow()}
	if _, err := s.EditChannelBanned(ctx, ban); err != nil {
		t.Fatal(err)
	}
	before, err := s.GetChannel(ctx, owner, group.Channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	blocked := send("blocked discussion injection", 5492)
	if blocked.Message.Discussion != nil || blocked.Discussion != nil {
		t.Fatalf("banned identity created discussion reference: %+v", blocked)
	}
	source, err := s.GetChannelMessages(ctx, owner, broadcast.Channel.ID, []int{blocked.Message.ID})
	if err != nil || len(source.Messages) != 1 || source.Messages[0].Body != "blocked discussion injection" {
		t.Fatalf("broadcast publication = %+v, %v", source, err)
	}
	assertNoDiscussion := func() {
		t.Helper()
		post, err := s.GetChannelMessages(ctx, owner, broadcast.Channel.ID, []int{blocked.Message.ID})
		if err != nil || len(post.Messages) != 1 || post.Messages[0].Replies != nil {
			t.Fatalf("suppressed broadcast replies = %+v, %v", post, err)
		}
		discussion, err := s.GetDiscussionMessage(ctx, owner, broadcast.Channel.ID, blocked.Message.ID)
		if err != nil || len(discussion.Messages) != 0 {
			t.Fatalf("suppressed discussion lookup = %+v, %v", discussion, err)
		}
		replies, err := s.ListChannelReplies(ctx, owner, domain.ChannelRepliesFilter{ChannelID: broadcast.Channel.ID, RootMessageID: blocked.Message.ID, Limit: 100})
		if err != nil || len(replies.Messages) != 0 {
			t.Fatalf("suppressed discussion replies = %+v, %v", replies, err)
		}
	}
	assertNoDiscussion()
	after, err := s.GetChannel(ctx, owner, group.Channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	if after.Channel.TopMessageID != before.Channel.TopMessageID || after.Channel.Pts != before.Channel.Pts {
		t.Fatal("banned identity changed the discussion message box")
	}
	history, err := s.ListChannelHistory(ctx, owner, domain.ChannelHistoryFilter{ChannelID: group.Channel.ID, Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	for _, message := range history.Messages {
		if message.Body == "blocked discussion injection" {
			t.Fatal("banned broadcast copy leaked into discussion history")
		}
	}
	ban.BannedRights = domain.ChannelBannedRights{}
	if _, err := s.EditChannelBanned(ctx, ban); err != nil {
		t.Fatal(err)
	}
	assertNoDiscussion()
	restored := send("restored discussion copy", 5493)
	if restored.Message.Discussion == nil || restored.Discussion == nil || restored.Discussion.Message.Body != "restored discussion copy" || restored.Discussion.Message.From != ban.Participant {
		t.Fatalf("unbanned linked post = %+v", restored)
	}
}

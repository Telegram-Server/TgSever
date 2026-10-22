package rpc

import (
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"

	appdialogs "telesrv/internal/app/dialogs"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func assertMessageRank(t *testing.T, message *tg.Message, want string) {
	t.Helper()
	got, present := message.GetFromRank()
	if got != want || present != (want != "") {
		t.Fatalf("message %d from_rank = %q (present=%v), want %q", message.ID, got, present, want)
	}
}

func TestChannelMessageRanksReadCurrentTagsAcrossRPCs(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(571, "15550005701", "Owner")
	member := f.user(572, "15550005702", "Member")
	outsider := f.user(573, "15550005703", "Outsider")
	first := f.createLegacyMegagroup(owner, "First rank page", member)
	second := f.createLegacyMegagroup(owner, "Second rank page", member)
	f.router.deps.Dialogs = appdialogs.NewService(memory.NewDialogStore(), f.channels)
	setRank := func(channel *tg.Channel, user domain.User, rank string) {
		t.Helper()
		_, err := f.router.onMessagesEditChatParticipantRank(f.userCtx(owner), &tg.MessagesEditChatParticipantRankRequest{Peer: inputPeerChannel(channel), Participant: &tg.InputPeerUser{UserID: user.ID, AccessHash: user.AccessHash}, Rank: rank})
		if err != nil {
			t.Fatal(err)
		}
	}
	setRank(first, owner, "captain")
	setRank(first, member, "pilot")
	setRank(second, member, "navigator")
	firstIDs := make(map[int]int64)
	for i, sender := range []domain.User{owner, member, member} {
		updates, err := f.router.onMessagesSendMessage(f.userCtx(sender), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(first), Message: "first page", RandomID: int64(5700 + i)})
		if err != nil {
			t.Fatal(err)
		}
		firstIDs[newMessageFromUpdates(t, updates).ID] = sender.ID
	}
	secondUpdates, err := f.router.onMessagesSendMessage(f.userCtx(member), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(second), Message: "second page", RandomID: 5704})
	if err != nil {
		t.Fatal(err)
	}
	secondID := newMessageFromUpdates(t, secondUpdates).ID
	dispatch := func(user domain.User, request bin.Encoder) bin.Encoder {
		t.Helper()
		var input bin.Buffer
		if err := request.Encode(&input); err != nil {
			t.Fatal(err)
		}
		out, err := f.router.Dispatch(f.userCtx(user), [8]byte{}, 1, &input)
		if err != nil {
			t.Fatal(err)
		}
		return out
	}
	for _, tag := range []string{"pilot", "updated", ""} {
		setRank(first, member, tag)
		history := dispatch(owner, &tg.MessagesGetHistoryRequest{Peer: inputPeerChannel(first), Limit: 100})
		messages, _, _ := searchMessagesPayload(t, history)
		seen := make(map[int]bool)
		for _, value := range messages {
			message, ok := value.(*tg.Message)
			if !ok {
				continue
			}
			if senderID, expected := firstIDs[message.ID]; expected {
				want := tag
				if senderID == owner.ID {
					want = "captain"
				}
				assertMessageRank(t, message, want)
				seen[message.ID] = true
			}
		}
		for id := range firstIDs {
			if !seen[id] {
				t.Fatalf("history omitted first-channel message %d", id)
			}
		}
		dialogs := dispatch(owner, &tg.MessagesGetDialogsRequest{OffsetPeer: &tg.InputPeerEmpty{}, Limit: 100})
		var tops []tg.MessageClass
		switch out := dispatchCanonicalValue(dialogs).(type) {
		case *tg.MessagesDialogs:
			tops = out.Messages
		case *tg.MessagesDialogsSlice:
			tops = out.Messages
		default:
			t.Fatalf("dialogs = %T", dispatchCanonicalValue(dialogs))
		}
		seenChannels := make(map[int64]bool)
		for _, value := range tops {
			message, ok := value.(*tg.Message)
			if !ok {
				continue
			}
			peer, ok := message.PeerID.(*tg.PeerChannel)
			if !ok {
				continue
			}
			switch peer.ChannelID {
			case first.ID:
				assertMessageRank(t, message, tag)
				seenChannels[first.ID] = true
			case second.ID:
				if message.ID != secondID {
					t.Fatalf("second dialog message = %d, want %d", message.ID, secondID)
				}
				assertMessageRank(t, message, "navigator")
				seenChannels[second.ID] = true
			}
		}
		if !seenChannels[first.ID] || !seenChannels[second.ID] {
			t.Fatalf("dialog top channels = %v", seenChannels)
		}
		difference, err := f.router.onUpdatesGetChannelDifference(f.userCtx(owner), &tg.UpdatesGetChannelDifferenceRequest{Channel: inputChannel(first), Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: domain.InitialChannelPts, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		out, ok := difference.(*tg.UpdatesChannelDifference)
		if !ok {
			t.Fatalf("channel difference = %T", difference)
		}
		seen = make(map[int]bool)
		for _, value := range out.NewMessages {
			message, ok := value.(*tg.Message)
			if !ok {
				continue
			}
			if senderID, expected := firstIDs[message.ID]; expected {
				want := tag
				if senderID == owner.ID {
					want = "captain"
				}
				assertMessageRank(t, message, want)
				seen[message.ID] = true
			}
		}
		for id := range firstIDs {
			if !seen[id] {
				t.Fatalf("difference omitted first-channel message %d", id)
			}
		}
	}
	setRank(first, member, "departing")
	if _, err := f.channels.LeaveChannel(f.ctx, first.ID, member.ID, int(f.router.clock.Now().Unix())); err != nil {
		t.Fatal(err)
	}
	history := dispatch(owner, &tg.MessagesGetHistoryRequest{Peer: inputPeerChannel(first), Limit: 100})
	messages, _, _ := searchMessagesPayload(t, history)
	seen := make(map[int]bool)
	for _, value := range messages {
		if message, ok := value.(*tg.Message); ok && firstIDs[message.ID] == member.ID {
			assertMessageRank(t, message, "")
			seen[message.ID] = true
		}
	}
	for id, senderID := range firstIDs {
		if senderID == member.ID && !seen[id] {
			t.Fatalf("departed author's visible message %d was lost", id)
		}
	}
	var input bin.Buffer
	if err := (&tg.MessagesGetHistoryRequest{Peer: inputPeerChannel(first), Limit: 100}).Encode(&input); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.Dispatch(f.userCtx(outsider), [8]byte{}, 1, &input); !tgerr.Is(err, "CHANNEL_PRIVATE") {
		t.Fatalf("unauthorized rank-bearing history error = %v", err)
	}
}

func TestChannelMessageRanksFanoutUsesFreshMetadataPerEvent(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(581, "15550005801", "Owner")
	member := f.user(582, "15550005802", "Member")
	viewer := f.user(583, "15550005803", "Viewer")
	group := f.createLegacyMegagroup(owner, "Rank fanout", member, viewer)
	sessions := newNudgeSessions([]int64{owner.ID, member.ID, viewer.ID})
	sessions.channelMembers = map[int64][]int64{group.ID: {owner.ID, member.ID, viewer.ID}}
	f.router.deps.Sessions = sessions
	messageID := 0
	for i, rank := range []string{"pilot", "captain", ""} {
		if _, err := f.router.onMessagesEditChatParticipantRank(f.userCtx(owner), &tg.MessagesEditChatParticipantRankRequest{Peer: inputPeerChannel(group), Participant: &tg.InputPeerUser{UserID: member.ID, AccessHash: member.AccessHash}, Rank: rank}); err != nil {
			t.Fatal(err)
		}
		var updates tg.UpdatesClass
		var err error
		if i == 0 {
			updates, err = f.router.onMessagesSendMessage(f.userCtx(member), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(group), Message: "original", RandomID: 5801})
			if err == nil {
				messageID = newMessageFromUpdates(t, updates).ID
			}
		} else {
			request := &tg.MessagesEditMessageRequest{Peer: inputPeerChannel(group), ID: messageID}
			request.SetMessage("revision " + rank)
			updates, err = f.router.onMessagesEditMessage(f.userCtx(member), request)
		}
		if err != nil {
			t.Fatal(err)
		}
		for _, recipient := range []domain.User{member, owner, viewer} {
			var envelope *tg.Updates
			if recipient.ID == member.ID {
				envelope, _ = updates.(*tg.Updates)
			} else {
				envelope, _ = sessions.msgFor(recipient.ID).(*tg.Updates)
			}
			if envelope == nil {
				t.Fatalf("recipient %d has no message envelope", recipient.ID)
			}
			found := false
			for _, update := range envelope.Updates {
				var value tg.MessageClass
				switch u := update.(type) {
				case *tg.UpdateNewChannelMessage:
					value = u.Message
				case *tg.UpdateEditChannelMessage:
					value = u.Message
				}
				message, ok := value.(*tg.Message)
				if !ok || message.ID != messageID {
					continue
				}
				assertMessageRank(t, message, rank)
				if message.Out != (recipient.ID == member.ID) {
					t.Fatalf("recipient %d out=%v: viewer-specific message was reused", recipient.ID, message.Out)
				}
				found = true
			}
			if !found {
				t.Fatalf("recipient %d omitted message %d", recipient.ID, messageID)
			}
		}
		if i > 0 {
			difference, err := f.router.onUpdatesGetChannelDifference(f.userCtx(owner), &tg.UpdatesGetChannelDifferenceRequest{Channel: inputChannel(group), Filter: &tg.ChannelMessagesFilterEmpty{}, Pts: domain.InitialChannelPts, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			out, ok := difference.(*tg.UpdatesChannelDifference)
			if !ok {
				t.Fatalf("edit difference = %T", difference)
			}
			found := false
			for _, update := range out.OtherUpdates {
				if edit, ok := update.(*tg.UpdateEditChannelMessage); ok {
					if message, ok := edit.Message.(*tg.Message); ok && message.ID == messageID {
						assertMessageRank(t, message, rank)
						found = true
					}
				}
			}
			if !found {
				t.Fatalf("difference omitted edited message %d", messageID)
			}
		}
	}
}

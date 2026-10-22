package rpc

import (
	"strings"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

func TestChannelSenderBansRPC(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(541, "15550005401", "Owner")
	actor, err := f.users.Create(f.ctx, domain.User{AccessHash: 542, Phone: "15550005402", FirstName: "Sender", PremiumUntil: int(time.Now().Add(time.Hour).Unix())})
	if err != nil {
		t.Fatal(err)
	}
	bot, err := f.users.Create(f.ctx, domain.User{AccessHash: 543, Phone: "15550005403", FirstName: "Moderator", Bot: true, BotInfoVersion: 1})
	if err != nil {
		t.Fatal(err)
	}
	group := f.createLegacyMegagroup(owner, "Sender bans", actor, bot)
	sender := createBroadcastChannelRPC(t, f.router, actor.ID, "Sender identity")
	f.router.deps.Files = albumFakeFiles()
	if _, err := f.router.onChannelsEditAdmin(f.userCtx(owner), &tg.ChannelsEditAdminRequest{Channel: inputChannel(group), UserID: inputUser(bot), AdminRights: tg.ChatAdminRights{BanUsers: true}}); err != nil {
		t.Fatal(err)
	}
	dialogs := memory.NewDialogStore()
	f.router.deps.Messages = appmessages.NewService(memory.NewMessageStore(dialogs), dialogs)
	edit := func(user domain.User, rights tg.ChatBannedRights) error {
		_, err := f.router.onChannelsEditBanned(f.userCtx(user), &tg.ChannelsEditBannedRequest{Channel: inputChannel(group), Participant: inputPeerChannel(sender), BannedRights: rights})
		return err
	}
	if err := edit(actor, tg.ChatBannedRights{ViewMessages: true}); err == nil || !strings.Contains(err.Error(), "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("member ban = %v", err)
	}
	if err := edit(bot, tg.ChatBannedRights{SendMessages: true}); err == nil {
		t.Fatal("partial channel ban accepted")
	}
	if err := edit(bot, tg.ChatBannedRights{ViewMessages: true, UntilDate: int(time.Now().Add(24 * time.Hour).Unix())}); err == nil {
		t.Fatal("temporary channel ban accepted")
	}
	if _, err := f.router.onChannelsGetFullChannel(f.userCtx(owner), inputChannel(sender)); err == nil {
		t.Fatal("ordinary lookup exposed another owner's private channel")
	}
	sourceUpdates, err := f.router.onMessagesSendMessage(f.userCtx(actor), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(group), Message: "source", RandomID: 5400})
	if err != nil {
		t.Fatal(err)
	}
	source := newMessageFromUpdates(t, sourceUpdates)
	calls := []struct {
		name  string
		send  func(int64) (tg.UpdatesClass, error)
		count int
	}{
		{"text", func(id int64) (tg.UpdatesClass, error) {
			return f.router.onMessagesSendMessage(f.userCtx(actor), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(group), SendAs: inputPeerChannel(sender), Message: "text", RandomID: id})
		}, 1},
		{"media", func(id int64) (tg.UpdatesClass, error) {
			return f.router.onMessagesSendMedia(f.userCtx(actor), &tg.MessagesSendMediaRequest{Peer: inputPeerChannel(group), SendAs: inputPeerChannel(sender), Media: &tg.InputMediaGeoPoint{GeoPoint: &tg.InputGeoPoint{Lat: 1, Long: 1}}, RandomID: id})
		}, 1},
		{"album", func(id int64) (tg.UpdatesClass, error) {
			return f.router.onMessagesSendMultiMedia(f.userCtx(actor), &tg.MessagesSendMultiMediaRequest{Peer: inputPeerChannel(group), SendAs: inputPeerChannel(sender), MultiMedia: albumInputItems(id)})
		}, 3},
		{"forward", func(id int64) (tg.UpdatesClass, error) {
			return f.router.onMessagesForwardMessages(f.userCtx(actor), &tg.MessagesForwardMessagesRequest{FromPeer: inputPeerChannel(group), ToPeer: inputPeerChannel(group), SendAs: inputPeerChannel(sender), ID: []int{source.ID}, RandomID: []int64{id}})
		}, 1},
	}
	for i, tc := range calls {
		t.Run(tc.name, func(t *testing.T) {
			if err := edit(bot, tg.ChatBannedRights{ViewMessages: true}); err != nil {
				t.Fatal(err)
			}
			if _, err := tc.send(int64(5410 + i*10)); err == nil || !strings.Contains(err.Error(), "SEND_AS_PEER_INVALID") {
				t.Fatalf("banned send = %v", err)
			}
			single, err := f.router.onChannelsGetParticipant(f.userCtx(bot), &tg.ChannelsGetParticipantRequest{Channel: inputChannel(group), Participant: inputPeerChannel(sender)})
			if err != nil {
				t.Fatal(err)
			}
			banned, ok := single.Participant.(*tg.ChannelParticipantBanned)
			if !ok || !banned.Left || !banned.BannedRights.ViewMessages || banned.Peer.(*tg.PeerChannel).ChannelID != sender.ID {
				t.Fatalf("sender participant = %#v", single.Participant)
			}
			assertBannedSenderIdentity(t, single.Chats, sender)
			listed, err := f.router.onChannelsGetParticipants(f.userCtx(bot), &tg.ChannelsGetParticipantsRequest{Channel: inputChannel(group), Filter: &tg.ChannelParticipantsKicked{}, Limit: 100})
			if err != nil {
				t.Fatal(err)
			}
			list := listed.(*tg.ChannelsChannelParticipants)
			if list.Count != 1 || len(list.Participants) != 1 {
				t.Fatalf("sender kicked list = %#v", list)
			}
			assertBannedSenderIdentity(t, list.Chats, sender)
			if err := edit(bot, tg.ChatBannedRights{}); err != nil {
				t.Fatal(err)
			}
			updates, err := tc.send(int64(5410 + i*10))
			if err != nil {
				t.Fatal(err)
			}
			messages := channelMessagesFromUpdates(updates)
			if len(messages) != tc.count {
				t.Fatalf("restored messages = %d, want %d", len(messages), tc.count)
			}
			for _, value := range messages {
				message := value.(*tg.Message)
				if from, ok := message.FromID.(*tg.PeerChannel); !ok || from.ChannelID != sender.ID {
					t.Fatalf("restored author = %#v", message.FromID)
				}
			}
		})
	}
	logs, err := f.router.onChannelsGetAdminLog(f.userCtx(bot), &tg.ChannelsGetAdminLogRequest{Channel: inputChannel(group), Limit: 100})
	if err != nil {
		t.Fatal(err)
	}
	bans, unbans := 0, 0
	for _, event := range logs.Events {
		action, ok := event.Action.(*tg.ChannelAdminLogEventActionParticipantToggleBan)
		if !ok {
			continue
		}
		if next, ok := action.NewParticipant.(*tg.ChannelParticipantBanned); ok && next.Peer.(*tg.PeerChannel).ChannelID == sender.ID {
			bans++
		}
		if next, ok := action.NewParticipant.(*tg.ChannelParticipantLeft); ok && next.Peer.(*tg.PeerChannel).ChannelID == sender.ID {
			unbans++
		}
	}
	if bans != 4 || unbans != 4 {
		t.Fatalf("audit bans/unbans = %d/%d", bans, unbans)
	}
}

func assertBannedSenderIdentity(t *testing.T, chats []tg.ChatClass, sender *tg.Channel) {
	t.Helper()
	for _, chat := range chats {
		if channel, ok := chat.(*tg.Channel); ok && channel.ID == sender.ID {
			hash, present := channel.GetAccessHash()
			if channel.Title != sender.Title || !present || hash != sender.AccessHash || channel.Broadcast != sender.Broadcast {
				t.Fatalf("banned sender identity = %+v", channel)
			}
			return
		}
	}
	t.Fatalf("banned private sender %d missing from Chats", sender.ID)
}

func TestChannelSenderBanLinkedDiscussionRPC(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(549, "15550005491", "Owner")
	observer := f.user(550, "15550005492", "Observer")
	group := f.createLegacyMegagroup(owner, "Linked sender ban", observer)
	broadcast := createBroadcastChannelRPC(t, f.router, owner.ID, "Linked broadcast")
	if ok, err := f.router.onChannelsSetDiscussionGroup(f.userCtx(owner), &tg.ChannelsSetDiscussionGroupRequest{Broadcast: inputChannel(broadcast), Group: inputChannel(group)}); err != nil || !ok {
		t.Fatalf("link = %v, %v", ok, err)
	}
	send := func(body string, randomID int64) *tg.Message {
		t.Helper()
		updates, err := f.router.onMessagesSendMessage(f.userCtx(owner), &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(broadcast), Message: body, RandomID: randomID})
		if err != nil {
			t.Fatal(err)
		}
		return newMessageFromUpdates(t, updates)
	}
	before := send("before identity ban", 54901)
	if _, present := before.GetReplies(); !present {
		t.Fatal("initial broadcast has no linked comments")
	}
	edit := func(rights tg.ChatBannedRights) {
		t.Helper()
		if _, err := f.router.onChannelsEditBanned(f.userCtx(owner), &tg.ChannelsEditBannedRequest{Channel: inputChannel(group), Participant: inputPeerChannel(broadcast), BannedRights: rights}); err != nil {
			t.Fatal(err)
		}
	}
	history := func() []tg.MessageClass {
		t.Helper()
		result := dispatchMessagesPayload(t, f.router, f.userCtx(observer), &tg.MessagesGetHistoryRequest{Peer: inputPeerChannel(group), Limit: 100})
		messages, _, _ := searchMessagesPayload(t, result)
		return messages
	}
	edit(tg.ChatBannedRights{ViewMessages: true})
	blocked := send("blocked automatic injection", 54902)
	if blocked.Message != "blocked automatic injection" || !blocked.Post {
		t.Fatalf("broadcast publication lost: %+v", blocked)
	}
	if _, present := blocked.GetReplies(); present {
		t.Fatal("banned identity broadcast advertises a discussion copy")
	}
	assertNoDiscussion := func() {
		t.Helper()
		discussion, err := f.router.onMessagesGetDiscussionMessage(f.userCtx(owner), &tg.MessagesGetDiscussionMessageRequest{Peer: inputPeerChannel(broadcast), MsgID: blocked.ID})
		if err != nil || len(discussion.Messages) != 0 {
			t.Fatalf("suppressed discussion lookup = %+v, %v", discussion, err)
		}
		replies, err := f.router.onMessagesGetReplies(f.userCtx(owner), &tg.MessagesGetRepliesRequest{Peer: inputPeerChannel(broadcast), MsgID: blocked.ID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		items, _, _ := searchMessagesPayload(t, replies)
		if len(items) != 0 {
			t.Fatalf("suppressed discussion replies = %+v", items)
		}
		source, err := f.router.onChannelsGetMessages(f.userCtx(owner), &tg.ChannelsGetMessagesRequest{Channel: inputChannel(broadcast), ID: []tg.InputMessageClass{&tg.InputMessageID{ID: blocked.ID}}})
		if err != nil {
			t.Fatal(err)
		}
		posts, _, _ := searchMessagesPayload(t, source)
		if len(posts) != 1 {
			t.Fatalf("suppressed source post = %+v", posts)
		}
		if _, present := posts[0].(*tg.Message).GetReplies(); present {
			t.Fatal("history invented a suppressed discussion root")
		}
	}
	assertNoDiscussion()
	for _, value := range history() {
		if message, ok := value.(*tg.Message); ok && message.Message == "blocked automatic injection" {
			t.Fatal("banned channel injected an automatic discussion message")
		}
	}
	edit(tg.ChatBannedRights{})
	assertNoDiscussion()
	restored := send("restored automatic discussion", 54903)
	if _, present := restored.GetReplies(); !present {
		t.Fatal("unbanned broadcast did not regain linked comments")
	}
	found := false
	for _, value := range history() {
		if message, ok := value.(*tg.Message); ok && message.Message == "restored automatic discussion" {
			from, ok := message.FromID.(*tg.PeerChannel)
			if !ok || from.ChannelID != broadcast.ID {
				t.Fatalf("discussion identity = %#v", message.FromID)
			}
			found = true
		}
	}
	if !found {
		t.Fatal("unbanned channel's discussion copy missing")
	}
}

func TestEmptyChannelUnbanRPCDoesNotNotify(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(551, "15550005501", "Owner")
	member := f.user(552, "15550005502", "Member")
	group := f.createLegacyMegagroup(owner, "Idempotent unban", member)
	sender := createBroadcastChannelRPC(t, f.router, member.ID, "Sender")
	sessions := &captureSessions{onlineUserIDs: []int64{owner.ID, member.ID}}
	f.router.deps.Sessions = sessions
	edit := func(rights tg.ChatBannedRights) tg.UpdatesClass {
		t.Helper()
		result, err := f.router.onChannelsEditBanned(f.userCtx(owner), &tg.ChannelsEditBannedRequest{Channel: inputChannel(group), Participant: inputPeerChannel(sender), BannedRights: rights})
		if err != nil {
			t.Fatal(err)
		}
		return result
	}
	assertEmpty := func() {
		t.Helper()
		sessions.clearMessages()
		result := edit(tg.ChatBannedRights{})
		updates, ok := result.(*tg.Updates)
		if !ok || len(updates.Updates) != 0 {
			t.Fatalf("empty unban updates = %#v", result)
		}
		if ids := sessions.pushedUserIDs(); len(ids) != 0 {
			t.Fatalf("empty unban notified viewers: %v", ids)
		}
	}
	assertEmpty()
	edit(tg.ChatBannedRights{ViewMessages: true})
	edit(tg.ChatBannedRights{})
	assertEmpty()
}

package rpc

import (
	"slices"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/app/push"
	"telesrv/internal/domain"
)

// sendPrivateAsBot 以机器人身份给 to 发一条私聊消息，返回机器人消息盒里的 id。
func (f botAPIBridgeFixture) sendPrivateAsBot(t *testing.T, to domain.User, text string, randomID int64) int {
	t.Helper()
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSendMessageRequest{
		Peer: &tg.InputPeerUser{UserID: to.ID, AccessHash: to.AccessHash}, Message: text, RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("bot send to %d: %v", to.ID, err)
	}
	ids := botAPIUpdateMessageIDs(domain.Peer{Type: domain.PeerTypeUser, ID: to.ID}, updates)
	if len(ids) != 1 {
		t.Fatalf("sent ids = %v", ids)
	}
	return ids[0]
}

// privateHistory 返回 owner 消息盒里与 peer 的私聊消息正文。
func (f botAPIBridgeFixture) privateHistory(t *testing.T, ownerID, peerID int64) []string {
	t.Helper()
	list, err := f.messages.GetHistory(f.ctx, ownerID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: peerID}, Limit: 20,
	})
	if err != nil {
		t.Fatalf("GetHistory(%d, %d): %v", ownerID, peerID, err)
	}
	var out []string
	for _, msg := range list.Messages {
		out = append(out, msg.Body)
	}
	return out
}

// chat_id 与消息所在会话不符时，任何会话里的消息都不能被删除。
func TestBotAPIDeleteMessageRejectsAMessageFromAnotherChat(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	toMember := f.sendPrivateAsBot(t, f.member, "for ann", 701)
	f.sendPrivateAsBot(t, f.owner, "for owner", 702)

	ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.owner.ID, toMember)
	if ok {
		t.Fatal("BotAPIDeleteMessage reported success for a message outside chat_id")
	}
	wantBotAPIError(t, err, "MESSAGE_ID_INVALID")
	if got := f.privateHistory(t, f.member.ID, f.bot.ID); !slices.Equal(got, []string{"for ann"}) {
		t.Fatalf("member history = %v, want the bot's message intact", got)
	}
	if got := f.privateHistory(t, f.owner.ID, f.bot.ID); !slices.Equal(got, []string{"for owner"}) {
		t.Fatalf("owner history = %v, want the bot's message intact", got)
	}
}

func TestBotAPIDeleteMessageReportsMissingMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	f.sendPrivateAsBot(t, f.member, "hello", 711)

	for name, chatID := range map[string]int64{"private": f.member.ID, "group": f.chatID} {
		t.Run(name, func(t *testing.T) {
			ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, chatID, 9_999)
			if ok {
				t.Fatal("BotAPIDeleteMessage reported success for a message that does not exist")
			}
			wantBotAPIError(t, err, "MESSAGE_ID_INVALID")
		})
	}
}

// 私聊删除仍对对端 revoke：两边的消息盒都不再有这条消息。
func TestBotAPIDeleteMessageRevokesPrivateMessageForThePeer(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	id := f.sendPrivateAsBot(t, f.member, "stale menu", 721)

	if ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.member.ID, id); err != nil || !ok {
		t.Fatalf("BotAPIDeleteMessage = %v, %v", ok, err)
	}
	if got := f.privateHistory(t, f.member.ID, f.bot.ID); len(got) != 0 {
		t.Fatalf("member history = %v, want the message revoked", got)
	}
	if got := f.privateHistory(t, f.bot.ID, f.member.ID); len(got) != 0 {
		t.Fatalf("bot history = %v, want the message deleted", got)
	}
}

type recordingDeleteNotifier struct{ deletes []push.DeletedMessages }

func (n *recordingDeleteNotifier) DeleteMessages(d push.DeletedMessages) bool {
	n.deletes = append(n.deletes, d)
	return true
}

// 私聊删除和用户自己删除一样同步清掉对端设备通知栏里的那条通知。
func TestBotAPIDeleteMessageClearsThePeersNotification(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	notifier := &recordingDeleteNotifier{}
	f.router.SetPushDeleteNotifier(notifier)
	id := f.sendPrivateAsBot(t, f.member, "stale menu", 725)
	list, err := f.messages.GetHistory(f.ctx, f.member.ID, domain.MessageFilter{
		HasPeer: true, Peer: domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}, Limit: 20,
	})
	if err != nil || len(list.Messages) != 1 {
		t.Fatalf("member history = %+v, %v", list.Messages, err)
	}
	memberCopy := list.Messages[0].ID

	if ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.member.ID, id); err != nil || !ok {
		t.Fatalf("BotAPIDeleteMessage = %v, %v", ok, err)
	}
	var got []int
	for _, d := range notifier.deletes {
		if d.UserID == f.member.ID && d.Chat == (push.Chat{Kind: push.ChatPrivate, ID: f.bot.ID}) {
			got = append(got, d.MessageIDs...)
		}
	}
	if !slices.Equal(got, []int{memberCopy}) {
		t.Fatalf("push deletes = %+v, want the member's copy of the message cleared", notifier.deletes)
	}
}

func TestBotAPIDeleteMessageInGroup(t *testing.T) {
	t.Run("admin with the delete right removes anyone's message", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
		spam := f.sendAs(t, f.member.ID, "spam", 731)
		owners := f.sendAs(t, f.owner.ID, "owner note", 732)

		for _, id := range []int{spam, owners} {
			if ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, id); err != nil || !ok {
				t.Fatalf("BotAPIDeleteMessage(%d) = %v, %v", id, ok, err)
			}
		}
		if got := f.visibleMessageIDs(t, []int{spam, owners}); len(got) != 0 {
			t.Fatalf("visible messages = %v, want both deleted", got)
		}
	})

	t.Run("own message needs no admin right", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		own := f.sendAs(t, f.bot.ID, "bot reply", 741)

		if ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, own); err != nil || !ok {
			t.Fatalf("BotAPIDeleteMessage = %v, %v", ok, err)
		}
		if got := f.visibleMessageIDs(t, []int{own}); len(got) != 0 {
			t.Fatalf("visible messages = %v, want the bot's message deleted", got)
		}
	})

	t.Run("non-admin cannot remove someone else's message", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		other := f.sendAs(t, f.member.ID, "hello", 751)

		ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, other)
		if ok {
			t.Fatal("BotAPIDeleteMessage reported success without the delete right")
		}
		wantBotAPIError(t, err, "MESSAGE_DELETE_FORBIDDEN")
		if got := f.visibleMessageIDs(t, []int{other}); !slices.Equal(got, []int{other}) {
			t.Fatalf("visible messages = %v, want %d kept", got, other)
		}
	})

	t.Run("group creation service message cannot be deleted", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})

		_, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, 1)
		wantBotAPIError(t, err, "MESSAGE_DELETE_FORBIDDEN")
	})

	t.Run("a group the bot left is an invalid chat", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		own := f.sendAs(t, f.bot.ID, "bye", 771)
		if err := f.router.BotAPILeaveChat(f.ctx, f.bot.ID, f.chatID); err != nil {
			t.Fatalf("BotAPILeaveChat: %v", err)
		}

		_, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, own)
		wantBotAPIError(t, err, "CHAT_ID_INVALID")
	})

	t.Run("admin without the delete right cannot remove someone else's message", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{PinMessages: true})
		other := f.sendAs(t, f.member.ID, "hello", 761)

		_, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, other)
		wantBotAPIError(t, err, "MESSAGE_DELETE_FORBIDDEN")
		if got := f.visibleMessageIDs(t, []int{other}); !slices.Equal(got, []int{other}) {
			t.Fatalf("visible messages = %v, want %d kept", got, other)
		}
	})
}

// 群里删除后，群成员的会话像真人管理员删除时一样收到删除更新。
func TestBotAPIDeleteMessageInGroupNotifiesMembers(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	spam := f.sendAs(t, f.member.ID, "spam", 781)
	f.sessions.clearMessages()

	if ok, err := f.router.BotAPIDeleteMessage(f.ctx, f.bot.ID, f.chatID, spam); err != nil || !ok {
		t.Fatalf("BotAPIDeleteMessage = %v, %v", ok, err)
	}
	pushed := f.sessions.pushedUserIDs()
	for _, userID := range []int64{f.owner.ID, f.member.ID} {
		if !slices.Contains(pushed, userID) {
			t.Fatalf("pushed to %v, want user %d to receive the deletion", pushed, userID)
		}
	}
	updates, ok := f.sessions.lastUserPush().(*tg.Updates)
	if !ok {
		t.Fatalf("last push = %T, want *tg.Updates", f.sessions.lastUserPush())
	}
	var deleted []int
	for _, update := range updates.Updates {
		if u, ok := update.(*tg.UpdateDeleteChannelMessages); ok && u.ChannelID == f.channel.ID {
			deleted = append(deleted, u.Messages...)
		}
	}
	if !slices.Equal(deleted, []int{spam}) {
		t.Fatalf("pushed updates = %+v, want updateDeleteChannelMessages for %d", updates.Updates, spam)
	}
}

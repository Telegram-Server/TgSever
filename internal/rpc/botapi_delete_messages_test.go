package rpc

import (
	"context"
	"slices"
	"testing"

	"github.com/iamxvbaba/td/tg"

	appchannels "telesrv/internal/app/channels"
	"telesrv/internal/domain"
)

// channelDeletePushes 返回推给 userID 的每条 updateDeleteChannelMessages 携带的 id，
// 一次推送一组：用来断言一批删除只推一次更新。
func (f botAPIBridgeFixture) channelDeletePushes(userID int64) [][]int {
	var out [][]int
	for _, push := range f.sessions.pushesTo(userID) {
		updates, ok := push.(*tg.Updates)
		if !ok {
			continue
		}
		for _, update := range updates.Updates {
			if u, ok := update.(*tg.UpdateDeleteChannelMessages); ok && u.ChannelID == f.channel.ID {
				ids := append([]int(nil), u.Messages...)
				slices.Sort(ids)
				out = append(out, ids)
			}
		}
	}
	return out
}

func TestBotAPIDeleteMessagesInGroupDeletesAllInOneUpdate(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
	ids := []int{
		f.sendAs(t, f.member.ID, "spam 1", 801),
		f.sendAs(t, f.member.ID, "spam 2", 802),
		f.sendAs(t, f.owner.ID, "owner note", 803),
	}
	f.sessions.clearMessages()

	if err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.chatID, ids); err != nil {
		t.Fatalf("BotAPIDeleteMessages: %v", err)
	}
	if got := f.visibleMessageIDs(t, ids); len(got) != 0 {
		t.Fatalf("visible messages = %v, want all deleted", got)
	}
	for _, userID := range []int64{f.owner.ID, f.member.ID} {
		if got := f.channelDeletePushes(userID); len(got) != 1 || !slices.Equal(got[0], ids) {
			t.Fatalf("user %d delete pushes = %v, want one update with %v", userID, got, ids)
		}
	}
}

// 没有删除权的 bot 批量删除时，别人的消息和不存在的 id 被跳过，自己的照删，
// 成员只收到一次合并的删除更新。
func TestBotAPIDeleteMessagesInGroupDeletesWhatItCan(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	own1 := f.sendAs(t, f.bot.ID, "bot reply 1", 811)
	other := f.sendAs(t, f.member.ID, "member text", 812)
	own2 := f.sendAs(t, f.bot.ID, "bot reply 2", 813)
	f.sessions.clearMessages()

	if err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.chatID, []int{1, own1, other, own2, 9_999}); err != nil {
		t.Fatalf("BotAPIDeleteMessages: %v", err)
	}
	if got := f.visibleMessageIDs(t, []int{own1, other, own2}); !slices.Equal(got, []int{other}) {
		t.Fatalf("visible messages = %v, want only %d kept", got, other)
	}
	if got := f.channelDeletePushes(f.member.ID); len(got) != 1 || !slices.Equal(got[0], []int{own1, own2}) {
		t.Fatalf("member delete pushes = %v, want one update with [%d %d]", got, own1, own2)
	}
}

func TestBotAPIDeleteMessagesInGroupFailsWhenNothingCanBeDeleted(t *testing.T) {
	t.Run("only other people's messages", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		other := f.sendAs(t, f.member.ID, "member text", 821)
		f.sessions.clearMessages()

		err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.chatID, []int{other, 9_999})
		wantBotAPIError(t, err, "MESSAGE_DELETE_FORBIDDEN")
		if got := f.visibleMessageIDs(t, []int{other}); !slices.Equal(got, []int{other}) {
			t.Fatalf("visible messages = %v, want %d kept", got, other)
		}
		if got := f.channelDeletePushes(f.member.ID); len(got) != 0 {
			t.Fatalf("member delete pushes = %v, want none", got)
		}
	})

	t.Run("only missing messages", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})

		err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.chatID, []int{9_998, 9_999})
		wantBotAPIError(t, err, "MESSAGE_ID_INVALID")
	})

	t.Run("only the group creation message", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})

		err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.chatID, []int{1, 9_999})
		wantBotAPIError(t, err, "MESSAGE_DELETE_FORBIDDEN")
	})

	// 机器人看不到 id=1 时，带上它也只是"找不到"（review #7）。
	t.Run("creation message not visible", func(t *testing.T) {
		f := newBotAPIBridgeFixture(t)
		f.promoteBot(t, domain.ChannelAdminRights{DeleteMessages: true})
		f.router.deps.Channels = hiddenCreationMessage{f.channels}

		err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.chatID, []int{1, 9_999})
		wantBotAPIError(t, err, "MESSAGE_ID_INVALID")
	})
}

// hiddenCreationMessage 让 id=1 的建群服务消息读不到，其余能力委托给真实服务。
type hiddenCreationMessage struct{ *appchannels.Service }

func (h hiddenCreationMessage) GetMessages(ctx context.Context, userID, channelID int64, ids []int) (domain.ChannelHistory, error) {
	list, err := h.Service.GetMessages(ctx, userID, channelID, ids)
	list.Messages = slices.DeleteFunc(list.Messages, func(msg domain.ChannelMessage) bool { return msg.ID == 1 })
	return list, err
}

// 私聊只删 chat_id 会话里的消息；别的会话里同一消息盒的 id 不受影响。
func TestBotAPIDeleteMessagesInPrivateChatStaysInTheChat(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	toMember1 := f.sendPrivateAsBot(t, f.member, "for ann 1", 831)
	toOwner := f.sendPrivateAsBot(t, f.owner, "for owner", 832)
	toMember2 := f.sendPrivateAsBot(t, f.member, "for ann 2", 833)

	if err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.member.ID, []int{toMember1, toOwner, toMember2, 9_999}); err != nil {
		t.Fatalf("BotAPIDeleteMessages: %v", err)
	}
	if got := f.privateHistory(t, f.member.ID, f.bot.ID); len(got) != 0 {
		t.Fatalf("member history = %v, want both messages revoked", got)
	}
	if got := f.privateHistory(t, f.owner.ID, f.bot.ID); !slices.Equal(got, []string{"for owner"}) {
		t.Fatalf("owner history = %v, want the bot's message intact", got)
	}
}

func TestBotAPIDeleteMessagesInPrivateChatFailsWhenNoneBelongToTheChat(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	toOwner := f.sendPrivateAsBot(t, f.owner, "for owner", 841)

	err := f.router.BotAPIDeleteMessages(f.ctx, f.bot.ID, f.member.ID, []int{toOwner, 9_999})
	wantBotAPIError(t, err, "MESSAGE_ID_INVALID")
	if got := f.privateHistory(t, f.owner.ID, f.bot.ID); !slices.Equal(got, []string{"for owner"}) {
		t.Fatalf("owner history = %v, want the bot's message intact", got)
	}
}

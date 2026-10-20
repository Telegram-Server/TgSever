package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"testing"
)

func TestPersonalChannelHistoryBotRPC(t *testing.T) {
	f := newInlineBotRPCTestFixture(t)
	ctx := context.Background()
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)
	result, err := f.router.onChannelsCreateChannel(ownerCtx, &tg.ChannelsCreateChannelRequest{Title: "Personal history", Broadcast: true})
	if err != nil {
		t.Fatal(err)
	}
	channel := result.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := f.router.onChannelsUpdateUsername(ownerCtx, &tg.ChannelsUpdateUsernameRequest{Channel: inputChannel(channel), Username: "personal_history_rpc"}); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.onAccountUpdatePersonalChannel(ownerCtx, inputChannel(channel)); err != nil {
		t.Fatal(err)
	}
	ids := make([]int, 0, 3)
	for i := range 3 {
		sent, err := f.router.onMessagesSendMessage(ownerCtx, &tg.MessagesSendMessageRequest{Peer: inputPeerChannel(channel), Message: []string{"first", "second", "third"}[i], RandomID: int64(57301 + i)})
		if err != nil {
			t.Fatal(err)
		}
		ids = append(ids, newMessageFromUpdates(t, sent).ID)
	}
	t.Run("bot reads recent messages without joining", func(t *testing.T) {
		result := dispatchMessagesPayload(t, f.router, botCtx, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: inputUser(f.owner), Limit: 2})
		messages, chats, _ := searchMessagesPayload(t, result)
		if len(messages) != 2 || messages[0].(*tg.Message).ID != ids[2] || messages[1].(*tg.Message).ID != ids[1] {
			t.Fatalf("personal history = %+v", messages)
		}
		if len(chats) != 1 || chats[0].(*tg.Channel).ID != channel.ID {
			t.Fatalf("history chats = %+v", chats)
		}
	})
	t.Run("ordinary channel history keeps bot privacy filtering", func(t *testing.T) {
		result := dispatchMessagesPayload(t, f.router, botCtx, &tg.MessagesGetHistoryRequest{Peer: inputPeerChannel(channel), Limit: 10})
		messages, _, _ := searchMessagesPayload(t, result)
		if len(messages) != 0 {
			t.Fatalf("ordinary bot history exposed unmentioned posts: %+v", messages)
		}
	})
	t.Run("message bounds select only the middle post", func(t *testing.T) {
		result, err := f.router.onMessagesGetPersonalChannelHistory(botCtx, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: inputUser(f.owner), Limit: 10, MaxID: ids[2], MinID: ids[0]})
		if err != nil {
			t.Fatal(err)
		}
		messages, _, _ := searchMessagesPayload(t, result)
		if len(messages) != 1 || messages[0].(*tg.Message).ID != ids[1] {
			t.Fatalf("bounded personal history = %+v", messages)
		}
	})
	t.Run("only bots may read through this method", func(t *testing.T) {
		if _, err := f.router.onMessagesGetPersonalChannelHistory(ownerCtx, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: inputUser(f.owner), Limit: 1}); !tgerr.Is(err, "USER_BOT_REQUIRED") {
			t.Fatalf("ordinary user history = %v", err)
		}
	})
	t.Run("invalid user access hash rejected", func(t *testing.T) {
		bad := inputUser(f.owner)
		bad.AccessHash++
		if _, err := f.router.onMessagesGetPersonalChannelHistory(botCtx, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: bad, Limit: 1}); !tgerr.Is(err, "USER_ID_INVALID") {
			t.Fatalf("invalid target = %v", err)
		}
	})
	t.Run("no personal channel returns empty", func(t *testing.T) {
		result, err := f.router.onMessagesGetPersonalChannelHistory(botCtx, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: inputUser(f.peer), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		messages, chats, _ := searchMessagesPayload(t, result)
		if len(messages) != 0 || len(chats) != 0 {
			t.Fatalf("unconfigured history = %+v chats=%+v", messages, chats)
		}
	})
	t.Run("cleared association no longer exposes channel history", func(t *testing.T) {
		if _, err := f.router.onAccountUpdatePersonalChannel(ownerCtx, &tg.InputChannelEmpty{}); err != nil {
			t.Fatal(err)
		}
		result, err := f.router.onMessagesGetPersonalChannelHistory(botCtx, &tg.MessagesGetPersonalChannelHistoryRequest{UserID: inputUser(f.owner), Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		messages, chats, _ := searchMessagesPayload(t, result)
		if len(messages) != 0 || len(chats) != 0 {
			t.Fatalf("cleared history = %+v chats=%+v", messages, chats)
		}
	})
}

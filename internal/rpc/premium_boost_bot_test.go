package rpc

import (
	"context"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"telesrv/internal/domain"
	"testing"
	"time"
)

func TestBotAdminReadsUserBoostsRPC(t *testing.T) {
	f := newInlineBotRPCTestFixture(t)
	ctx := context.Background()
	ownerCtx := WithUserID(ctx, f.owner.ID)
	botCtx := WithUserID(ctx, f.bot.ID)
	created, err := f.router.onChannelsCreateChannel(ownerCtx, &tg.ChannelsCreateChannelRequest{Title: "Bot boosts", Megagroup: true})
	if err != nil {
		t.Fatal(err)
	}
	channel := created.(*tg.Updates).Chats[0].(*tg.Channel)
	if _, err := f.router.onChannelsInviteToChannel(ownerCtx, &tg.ChannelsInviteToChannelRequest{Channel: inputChannel(channel), Users: []tg.InputUserClass{inputUser(f.bot), inputUser(f.peer)}}); err != nil {
		t.Fatal(err)
	}
	now := int(time.Now().Unix())
	if _, err := f.channels.ApplyPremiumBoost(ctx, f.peer.ID, channel.ID, []int{domain.DefaultPremiumBoostSlotID}, now, now+3600); err != nil {
		t.Fatal(err)
	}
	req := &tg.PremiumGetUserBoostsRequest{Peer: inputPeerChannel(channel), UserID: inputUser(f.peer)}
	if _, err := f.router.onPremiumGetUserBoosts(botCtx, req); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("nonadmin bot boost query = %v", err)
	}
	if _, _, err := f.router.BotAPIGetUserChatBoosts(ctx, f.bot.ID, -botAPIChannelChatIDBase-channel.ID, f.peer.ID); !tgerr.Is(err, "CHAT_ADMIN_REQUIRED") {
		t.Fatalf("Bot API nonadmin boost=%v", err)
	}
	if _, err := f.router.onChannelsEditAdmin(ownerCtx, &tg.ChannelsEditAdminRequest{Channel: inputChannel(channel), UserID: inputUser(f.bot), AdminRights: tg.ChatAdminRights{ChangeInfo: true}}); err != nil {
		t.Fatal(err)
	}
	list, err := f.router.onPremiumGetUserBoosts(botCtx, req)
	if err != nil {
		t.Fatal(err)
	}
	if list.Count != 1 || len(list.Boosts) != 1 {
		t.Fatalf("user boosts = %+v", list)
	}
	id, ok := list.Boosts[0].GetUserID()
	if !ok || id != f.peer.ID {
		t.Fatalf("boost user = %d present=%v", id, ok)
	}
	boosts, _, err := f.router.BotAPIGetUserChatBoosts(ctx, f.bot.ID, -botAPIChannelChatIDBase-channel.ID, f.peer.ID)
	if err != nil || len(boosts) != 1 || boosts[0].UserID != f.peer.ID || boosts[0].Slot != domain.DefaultPremiumBoostSlotID || boosts[0].Expires != now+3600 {
		t.Fatalf("Bot API user boosts=%+v %v", boosts, err)
	}
}

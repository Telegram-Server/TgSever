package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appbots "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// botAPIBridgeFixture 是 Bot API 各组网关测试共用的环境：一个机器人、群主 owner、
// 普通成员 member，以及一个三人都在的公开超级群 @bridgegroup（机器人是普通成员；
// 需要管理员身份的测试自行用 channels.EditAdmin 提升）。
type botAPIBridgeFixture struct {
	ctx      context.Context
	owner    domain.User
	member   domain.User
	bot      domain.User
	channel  domain.Channel
	chatID   int64 // channel 的 Bot API chat_id（-100 前缀）
	router   *Router
	users    *appusers.Service
	channels *appchannels.Service
	messages *appmessages.Service
	bots     *appbots.Service
	sessions *captureSessions
}

func newBotAPIBridgeFixture(t *testing.T) botAPIBridgeFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 1101, Phone: "15550009101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	member, err := userStore.Create(ctx, domain.User{AccessHash: 1102, Phone: "15550009102", FirstName: "Ann", Username: "ann_member"})
	if err != nil {
		t.Fatalf("create member: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	botStore := memory.NewBotStore(userStore)
	bot, _, err := botStore.CreateBotAccount(ctx, domain.User{AccessHash: 2101, FirstName: "BridgeBot", Username: "BridgeBot"}, domain.BotProfile{
		OwnerUserID: owner.ID,
		TokenSecret: "secret",
		ChatHistory: true,
	})
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	botsService := appbots.NewService(userStore, botStore, messageStore)
	channelsService := appchannels.NewService(memory.NewChannelStore(), appchannels.WithBotProfileResolver(botsService))
	created, err := channelsService.CreateMegagroupFromCreateChat(ctx, owner.ID, domain.CreateChannelRequest{
		Title:         "Bridge Group",
		MemberUserIDs: []int64{member.ID, bot.ID},
		Date:          10,
	})
	if err != nil {
		t.Fatalf("create megagroup: %v", err)
	}
	channel, err := channelsService.UpdateUsername(ctx, owner.ID, domain.UpdateChannelUsernameRequest{
		UserID: owner.ID, ChannelID: created.Channel.ID, Username: "bridgegroup",
	})
	if err != nil {
		t.Fatalf("set group username: %v", err)
	}
	usersService := appusers.NewService(userStore)
	messagesService := appmessages.NewService(messageStore, dialogStore)
	sessions := &captureSessions{channelMembers: map[int64][]int64{channel.ID: {owner.ID, member.ID, bot.ID}}}
	router := New(Config{}, Deps{
		Users:         usersService,
		Messages:      messagesService,
		Channels:      channelsService,
		Bots:          botsService,
		BotAPIUpdates: memory.NewBotAPIUpdateStore(),
		Sessions:      sessions,
	}, zaptest.NewLogger(t), clock.System)
	return botAPIBridgeFixture{
		ctx:      ctx,
		owner:    owner,
		member:   member,
		bot:      bot,
		channel:  channel,
		chatID:   -botAPIChannelChatIDBase - channel.ID,
		router:   router,
		users:    usersService,
		channels: channelsService,
		messages: messagesService,
		bots:     botsService,
		sessions: sessions,
	}
}

// promoteBot 让机器人成为群管理员，给需要管理权限的测试用。
func (f botAPIBridgeFixture) promoteBot(t *testing.T, rights domain.ChannelAdminRights) {
	t.Helper()
	if _, err := f.channels.EditAdmin(f.ctx, f.owner.ID, domain.EditChannelAdminRequest{
		ChannelID:   f.channel.ID,
		UserID:      f.bot.ID,
		AdminRights: rights,
		Date:        20,
	}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
}

func TestBotAPIResolveChatUsernameFindsPublicGroup(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	got, err := f.router.BotAPIResolveChatUsername(f.ctx, f.bot.ID, "BridgeGroup")
	if err != nil || got != f.chatID {
		t.Fatalf("resolve @BridgeGroup = %d, %v; want %d", got, err, f.chatID)
	}
}

func TestBotAPIResolveChatUsernameRejectsUsersAndUnknownNames(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	// The official gateway only resolves @channelusername: users are addressed by id.
	for _, name := range []string{"ann_member", "nobody_here"} {
		if _, err := f.router.BotAPIResolveChatUsername(f.ctx, f.bot.ID, name); err == nil {
			t.Fatalf("resolve @%s succeeded, want an error", name)
		}
	}
}

func TestBotAPIInputChannelCarriesAccessHash(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	input, view, err := f.router.botAPIInputChannel(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("botAPIInputChannel: %v", err)
	}
	if input.ChannelID != f.channel.ID || input.AccessHash != f.channel.AccessHash || view.Channel.ID != f.channel.ID {
		t.Fatalf("input = %+v view = %+v, want channel %d with its access hash", input, view.Channel, f.channel.ID)
	}
	if _, _, err := f.router.botAPIInputChannel(f.ctx, f.bot.ID, f.member.ID); err == nil {
		t.Fatal("botAPIInputChannel accepted a user chat id")
	}
}

func TestBotAPIInputPeerResolvesUsersAndChannels(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	userPeer, peer, err := f.router.botAPIInputPeer(f.ctx, f.bot.ID, f.member.ID)
	if err != nil {
		t.Fatalf("botAPIInputPeer(user): %v", err)
	}
	if u, ok := userPeer.(*tg.InputPeerUser); !ok || u.UserID != f.member.ID || peer != (domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}) {
		t.Fatalf("user input = %#v peer = %+v", userPeer, peer)
	}
	channelPeer, peer, err := f.router.botAPIInputPeer(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("botAPIInputPeer(channel): %v", err)
	}
	if c, ok := channelPeer.(*tg.InputPeerChannel); !ok || c.ChannelID != f.channel.ID || c.AccessHash != f.channel.AccessHash ||
		peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}) {
		t.Fatalf("channel input = %#v peer = %+v", channelPeer, peer)
	}
	if _, _, err := f.router.botAPIInputPeer(f.ctx, f.bot.ID, -5); err == nil {
		t.Fatal("botAPIInputPeer accepted a basic-group chat id this server does not have")
	}
}

// 桥接的核心假设：以机器人身份直接调用 MTProto 处理器，再从返回的 Updates 里取回
// 持久化后的消息，得到与 BotAPISendMessage 相同形状的 domain.Message。
func TestBotAPIMessagesFromUpdatesReloadsChannelMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	input, peer, err := f.router.botAPIInputPeer(f.ctx, f.bot.ID, f.chatID)
	if err != nil {
		t.Fatalf("botAPIInputPeer: %v", err)
	}
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSendMessageRequest{
		Peer: input, Message: "hello via bridge", RandomID: 9101,
	})
	if err != nil {
		t.Fatalf("onMessagesSendMessage as bot: %v", err)
	}

	msgs, err := f.router.botAPIMessagesFromUpdates(f.ctx, f.bot.ID, peer, updates)
	if err != nil {
		t.Fatalf("botAPIMessagesFromUpdates: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "hello via bridge" || msgs[0].ID == 0 ||
		msgs[0].Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: f.channel.ID}) ||
		msgs[0].From != (domain.Peer{Type: domain.PeerTypeUser, ID: f.bot.ID}) || !msgs[0].Out {
		t.Fatalf("messages = %+v, want the bot's outbound channel message", msgs)
	}
}

func TestBotAPIMessagesFromUpdatesReloadsPrivateMessages(t *testing.T) {
	f := newBotAPIBridgeFixture(t)
	input, peer, err := f.router.botAPIInputPeer(f.ctx, f.bot.ID, f.member.ID)
	if err != nil {
		t.Fatalf("botAPIInputPeer: %v", err)
	}
	updates, err := f.router.onMessagesSendMessage(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSendMessageRequest{
		Peer: input, Message: "private via bridge", RandomID: 9102,
	})
	if err != nil {
		t.Fatalf("onMessagesSendMessage as bot: %v", err)
	}

	msgs, err := f.router.botAPIMessagesFromUpdates(f.ctx, f.bot.ID, peer, updates)
	if err != nil {
		t.Fatalf("botAPIMessagesFromUpdates: %v", err)
	}
	if len(msgs) != 1 || msgs[0].Body != "private via bridge" ||
		msgs[0].Peer != (domain.Peer{Type: domain.PeerTypeUser, ID: f.member.ID}) || !msgs[0].Out {
		t.Fatalf("messages = %+v, want the bot's copy of the private message", msgs)
	}
}

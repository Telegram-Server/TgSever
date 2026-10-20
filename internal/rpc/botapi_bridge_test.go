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
	apppolls "telesrv/internal/app/polls"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// botAPIBridgeFixture 是 Bot API 各组网关测试共用的唯一环境：一个机器人、群主 owner、
// 普通成员 member，以及一个三人都在的公开超级群 @bridgegroup（机器人是普通成员；
// 需要管理员身份的测试用 promote/promoteBot 提升）。文件服务与投票服务默认就绪，
// 媒体组、投票、媒体编辑和群头像测试直接可用。
type botAPIBridgeFixture struct {
	ctx       context.Context
	owner     domain.User
	member    domain.User
	bot       domain.User
	channel   domain.Channel
	chatID    int64 // channel 的 Bot API chat_id（-100 前缀）
	router    *Router
	users     *appusers.Service
	userStore *memory.UserStore
	channels  *appchannels.Service
	messages  *appmessages.Service
	bots      *appbots.Service
	sessions  *captureSessions
	files     *fakeFiles
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
	pollStore := memory.NewPollStore()
	messageStore.AttachPollStore(pollStore)
	channelStore := memory.NewChannelStore()
	channelStore.AttachPollStore(pollStore)
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
	channelsService := appchannels.NewService(channelStore, appchannels.WithBotProfileResolver(botsService))
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
	files := &fakeFiles{photos: map[int64]domain.Photo{}, docs: map[int64]domain.Document{}}
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:         usersService,
		Messages:      messagesService,
		Channels:      channelsService,
		Bots:          botsService,
		Files:         files,
		Polls:         apppolls.NewService(pollStore),
		BotAPIUpdates: memory.NewBotAPIUpdateStore(),
		Sessions:      sessions,
	}, zaptest.NewLogger(t), clock.System)
	return botAPIBridgeFixture{
		ctx:       ctx,
		owner:     owner,
		member:    member,
		bot:       bot,
		channel:   channel,
		chatID:    -botAPIChannelChatIDBase - channel.ID,
		router:    router,
		users:     usersService,
		userStore: userStore,
		channels:  channelsService,
		messages:  messagesService,
		bots:      botsService,
		sessions:  sessions,
		files:     files,
	}
}

// userByID 返回夹具里的用户（owner、member 或 bot），用来构造带 access_hash 的 InputUser。
func (f botAPIBridgeFixture) userByID(t *testing.T, id int64) domain.User {
	t.Helper()
	for _, u := range []domain.User{f.owner, f.member, f.bot} {
		if u.ID == id {
			return u
		}
	}
	t.Fatalf("user %d is not part of the fixture", id)
	return domain.User{}
}

// promote 由 by 经 channels.editAdmin 把 target 提为测试群管理员（rank 为空则不设头衔）。
// 走 MTProto 处理器，与真实客户端一样会推送更新并给机器人入队 chat_member/my_chat_member。
func (f botAPIBridgeFixture) promote(t *testing.T, by, target int64, rights domain.ChannelAdminRights, rank string) {
	t.Helper()
	user := f.userByID(t, target)
	req := &tg.ChannelsEditAdminRequest{
		Channel:     f.inputChannel(),
		UserID:      &tg.InputUser{UserID: user.ID, AccessHash: user.AccessHash},
		AdminRights: tgChatAdminRights(rights),
	}
	if rank != "" {
		req.SetRank(rank)
	}
	if _, err := f.router.onChannelsEditAdmin(WithUserID(f.ctx, by), req); err != nil {
		t.Fatalf("promote %d by %d: %v", target, by, err)
	}
}

// promoteBot 是 promote 的常用快捷方式：群主把机器人提为管理员。
func (f botAPIBridgeFixture) promoteBot(t *testing.T, rights domain.ChannelAdminRights) {
	t.Helper()
	f.promote(t, f.owner.ID, f.bot.ID, rights, "")
}

// 官方网关只解析公开群/频道的 @username；用户只能按 id 寻址，不存在的名字同样报错。
func TestBotAPIResolveChatUsername(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	for _, tc := range []struct {
		name    string
		want    int64
		wantErr bool
	}{
		{name: "BridgeGroup", want: f.chatID},
		{name: "ann_member", wantErr: true},
		{name: "nobody_here", wantErr: true},
	} {
		got, err := f.router.BotAPIResolveChatUsername(f.ctx, f.bot.ID, tc.name)
		if tc.wantErr {
			if err == nil {
				t.Fatalf("resolve @%s = %d, want an error", tc.name, got)
			}
			continue
		}
		if err != nil || got != tc.want {
			t.Fatalf("resolve @%s = %d, %v; want %d", tc.name, got, err, tc.want)
		}
	}
}

func TestBotAPIInputPeerRejectsInvalidChatIDs(t *testing.T) {
	f := newBotAPIBridgeFixture(t)

	if _, _, err := f.router.botAPIInputChannel(f.ctx, f.bot.ID, f.member.ID); err == nil {
		t.Fatal("botAPIInputChannel accepted a user chat id")
	}
	if _, _, err := f.router.botAPIInputPeer(f.ctx, f.bot.ID, -5); err == nil {
		t.Fatal("botAPIInputPeer accepted a basic-group chat id this server does not have")
	}
}

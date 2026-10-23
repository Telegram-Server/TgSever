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

// botGroupFixture 是一个机器人、群主 owner、普通成员 member，以及一个三人都在的公开
// 超级群 @bridgegroup（机器人是普通成员；需要管理员身份的测试用 promote/promoteBot 提升）。
// 全部经 MTProto 处理器操作。
type botGroupFixture struct {
	ctx      context.Context
	owner    domain.User
	member   domain.User
	bot      domain.User
	channel  domain.Channel
	router   *Router
	channels *appchannels.Service
	messages *appmessages.Service
	bots     *appbots.Service
	sessions *captureSessions
}

func newBotGroupFixture(t *testing.T) botGroupFixture {
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
	messagesService := appmessages.NewService(messageStore, dialogStore)
	sessions := &captureSessions{channelMembers: map[int64][]int64{channel.ID: {owner.ID, member.ID, bot.ID}}}
	router := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: messagesService,
		Channels: channelsService,
		Bots:     botsService,
		Files:    &fakeFiles{photos: map[int64]domain.Photo{}, docs: map[int64]domain.Document{}},
		Polls:    apppolls.NewService(pollStore),
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return botGroupFixture{
		ctx:      ctx,
		owner:    owner,
		member:   member,
		bot:      bot,
		channel:  channel,
		router:   router,
		channels: channelsService,
		messages: messagesService,
		bots:     botsService,
		sessions: sessions,
	}
}

func (f botGroupFixture) inputChannel() *tg.InputChannel {
	return &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

// userByID 返回夹具里的用户（owner、member 或 bot），用来构造带 access_hash 的 InputUser。
func (f botGroupFixture) userByID(t *testing.T, id int64) domain.User {
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
func (f botGroupFixture) promote(t *testing.T, by, target int64, rights domain.ChannelAdminRights, rank string) {
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
func (f botGroupFixture) promoteBot(t *testing.T, rights domain.ChannelAdminRights) {
	t.Helper()
	f.promote(t, f.owner.ID, f.bot.ID, rights, "")
}

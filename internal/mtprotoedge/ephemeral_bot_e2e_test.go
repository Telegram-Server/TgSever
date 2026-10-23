package mtprotoedge

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/iamxvbaba/td/session"
	"github.com/iamxvbaba/td/telegram"
	"github.com/iamxvbaba/td/tg"

	ephemeralapp "telesrv/internal/app/ephemeral"
	"telesrv/internal/domain"
	"telesrv/internal/rpc"
	"telesrv/internal/store/memory"
)

const (
	ephemeralE2EChannelID  int64 = 3001
	ephemeralE2EAccessHash int64 = 7
)

// ephemeralE2EChannels 只提供临时消息需要的频道视图：一个超级群，bot 是管理员。
type ephemeralE2EChannels struct {
	rpc.ChannelsService
	botID int64
}

func (c *ephemeralE2EChannels) member(userID int64) domain.ChannelMember {
	role := domain.ChannelRoleMember
	if userID == c.botID {
		role = domain.ChannelRoleAdmin
	}
	return domain.ChannelMember{ChannelID: ephemeralE2EChannelID, UserID: userID, Role: role, Status: domain.ChannelMemberActive}
}

func (c *ephemeralE2EChannels) channel() domain.Channel {
	return domain.Channel{ID: ephemeralE2EChannelID, AccessHash: ephemeralE2EAccessHash, Title: "Group", Megagroup: true}
}

func (c *ephemeralE2EChannels) ResolveChannel(_ context.Context, userID, channelID int64) (domain.ChannelView, error) {
	if channelID != ephemeralE2EChannelID {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	return domain.ChannelView{Channel: c.channel(), Self: c.member(userID)}, nil
}

func (c *ephemeralE2EChannels) GetParticipant(_ context.Context, _, channelID, participantUserID int64) (domain.ChannelMember, error) {
	if channelID != ephemeralE2EChannelID {
		return domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	return c.member(participantUserID), nil
}

func (c *ephemeralE2EChannels) GetForumTopicsByID(context.Context, int64, int64, []int) (domain.ChannelForumTopicList, error) {
	return domain.ChannelForumTopicList{Channel: c.channel()}, nil
}

// ActiveChannelIDsForUser 由会话开始接收更新时的频道订阅同步调用；这里没有真实频道成员关系。
func (c *ephemeralE2EChannels) ActiveChannelIDsForUser(context.Context, int64, int64, int) ([]int64, error) {
	return nil, nil
}

type ephemeralE2ECommands struct{}

func (ephemeralE2ECommands) GetBotCommands(context.Context, int64) ([]domain.BotCommand, error) {
	return nil, nil
}

// TestBotEphemeralMessageLifecycleOverMTProto 证明接通：bot 会话经真实 MTProto 连接
// 发送、编辑、删除临时消息，在线接收者依次收到 new/edit/delete 三个临时更新。
// 授权与形状矩阵在 app/ephemeral 与 rpc 层测试。
func TestBotEphemeralMessageLifecycleOverMTProto(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	channels := &ephemeralE2EChannels{}
	env := newBotCallbackEnv(t, ctx, func(deps *rpc.Deps) {
		deps.Channels = channels
		deps.Ephemeral = ephemeralapp.NewService(memory.NewEphemeralMessageStore(), channels, deps.Users, ephemeralE2ECommands{})
	})
	owner, ownerStorage := registerOwner(t, ctx, env, "+15550005101", "Receiver")
	botUser, botToken, err := env.bots.CreateBot(context.Background(), owner.ID, "Ephemeral Bot", "ephemeral_e2e_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	channels.botID = botUser.ID

	received := make(chan tg.UpdateClass, 8)
	receiverReady := make(chan struct{})
	receiverStop := make(chan struct{})
	receiverErr := make(chan error, 1)
	go func() {
		client := env.newCli(ownerStorage, telegram.UpdateHandlerFunc(func(_ context.Context, u tg.UpdatesClass) error {
			for _, update := range updatesFromClass(u) {
				switch update.(type) {
				case *tg.UpdateNewEphemeralMessage, *tg.UpdateEditEphemeralMessage, *tg.UpdateDeleteEphemeralMessages:
					received <- update
				}
			}
			return nil
		}))
		receiverErr <- client.Run(ctx, func(ctx context.Context) error {
			if _, err := tg.NewClient(client).UpdatesGetState(ctx); err != nil {
				return err
			}
			close(receiverReady)
			select {
			case <-receiverStop:
			case <-ctx.Done():
			}
			return nil
		})
	}()
	select {
	case <-receiverReady:
	case err := <-receiverErr:
		t.Fatalf("receiver: %v", err)
	case <-ctx.Done():
		t.Fatal("receiver did not come online")
	}

	next := func(what string) tg.UpdateClass {
		select {
		case update := <-received:
			return update
		case <-ctx.Done():
			t.Fatalf("receiver did not get the %s update", what)
			return nil
		}
	}

	botClient := env.newCliH(&session.StorageMemory{})
	if err := botClient.Run(ctx, func(ctx context.Context) error {
		raw := tg.NewClient(botClient)
		if _, err := raw.AuthImportBotAuthorization(ctx, &tg.AuthImportBotAuthorizationRequest{APIID: 1, APIHash: "hash", BotAuthToken: botToken}); err != nil {
			return err
		}
		peer := &tg.InputPeerChannel{ChannelID: ephemeralE2EChannelID, AccessHash: ephemeralE2EAccessHash}
		receiver := &tg.InputUser{UserID: owner.ID}

		sentUpdates, err := raw.EphemeralSendMessage(ctx, &tg.EphemeralSendMessageRequest{
			Peer: peer, ReceiverID: receiver, Message: "just for you", RandomID: 515151,
		})
		if err != nil {
			return fmt.Errorf("send: %w", err)
		}
		var id int
		for _, update := range updatesFromClass(sentUpdates) {
			if created, ok := update.(*tg.UpdateNewEphemeralMessage); ok {
				id = created.Message.ID
			}
		}
		if id == 0 {
			return fmt.Errorf("send result = %#v", sentUpdates)
		}
		if got, ok := next("new").(*tg.UpdateNewEphemeralMessage); !ok || got.Message.ID != id || got.Message.Message != "just for you" {
			return fmt.Errorf("receiver new = %#v", got)
		}

		edit := &tg.EphemeralEditMessageRequest{Peer: peer, ReceiverID: receiver, ID: id}
		edit.SetMessage("edited for you")
		if _, err := raw.EphemeralEditMessage(ctx, edit); err != nil {
			return fmt.Errorf("edit: %w", err)
		}
		if got, ok := next("edit").(*tg.UpdateEditEphemeralMessage); !ok || got.Message.ID != id || got.Message.Message != "edited for you" {
			return fmt.Errorf("receiver edit = %#v", got)
		}

		if ok, err := raw.EphemeralDeleteMessage(ctx, &tg.EphemeralDeleteMessageRequest{Peer: peer, ReceiverID: receiver, ID: id}); err != nil || !ok {
			return fmt.Errorf("delete = %v, %v", ok, err)
		}
		if got, ok := next("delete").(*tg.UpdateDeleteEphemeralMessages); !ok || len(got.IDs) != 1 || got.IDs[0] != id {
			return fmt.Errorf("receiver delete = %#v", got)
		}
		return nil
	}); err != nil {
		t.Fatalf("bot: %v", err)
	}
	close(receiverStop)
	if err := <-receiverErr; err != nil {
		t.Fatalf("receiver: %v", err)
	}
	cancel()
}

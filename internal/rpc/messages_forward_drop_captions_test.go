package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// forwardCaptionFixture：owner 建一个频道，里面有带说明文字的图片与纯文本消息；
// recipient 是私聊转发目标。
type forwardCaptionFixture struct {
	ctx       context.Context
	router    *Router
	channels  *appchannels.Service
	store     *memory.ChannelStore
	owner     domain.User
	recipient domain.User
	channel   *tg.Channel
}

func newForwardCaptionFixture(t *testing.T) forwardCaptionFixture {
	t.Helper()
	return newForwardCaptionFixtureWithMessages(t, appmessages.NewService(memory.NewMessageStore(), nil))
}

func newForwardCaptionFixtureWithMessages(t *testing.T, messages MessagesService) forwardCaptionFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 51, Phone: "15550005101", FirstName: "Owner"})
	recipient, _ := userStore.Create(ctx, domain.User{AccessHash: 52, Phone: "15550005102", FirstName: "Recipient"})
	channelStore := memory.NewChannelStore()
	channels := appchannels.NewService(channelStore)
	r := New(Config{}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channels,
		Messages: messages,
	}, zaptest.NewLogger(t), clock.System)
	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{
		Title:     "Caption Source",
		About:     "forward source",
		Megagroup: true,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	return forwardCaptionFixture{
		ctx:       ctx,
		router:    r,
		channels:  channels,
		store:     channelStore,
		owner:     owner,
		recipient: recipient,
		channel:   created.(*tg.Updates).Chats[0].(*tg.Channel),
	}
}

// post 直接经 channels 服务写一条消息（夹具没有文件服务）。
func (f forwardCaptionFixture) post(t *testing.T, body string, media *domain.MessageMedia, groupedID, randomID int64) int {
	t.Helper()
	var entities []domain.MessageEntity
	if body != "" {
		entities = []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 1}}
	}
	res, err := f.channels.SendMessage(f.ctx, f.owner.ID, domain.SendChannelMessageRequest{
		UserID:    f.owner.ID,
		ChannelID: f.channel.ID,
		RandomID:  randomID,
		Message:   body,
		Entities:  entities,
		Media:     media,
		GroupedID: groupedID,
		Date:      40,
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	return res.Message.ID
}

func forwardCaptionPhoto(id int64) *domain.MessageMedia {
	return &domain.MessageMedia{Kind: domain.MessageMediaKindPhoto, Photo: &domain.Photo{ID: id, AccessHash: id + 1}}
}

func (f forwardCaptionFixture) channelPeer() *tg.InputPeerChannel {
	return &tg.InputPeerChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash}
}

func (f forwardCaptionFixture) recipientPeer() *tg.InputPeerUser {
	return &tg.InputPeerUser{UserID: f.recipient.ID, AccessHash: f.recipient.AccessHash}
}

// forward 以 owner 身份调用处理器，按 updates 顺序返回新消息。
func (f forwardCaptionFixture) forward(t *testing.T, req *tg.MessagesForwardMessagesRequest) []*tg.Message {
	t.Helper()
	updates, err := f.router.onMessagesForwardMessages(WithUserID(f.ctx, f.owner.ID), req)
	if err != nil {
		t.Fatalf("forward: %v", err)
	}
	var out []*tg.Message
	for _, update := range updates.(*tg.Updates).Updates {
		var msg tg.MessageClass
		switch u := update.(type) {
		case *tg.UpdateNewMessage:
			msg = u.Message
		case *tg.UpdateNewChannelMessage:
			msg = u.Message
		default:
			continue
		}
		m, ok := msg.(*tg.Message)
		if !ok {
			t.Fatalf("forwarded message = %T", msg)
		}
		out = append(out, m)
	}
	if len(out) != len(req.ID) {
		t.Fatalf("forwarded %d messages, want %d", len(out), len(req.ID))
	}
	return out
}

func TestForwardDropMediaCaptionsStripsCaptionsInPrivateAndChannel(t *testing.T) {
	for _, target := range []string{"private", "channel"} {
		t.Run(target, func(t *testing.T) {
			f := newForwardCaptionFixture(t)
			photo := f.post(t, "pic caption", forwardCaptionPhoto(8801), 0, 6101)
			text := f.post(t, "plain text", nil, 0, 6102)
			var toPeer tg.InputPeerClass = f.recipientPeer()
			if target == "channel" {
				toPeer = f.channelPeer()
			}
			msgs := f.forward(t, &tg.MessagesForwardMessagesRequest{
				FromPeer:          f.channelPeer(),
				ToPeer:            toPeer,
				ID:                []int{photo, text},
				RandomID:          []int64{6201, 6202},
				DropAuthor:        true,
				DropMediaCaptions: true,
			})
			if msgs[0].Message != "" || len(msgs[0].Entities) != 0 || msgs[0].Media == nil {
				t.Fatalf("photo = %q %+v media=%T, want media without caption", msgs[0].Message, msgs[0].Entities, msgs[0].Media)
			}
			if msgs[0].FwdFrom.Date != 0 {
				t.Fatalf("photo forward header = %+v, want none", msgs[0].FwdFrom)
			}
			if msgs[1].Message != "plain text" || len(msgs[1].Entities) != 1 {
				t.Fatalf("text = %q %+v, want text kept", msgs[1].Message, msgs[1].Entities)
			}
		})
	}
}

// 官方转发相册会得到新的共享 grouped_id；不同源相册映射到不同的新值，非相册保持 0。
func TestForwardAlbumKeepsGroupingWithFreshGroupedID(t *testing.T) {
	for _, target := range []string{"private", "channel"} {
		t.Run(target, func(t *testing.T) {
			f := newForwardCaptionFixture(t)
			a1 := f.post(t, "album a", forwardCaptionPhoto(8841), 777, 6141)
			a2 := f.post(t, "", forwardCaptionPhoto(8842), 777, 6142)
			b1 := f.post(t, "", forwardCaptionPhoto(8843), 888, 6143)
			b2 := f.post(t, "", forwardCaptionPhoto(8844), 888, 6144)
			text := f.post(t, "plain", nil, 0, 6145)
			var toPeer tg.InputPeerClass = f.recipientPeer()
			if target == "channel" {
				toPeer = f.channelPeer()
			}
			msgs := f.forward(t, &tg.MessagesForwardMessagesRequest{
				FromPeer:          f.channelPeer(),
				ToPeer:            toPeer,
				ID:                []int{a1, a2, b1, b2, text},
				RandomID:          []int64{6241, 6242, 6243, 6244, 6245},
				DropAuthor:        true,
				DropMediaCaptions: true,
			})
			a, aOK := msgs[0].GetGroupedID()
			b, bOK := msgs[2].GetGroupedID()
			if !aOK || !bOK || a == 0 || b == 0 || a == b || a == 777 || b == 888 {
				t.Fatalf("grouped ids a=%d/%v b=%d/%v, want two distinct fresh ids", a, aOK, b, bOK)
			}
			if got, _ := msgs[1].GetGroupedID(); got != a {
				t.Fatalf("second album item grouped id = %d, want %d", got, a)
			}
			if got, _ := msgs[3].GetGroupedID(); got != b {
				t.Fatalf("fourth album item grouped id = %d, want %d", got, b)
			}
			if got, ok := msgs[4].GetGroupedID(); ok || got != 0 {
				t.Fatalf("plain text grouped id = %d/%v, want none", got, ok)
			}
		})
	}
}

// 官方语义：drop_media_captions 不带 drop_author 时无效，说明文字与转发头都保留。
func TestForwardDropMediaCaptionsIgnoredWithoutDropAuthor(t *testing.T) {
	f := newForwardCaptionFixture(t)
	photo := f.post(t, "pic caption", forwardCaptionPhoto(8811), 0, 6111)
	msgs := f.forward(t, &tg.MessagesForwardMessagesRequest{
		FromPeer:          f.channelPeer(),
		ToPeer:            f.recipientPeer(),
		ID:                []int{photo},
		RandomID:          []int64{6211},
		DropMediaCaptions: true,
	})
	if msgs[0].Message != "pic caption" || len(msgs[0].Entities) != 1 || msgs[0].FwdFrom.Date == 0 {
		t.Fatalf("photo = %q %+v fwd=%+v, want caption and header kept", msgs[0].Message, msgs[0].Entities, msgs[0].FwdFrom)
	}
}

// owner 以订阅者身份把图片转发进 recipient 频道的私信（monoforum）。
func TestForwardDropMediaCaptionsAppliesToMonoforum(t *testing.T) {
	f := newForwardCaptionFixture(t)
	photo := f.post(t, "pic caption", forwardCaptionPhoto(8831), 0, 6131)
	created, err := f.channels.CreateChannel(f.ctx, f.recipient.ID, domain.CreateChannelRequest{Title: "DM Target", Broadcast: true, Date: 41})
	if err != nil {
		t.Fatalf("create broadcast: %v", err)
	}
	enabled, err := f.store.SetPaidMessagesPrice(f.ctx, f.recipient.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	mono, err := f.store.GetChannelByID(f.ctx, enabled.Channel.LinkedMonoforumID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	msgs := f.forward(t, &tg.MessagesForwardMessagesRequest{
		FromPeer:          f.channelPeer(),
		ToPeer:            &tg.InputPeerChannel{ChannelID: mono.ID, AccessHash: mono.AccessHash},
		ID:                []int{photo},
		RandomID:          []int64{6231},
		DropAuthor:        true,
		DropMediaCaptions: true,
	})
	if msgs[0].Message != "" || len(msgs[0].Entities) != 0 || msgs[0].Media == nil {
		t.Fatalf("monoforum photo = %q %+v, want media without caption", msgs[0].Message, msgs[0].Entities)
	}
}

// 定时转发同样去掉说明文字与转发头：从定时队列（getScheduledHistory）里读回排程结果。
func TestForwardDropMediaCaptionsAppliesToScheduledForward(t *testing.T) {
	f := newScheduledFixture(t)
	group := f.createGroup("Caption Source")
	posted, err := f.channels.SendChannelMessage(context.Background(), domain.SendChannelMessageRequest{
		UserID: f.owner.ID, ChannelID: group.ID, RandomID: 6121, Message: "pic caption",
		Entities: []domain.MessageEntity{{Type: domain.MessageEntityBold, Offset: 0, Length: 1}},
		Media:    forwardCaptionPhoto(8821), Date: 40,
	})
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	f.forward(&tg.MessagesForwardMessagesRequest{
		FromPeer:          inputPeerChannel(group),
		ToPeer:            f.friendPeer(),
		ID:                []int{posted.Message.ID},
		RandomID:          []int64{6221},
		DropAuthor:        true,
		DropMediaCaptions: true,
	}, f.scheduleDate())
	queued := f.scheduledHistory(f.owner, f.friendPeer())
	if len(queued) != 1 {
		t.Fatalf("scheduled queue = %+v, want the forwarded photo", queued)
	}
	if msg := queued[0]; msg.Message != "" || len(msg.Entities) != 0 || msg.Media == nil {
		t.Fatalf("scheduled forward = %q %+v media=%T, want media without caption", msg.Message, msg.Entities, msg.Media)
	}
	if fwd, ok := queued[0].GetFwdFrom(); ok {
		t.Fatalf("scheduled forward header = %+v, want none", fwd)
	}
}

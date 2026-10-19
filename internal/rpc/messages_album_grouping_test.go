package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// albumScheduledMessages 在真实内存消息服务上叠一个内存定时队列：普通发送、相册预留走
// appmessages，定时消息走 scheduledQueue（与 postgres store 同语义）。appmessages.Service
// 多包一层，让它自带的定时方法沉到更深的嵌入层，由 scheduledQueue 的同名方法遮蔽。
type albumScheduledMessages struct {
	appMessagesBase
	*scheduledQueue
}

type appMessagesBase struct{ *appmessages.Service }

func newAlbumScheduledRouter(t *testing.T) (*Router, *albumScheduledMessages, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550019001", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550019002", FirstName: "Friend"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	messages := &albumScheduledMessages{
		appMessagesBase: appMessagesBase{appmessages.NewService(messageStore, dialogStore)},
		scheduledQueue:  &scheduledQueue{},
	}
	files := &fakeFiles{docs: map[int64]domain.Document{
		555: {ID: 555, AccessHash: 5, DCID: 2, MimeType: "image/jpeg"},
		556: {ID: 556, AccessHash: 6, DCID: 2, MimeType: "image/jpeg"},
		557: {ID: 557, AccessHash: 7, DCID: 2, MimeType: "image/jpeg"},
	}, photos: map[int64]domain.Photo{}}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Messages: messages,
		Files:    files,
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	return r, messages, owner, friend
}

func albumInputItems(randomBase int64) []tg.InputSingleMedia {
	items := make([]tg.InputSingleMedia, 0, 3)
	for i := int64(0); i < 3; i++ {
		items = append(items, tg.InputSingleMedia{
			Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555 + i, AccessHash: 5 + i}},
			RandomID: randomBase + i,
			Message:  "album",
		})
	}
	return items
}

// sharedGroupedID 断言 messages 全部带同一个非零 grouped_id 并返回它。
func sharedGroupedID(t *testing.T, label string, messages []tg.MessageClass, want int) int64 {
	t.Helper()
	if len(messages) != want {
		t.Fatalf("%s: got %d messages, want %d", label, len(messages), want)
	}
	var group int64
	for _, item := range messages {
		msg, ok := item.(*tg.Message)
		if !ok {
			t.Fatalf("%s: message %T, want *tg.Message", label, item)
		}
		got, ok := msg.GetGroupedID()
		if !ok || got == 0 {
			t.Fatalf("%s: message %d grouped_id = %d present=%v, want non-zero", label, msg.ID, got, ok)
		}
		if group != 0 && got != group {
			t.Fatalf("%s: message %d grouped_id = %d, want shared %d", label, msg.ID, got, group)
		}
		group = got
	}
	return group
}

func scheduledMessagesFromUpdates(updates tg.UpdatesClass) []tg.MessageClass {
	up, ok := updates.(*tg.Updates)
	if !ok {
		return nil
	}
	out := make([]tg.MessageClass, 0)
	for _, update := range up.Updates {
		if v, ok := update.(*tg.UpdateNewScheduledMessage); ok {
			out = append(out, v.Message)
		}
	}
	return out
}

func TestScheduledSendMultiMediaKeepsAlbumGrouping(t *testing.T) {
	r, _, owner, friend := newAlbumScheduledRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	req := &tg.MessagesSendMultiMediaRequest{Peer: peer, MultiMedia: albumInputItems(61001)}
	req.SetScheduleDate(int(time.Now().Unix()) + 3600)
	updates, err := r.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("scheduled sendMultiMedia: %v", err)
	}
	echoGroup := sharedGroupedID(t, "scheduled echo", scheduledMessagesFromUpdates(updates), 3)

	history, err := r.onMessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{Peer: peer})
	if err != nil {
		t.Fatalf("get scheduled history: %v", err)
	}
	listGroup := sharedGroupedID(t, "scheduled history", history.(*tg.MessagesMessages).Messages, 3)
	if listGroup != echoGroup {
		t.Fatalf("scheduled history grouped_id = %d, want echo %d", listGroup, echoGroup)
	}
}

// TestScheduledForwardAlbumKeepsGrouping 验证定时转发相册：定时列表同组，到点发出仍同组。
func TestScheduledForwardAlbumKeepsGrouping(t *testing.T) {
	r, messages, owner, friend := newAlbumScheduledRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	sent, err := r.onMessagesSendMultiMedia(ctx, &tg.MessagesSendMultiMediaRequest{Peer: peer, MultiMedia: albumInputItems(63001)})
	if err != nil {
		t.Fatalf("send source album: %v", err)
	}
	sourceIDs := make([]int, 0, 3)
	for _, update := range sent.(*tg.Updates).Updates {
		if v, ok := update.(*tg.UpdateNewMessage); ok {
			sourceIDs = append(sourceIDs, v.Message.(*tg.Message).ID)
		}
	}
	if len(sourceIDs) != 3 {
		t.Fatalf("source album ids = %v, want 3", sourceIDs)
	}

	fwd := &tg.MessagesForwardMessagesRequest{
		FromPeer: peer,
		ToPeer:   peer,
		ID:       sourceIDs,
		RandomID: []int64{63101, 63102, 63103},
	}
	fwd.SetScheduleDate(int(time.Now().Unix()) + 3600)
	updates, err := r.onMessagesForwardMessages(ctx, fwd)
	if err != nil {
		t.Fatalf("scheduled forward album: %v", err)
	}
	scheduledGroup := sharedGroupedID(t, "scheduled forward echo", scheduledMessagesFromUpdates(updates), 3)
	history, err := r.onMessagesGetScheduledHistory(ctx, &tg.MessagesGetScheduledHistoryRequest{Peer: peer})
	if err != nil {
		t.Fatalf("get scheduled history: %v", err)
	}
	if got := sharedGroupedID(t, "scheduled forward history", history.(*tg.MessagesMessages).Messages, 3); got != scheduledGroup {
		t.Fatalf("scheduled forward history grouped_id = %d, want %d", got, scheduledGroup)
	}

	if !NewScheduledDispatcher(r, zaptest.NewLogger(t)).DispatchOnce(context.Background()) {
		t.Fatalf("dispatcher sent nothing")
	}
	sentHistory, err := messages.GetHistory(context.Background(), owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	forwarded := 0
	for _, msg := range sentHistory.Messages {
		if msg.RandomID < 63101 {
			continue
		}
		forwarded++
		if msg.GroupedID != scheduledGroup {
			t.Fatalf("dispatched forward %d grouped_id = %d, want %d", msg.ID, msg.GroupedID, scheduledGroup)
		}
	}
	if forwarded != 3 {
		t.Fatalf("dispatched forwards = %d, want 3", forwarded)
	}
}

type albumMonoforumFixture struct {
	router    *Router
	channels  *memory.ChannelStore
	owner     domain.User
	sub       domain.User
	monoID    int64
	monoInput *tg.InputPeerChannel
}

func newAlbumMonoforumFixture(t *testing.T) albumMonoforumFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, err := userStore.Create(ctx, domain.User{AccessHash: 41, Phone: "15550019101", FirstName: "Owner"})
	if err != nil {
		t.Fatalf("create owner: %v", err)
	}
	sub, err := userStore.Create(ctx, domain.User{AccessHash: 42, Phone: "15550019102", FirstName: "Sub"})
	if err != nil {
		t.Fatalf("create sub: %v", err)
	}
	channelStore := memory.NewChannelStore()
	channelSvc := appchannels.NewService(channelStore)
	dialogStore := memory.NewDialogStore()
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: channelSvc,
		Messages: appmessages.NewService(memory.NewMessageStore(dialogStore), dialogStore),
		Files: &fakeFiles{docs: map[int64]domain.Document{
			555: {ID: 555, AccessHash: 5, DCID: 2, MimeType: "image/jpeg"},
			556: {ID: 556, AccessHash: 6, DCID: 2, MimeType: "image/jpeg"},
			557: {ID: 557, AccessHash: 7, DCID: 2, MimeType: "image/jpeg"},
		}, photos: map[int64]domain.Photo{}},
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	created, err := channelSvc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "DM Album", Broadcast: true, Date: 1000})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := channelStore.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	mono, err := channelStore.GetChannelByID(ctx, monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	return albumMonoforumFixture{
		router:    r,
		channels:  channelStore,
		owner:     owner,
		sub:       sub,
		monoID:    monoID,
		monoInput: &tg.InputPeerChannel{ChannelID: monoID, AccessHash: mono.AccessHash},
	}
}

func channelMessagesFromUpdates(updates tg.UpdatesClass) []tg.MessageClass {
	up, ok := updates.(*tg.Updates)
	if !ok {
		return nil
	}
	out := make([]tg.MessageClass, 0)
	for _, update := range up.Updates {
		if v, ok := update.(*tg.UpdateNewChannelMessage); ok {
			out = append(out, v.Message)
		}
	}
	return out
}

// monoforumHistoryGroups 返回 monoforum 中订阅者子会话里 randomID 落在 [from, to] 的消息分组。
func (f albumMonoforumFixture) monoforumHistoryGroups(t *testing.T, from, to int64) []int64 {
	t.Helper()
	hist, err := f.channels.ListChannelHistory(context.Background(), f.sub.ID, domain.ChannelHistoryFilter{ChannelID: f.monoID, Limit: 50})
	if err != nil {
		t.Fatalf("monoforum history: %v", err)
	}
	groups := make([]int64, 0)
	for _, msg := range hist.Messages {
		if msg.RandomID >= from && msg.RandomID <= to {
			groups = append(groups, msg.GroupedID)
		}
	}
	return groups
}

// TestForwardAlbumToMonoforumKeepsGrouping 验证转发相册到频道私信：新消息共享一个新的非零分组。
func TestForwardAlbumToMonoforumKeepsGrouping(t *testing.T) {
	f := newAlbumMonoforumFixture(t)
	ctx := context.Background()
	subPeer := domain.Peer{Type: domain.PeerTypeUser, ID: f.sub.ID}
	const sourceGroup = int64(424242)
	sourceIDs := make([]int, 0, 3)
	for i := int64(0); i < 3; i++ {
		res, err := f.channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{
			MonoforumID: f.monoID, SenderUserID: f.sub.ID, SavedPeer: subPeer,
			RandomID: 64001 + i, Message: "source album", GroupedID: sourceGroup, Date: 1001 + int(i),
		})
		if err != nil {
			t.Fatalf("seed source album %d: %v", i, err)
		}
		sourceIDs = append(sourceIDs, res.Message.ID)
	}

	updates, err := f.router.onMessagesForwardMessages(WithUserID(ctx, f.sub.ID), &tg.MessagesForwardMessagesRequest{
		FromPeer: f.monoInput,
		ToPeer:   f.monoInput,
		ID:       sourceIDs,
		RandomID: []int64{64101, 64102, 64103},
	})
	if err != nil {
		t.Fatalf("forward album to monoforum: %v", err)
	}
	echoGroup := sharedGroupedID(t, "monoforum forward echo", channelMessagesFromUpdates(updates), 3)
	if echoGroup == sourceGroup {
		t.Fatalf("forwarded album reused source grouped_id %d, want a fresh group", sourceGroup)
	}
	for _, group := range f.monoforumHistoryGroups(t, 64101, 64103) {
		if group != echoGroup {
			t.Fatalf("monoforum forwarded history grouped_id = %d, want %d", group, echoGroup)
		}
	}
}

// TestSendMultiMediaToMonoforumKeepsGrouping 验证订阅者向频道私信发相册：各条写入同一
// saved_peer 子会话并共享一个非零分组；重放同一请求返回同一分组。
func TestSendMultiMediaToMonoforumKeepsGrouping(t *testing.T) {
	f := newAlbumMonoforumFixture(t)
	ctx := WithUserID(context.Background(), f.sub.ID)
	req := &tg.MessagesSendMultiMediaRequest{Peer: f.monoInput, MultiMedia: albumInputItems(65001)}
	updates, err := f.router.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("sendMultiMedia to monoforum: %v", err)
	}
	echoGroup := sharedGroupedID(t, "monoforum album echo", channelMessagesFromUpdates(updates), 3)
	groups := f.monoforumHistoryGroups(t, 65001, 65003)
	if len(groups) != 3 {
		t.Fatalf("monoforum album history = %v, want 3 items", groups)
	}
	for _, group := range groups {
		if group != echoGroup {
			t.Fatalf("monoforum album history grouped_id = %d, want %d", group, echoGroup)
		}
	}
	replay, err := f.router.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("replay sendMultiMedia to monoforum: %v", err)
	}
	if got := sharedGroupedID(t, "monoforum album replay", channelMessagesFromUpdates(replay), 3); got != echoGroup {
		t.Fatalf("replayed album grouped_id = %d, want %d", got, echoGroup)
	}
	if len(f.monoforumHistoryGroups(t, 65001, 65003)) != 3 {
		t.Fatalf("replay duplicated album messages")
	}
}

// TestScheduledAlbumDispatchedInBatchesKeepsOneGroup 验证同组定时消息分批发出（先手动
// sendScheduledMessages 一条，其余由后台派发器到点发出）后，历史里仍是同一个非零分组。
func TestScheduledAlbumDispatchedInBatchesKeepsOneGroup(t *testing.T) {
	r, messages, owner, friend := newAlbumScheduledRouter(t)
	ctx := WithUserID(context.Background(), owner.ID)
	peer := &tg.InputPeerUser{UserID: friend.ID, AccessHash: friend.AccessHash}
	req := &tg.MessagesSendMultiMediaRequest{Peer: peer, MultiMedia: albumInputItems(62001)}
	req.SetScheduleDate(int(time.Now().Unix()) + 3600)
	updates, err := r.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("scheduled sendMultiMedia: %v", err)
	}
	scheduledGroup := sharedGroupedID(t, "scheduled echo", scheduledMessagesFromUpdates(updates), 3)
	first := scheduledMessagesFromUpdates(updates)[0].(*tg.Message).ID

	if _, err := r.onMessagesSendScheduledMessages(ctx, &tg.MessagesSendScheduledMessagesRequest{Peer: peer, ID: []int{first}}); err != nil {
		t.Fatalf("send first scheduled now: %v", err)
	}
	if !NewScheduledDispatcher(r, zaptest.NewLogger(t)).DispatchOnce(context.Background()) {
		t.Fatalf("dispatcher sent nothing, want remaining album items")
	}

	history, err := messages.GetHistory(context.Background(), owner.ID, domain.MessageFilter{
		HasPeer: true,
		Peer:    domain.Peer{Type: domain.PeerTypeUser, ID: friend.ID},
		Limit:   10,
	})
	if err != nil {
		t.Fatalf("history: %v", err)
	}
	if len(history.Messages) != 3 {
		t.Fatalf("history len = %d, want 3 dispatched album items", len(history.Messages))
	}
	for _, msg := range history.Messages {
		if msg.GroupedID != scheduledGroup {
			t.Fatalf("dispatched message %d grouped_id = %d, want scheduled group %d", msg.ID, msg.GroupedID, scheduledGroup)
		}
	}
}

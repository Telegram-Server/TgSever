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

// albumFakeFiles 提供 albumInputItems 引用的三个图片文档（555–557）。
func albumFakeFiles() *fakeFiles {
	return &fakeFiles{docs: map[int64]domain.Document{
		555: {ID: 555, AccessHash: 5, DCID: 2, MimeType: "image/jpeg"},
		556: {ID: 556, AccessHash: 6, DCID: 2, MimeType: "image/jpeg"},
		557: {ID: 557, AccessHash: 7, DCID: 2, MimeType: "image/jpeg"},
	}, photos: map[int64]domain.Photo{}}
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

func scheduledHistoryClasses(messages []*tg.Message) []tg.MessageClass {
	out := make([]tg.MessageClass, 0, len(messages))
	for _, msg := range messages {
		out = append(out, msg)
	}
	return out
}

// TestScheduledForwardAlbumKeepsGrouping 验证定时转发相册：定时列表同组，到点发出仍同组。
func TestScheduledForwardAlbumKeepsGrouping(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := f.ctx(f.owner)
	peer := f.friendPeer()
	sent, err := f.router.onMessagesSendMultiMedia(ctx, &tg.MessagesSendMultiMediaRequest{Peer: peer, MultiMedia: albumInputItems(63001)})
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
	fwd.SetScheduleDate(f.scheduleDate())
	updates, err := f.router.onMessagesForwardMessages(ctx, fwd)
	if err != nil {
		t.Fatalf("scheduled forward album: %v", err)
	}
	scheduledGroup := sharedGroupedID(t, "scheduled forward echo", scheduledMessagesFromUpdates(updates), 3)
	if got := sharedGroupedID(t, "scheduled forward history", scheduledHistoryClasses(f.scheduledHistory(f.owner, peer)), 3); got != scheduledGroup {
		t.Fatalf("scheduled forward history grouped_id = %d, want %d", got, scheduledGroup)
	}

	if !f.dispatch() {
		t.Fatalf("dispatcher sent nothing")
	}
	for _, randomID := range []int64{63101, 63102, 63103} {
		msg, ok := f.privateByRandomID(randomID)
		if !ok {
			t.Fatalf("dispatched forward random_id=%d not in history", randomID)
		}
		if msg.GroupedID != scheduledGroup {
			t.Fatalf("dispatched forward %d grouped_id = %d, want %d", msg.ID, msg.GroupedID, scheduledGroup)
		}
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
		Files:    albumFakeFiles(),
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
// 历史里的分组由 storetest.MonoforumAlbumGrouping 契约覆盖，这里只看 RPC 回声。
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
	if echoGroup := sharedGroupedID(t, "monoforum forward echo", channelMessagesFromUpdates(updates), 3); echoGroup == sourceGroup {
		t.Fatalf("forwarded album reused source grouped_id %d, want a fresh group", sourceGroup)
	}
}

// TestSendMultiMediaToMonoforumKeepsGrouping 验证订阅者向频道私信发相册：回声共享一个非零
// 分组；重放同一请求返回同一分组且不重复写入。
func TestSendMultiMediaToMonoforumKeepsGrouping(t *testing.T) {
	f := newAlbumMonoforumFixture(t)
	ctx := WithUserID(context.Background(), f.sub.ID)
	req := &tg.MessagesSendMultiMediaRequest{Peer: f.monoInput, MultiMedia: albumInputItems(65001)}
	updates, err := f.router.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("sendMultiMedia to monoforum: %v", err)
	}
	echoGroup := sharedGroupedID(t, "monoforum album echo", channelMessagesFromUpdates(updates), 3)
	replay, err := f.router.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("replay sendMultiMedia to monoforum: %v", err)
	}
	if got := sharedGroupedID(t, "monoforum album replay", channelMessagesFromUpdates(replay), 3); got != echoGroup {
		t.Fatalf("replayed album grouped_id = %d, want %d", got, echoGroup)
	}
	if got := f.monoforumHistoryGroups(t, 65001, 65003); len(got) != 3 {
		t.Fatalf("monoforum album history after replay = %v, want 3 items (no duplicates)", got)
	}
}

// TestScheduledAlbumDispatchedInBatchesKeepsOneGroup 验证定时相册：排程回声与定时列表同组；
// 分批发出（先手动 sendScheduledMessages 一条，其余由后台派发器到点发出）后，历史里仍是
// 排程时的同一个非零分组。
func TestScheduledAlbumDispatchedInBatchesKeepsOneGroup(t *testing.T) {
	f := newScheduledFixture(t)
	ctx := f.ctx(f.owner)
	peer := f.friendPeer()
	req := &tg.MessagesSendMultiMediaRequest{Peer: peer, MultiMedia: albumInputItems(62001)}
	req.SetScheduleDate(f.scheduleDate())
	updates, err := f.router.onMessagesSendMultiMedia(ctx, req)
	if err != nil {
		t.Fatalf("scheduled sendMultiMedia: %v", err)
	}
	scheduledGroup := sharedGroupedID(t, "scheduled echo", scheduledMessagesFromUpdates(updates), 3)
	if got := sharedGroupedID(t, "scheduled history", scheduledHistoryClasses(f.scheduledHistory(f.owner, peer)), 3); got != scheduledGroup {
		t.Fatalf("scheduled history grouped_id = %d, want echo %d", got, scheduledGroup)
	}
	first := scheduledMessagesFromUpdates(updates)[0].(*tg.Message).ID

	if _, err := f.router.onMessagesSendScheduledMessages(ctx, &tg.MessagesSendScheduledMessagesRequest{Peer: peer, ID: []int{first}}); err != nil {
		t.Fatalf("send first scheduled now: %v", err)
	}
	if !f.dispatch() {
		t.Fatalf("dispatcher sent nothing, want remaining album items")
	}

	for _, randomID := range []int64{62001, 62002, 62003} {
		msg, ok := f.privateByRandomID(randomID)
		if !ok {
			t.Fatalf("dispatched album item random_id=%d not in history", randomID)
		}
		if msg.GroupedID != scheduledGroup {
			t.Fatalf("dispatched message %d grouped_id = %d, want scheduled group %d", msg.ID, msg.GroupedID, scheduledGroup)
		}
	}
}

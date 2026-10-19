package rpc

import (
	"context"
	"sync"
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
// appmessages，定时消息的存取按 postgres store 的语义原样保留 GroupedID。
type albumScheduledMessages struct {
	*appmessages.Service
	mu        sync.Mutex
	scheduled []domain.ScheduledMessage
	nextID    int
}

func (s *albumScheduledMessages) ScheduleMessage(_ context.Context, userID int64, req domain.ScheduleMessageRequest) (domain.ScheduledMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if req.OwnerUserID == 0 {
		req.OwnerUserID = userID
	}
	s.nextID++
	msg := domain.ScheduledMessage{
		OwnerUserID:  req.OwnerUserID,
		ID:           s.nextID,
		Peer:         req.Peer,
		RandomID:     req.RandomID,
		Message:      req.Message,
		Entities:     append([]domain.MessageEntity(nil), req.Entities...),
		Media:        req.Media,
		RichMessage:  req.RichMessage,
		Silent:       req.Silent,
		NoForwards:   req.NoForwards,
		ReplyTo:      req.ReplyTo,
		Forward:      req.Forward,
		SendAs:       req.SendAs,
		GroupedID:    req.GroupedID,
		ScheduleDate: req.ScheduleDate,
		CreatedAt:    req.Date,
		UpdatedAt:    req.Date,
		State:        "pending",
	}
	s.scheduled = append(s.scheduled, msg)
	return msg, nil
}

func (s *albumScheduledMessages) EditScheduledMessage(context.Context, int64, domain.EditScheduledMessageRequest) (domain.ScheduledMessage, error) {
	return domain.ScheduledMessage{}, domain.ErrMessageIDInvalid
}

func (s *albumScheduledMessages) match(owner int64, peer domain.Peer, ids []int, states ...string) []domain.ScheduledMessage {
	want := make(map[int]bool, len(ids))
	for _, id := range ids {
		want[id] = true
	}
	out := make([]domain.ScheduledMessage, 0, len(s.scheduled))
	for _, msg := range s.scheduled {
		if owner != 0 && msg.OwnerUserID != owner {
			continue
		}
		if peer.ID != 0 && msg.Peer != peer {
			continue
		}
		if len(want) > 0 && !want[msg.ID] {
			continue
		}
		stateOK := false
		for _, state := range states {
			if msg.State == state {
				stateOK = true
			}
		}
		if stateOK {
			out = append(out, msg)
		}
	}
	return out
}

func (s *albumScheduledMessages) ListScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.match(userID, filter.Peer, nil, "pending", "dispatching")
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (s *albumScheduledMessages) GetScheduledMessages(_ context.Context, userID int64, filter domain.ScheduledMessageFilter) (domain.ScheduledMessageList, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := s.match(userID, filter.Peer, filter.IDs, "pending", "dispatching")
	return domain.ScheduledMessageList{Messages: items, Count: len(items)}, nil
}

func (s *albumScheduledMessages) DeleteScheduledMessages(context.Context, int64, domain.ScheduledMessageFilter, int) ([]domain.ScheduledMessage, error) {
	return nil, nil
}

func (s *albumScheduledMessages) setState(items []domain.ScheduledMessage, state string) []domain.ScheduledMessage {
	for i := range items {
		for j := range s.scheduled {
			if s.scheduled[j].OwnerUserID == items[i].OwnerUserID && s.scheduled[j].ID == items[i].ID {
				s.scheduled[j].State = state
				items[i].State = state
			}
		}
	}
	return items
}

func (s *albumScheduledMessages) ClaimScheduledMessages(_ context.Context, userID int64, claim domain.ScheduledMessageClaim) ([]domain.ScheduledMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setState(s.match(userID, claim.Peer, claim.IDs, "pending"), "dispatching"), nil
}

// ClaimDueScheduledMessages 忽略时间：测试里所有 pending 都视为已到期。
func (s *albumScheduledMessages) ClaimDueScheduledMessages(context.Context, int, int, int) ([]domain.ScheduledMessage, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.setState(s.match(0, domain.Peer{}, nil, "pending"), "dispatching"), nil
}

func (s *albumScheduledMessages) MarkScheduledMessageSent(_ context.Context, ownerUserID int64, id, sentMessageID, _ int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.scheduled {
		if s.scheduled[i].OwnerUserID == ownerUserID && s.scheduled[i].ID == id {
			s.scheduled[i].State = "sent"
			s.scheduled[i].SentMessageID = sentMessageID
		}
	}
	return nil
}

func (s *albumScheduledMessages) ReleaseScheduledMessage(_ context.Context, ownerUserID int64, id int, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.scheduled {
		if s.scheduled[i].OwnerUserID == ownerUserID && s.scheduled[i].ID == id {
			s.scheduled[i].State = "pending"
		}
	}
	return nil
}

func (s *albumScheduledMessages) HasScheduledMessages(_ context.Context, userID int64, peer domain.Peer) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.match(userID, peer, nil, "pending", "dispatching")) > 0, nil
}

func newAlbumScheduledRouter(t *testing.T) (*Router, *albumScheduledMessages, domain.User, domain.User) {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 11, Phone: "15550019001", FirstName: "Owner"})
	friend, _ := userStore.Create(ctx, domain.User{AccessHash: 12, Phone: "15550019002", FirstName: "Friend"})
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	messages := &albumScheduledMessages{Service: appmessages.NewService(messageStore, dialogStore)}
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

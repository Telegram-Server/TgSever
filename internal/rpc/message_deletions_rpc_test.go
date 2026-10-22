package rpc

import (
	"context"
	"testing"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type deletionLogFixture struct {
	ctx       context.Context
	alice     domain.User
	bob       domain.User
	messages  *memory.MessageStore
	service   *appmessages.Service
	deletions *memory.MessageDeletionStore
	router    *Router
}

// expiringMessages 在真实消息服务上补一个固定的到期认领结果；memory store
// 不实现 TTL 认领，删除本身仍走真实 store。
type expiringMessages struct {
	*appmessages.Service
	claims []domain.DeleteMessagesRequest
}

func (m *expiringMessages) ClaimExpiredPrivateMessages(context.Context, int, int) ([]domain.DeleteMessagesRequest, error) {
	claims := m.claims
	m.claims = nil
	return claims, nil
}

func newDeletionLogFixture(t *testing.T) deletionLogFixture {
	t.Helper()
	ctx := context.Background()
	users := memory.NewUserStore()
	alice, err := users.Create(ctx, domain.User{AccessHash: 3101, Phone: "15550031001", FirstName: "Alice"})
	if err != nil {
		t.Fatalf("create alice: %v", err)
	}
	bob, err := users.Create(ctx, domain.User{AccessHash: 3102, Phone: "15550031002", FirstName: "Bob"})
	if err != nil {
		t.Fatalf("create bob: %v", err)
	}
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	deletions := memory.NewMessageDeletionStore()
	messages.AttachDeletionStore(deletions)
	service := appmessages.NewService(messages, dialogs)
	router := New(Config{}, Deps{
		Users:    appusers.NewService(users),
		Messages: service,
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clock.System)
	return deletionLogFixture{ctx: ctx, alice: alice, bob: bob, messages: messages, service: service, deletions: deletions, router: router}
}

func (f deletionLogFixture) send(t *testing.T, from, to domain.User, randomID int64, body string) domain.SendPrivateTextResult {
	t.Helper()
	sent, err := f.messages.SendPrivateText(f.ctx, domain.SendPrivateTextRequest{
		SenderUserID:    from.ID,
		RecipientUserID: to.ID,
		RandomID:        randomID,
		Message:         body,
		Date:            1_700_310_000,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	return sent
}

func (f deletionLogFixture) deletionsBy(t *testing.T, actorUserID int64) []domain.MessageDeletion {
	t.Helper()
	got, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{ActorUserID: actorUserID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	return got
}

func TestMessagesDeleteMessagesRecordsTheDeletingSession(t *testing.T) {
	f := newDeletionLogFixture(t)
	sent := f.send(t, f.bob, f.alice, 310_001, "unsend this")
	authKeyID := [8]byte{0x31, 0, 0, 0, 0, 0, 0, 0x01}
	req := &tg.MessagesDeleteMessagesRequest{Revoke: true, ID: []int{sent.RecipientMessage.ID}}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	if _, err := f.router.Dispatch(WithUserID(f.ctx, f.alice.ID), authKeyID, 77, &in); err != nil {
		t.Fatalf("dispatch messages.deleteMessages: %v", err)
	}

	got := f.deletionsBy(t, f.alice.ID)
	if len(got) != 2 {
		t.Fatalf("deletions by alice = %+v, want both copies of bob's message", got)
	}
	for _, row := range got {
		if row.Source != domain.DeletionSourceUser || row.ActorType != domain.DeletionActorUser ||
			row.OriginAuthKeyID != authKeyID || row.OriginSessionID != 77 {
			t.Fatalf("deletion = %+v, want alice's session %x/77 revoking bob's message", row, authKeyID)
		}
	}
}

func TestBotAPIDeleteMessageRecordsTheBot(t *testing.T) {
	f := newDeletionLogFixture(t)
	bot := f.bob
	sent, err := f.messages.SendPrivateText(f.ctx, domain.SendPrivateTextRequest{
		SenderUserID: bot.ID, RecipientUserID: f.alice.ID, RandomID: 310_003,
		Message: "stale menu", Date: int(f.router.clock.Now().Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}

	if ok, err := f.router.BotAPIDeleteMessage(f.ctx, bot.ID, f.alice.ID, sent.SenderMessage.ID); err != nil || !ok {
		t.Fatalf("BotAPIDeleteMessage = %v, %v", ok, err)
	}

	got := f.deletionsBy(t, bot.ID)
	if len(got) != 2 {
		t.Fatalf("deletions by bot = %+v, want both copies", got)
	}
	for _, row := range got {
		if row.Source != domain.DeletionSourceBot || row.ActorType != domain.DeletionActorBot || row.Scope != domain.DeletionScopeBoth {
			t.Fatalf("deletion = %+v, want a bot api deletion by bot %d", row, bot.ID)
		}
	}
}

// 群里经 Bot API 的删除同样记为机器人删除，而不是普通用户会话删除。
func TestBotAPIDeleteChannelMessageRecordsTheBot(t *testing.T) {
	f := newChannelDeletionLogFixture(t)
	bot := f.member
	sent, err := f.store.SendChannelMessage(f.ctx, domain.SendChannelMessageRequest{
		UserID: bot, ChannelID: f.channel.ID, RandomID: 320_103,
		Message: "stale group menu", Date: int(f.router.clock.Now().Unix()),
	})
	if err != nil {
		t.Fatal(err)
	}
	msg := sent.Message

	chatID := -botAPIChannelChatIDBase - f.channel.ID
	if ok, err := f.router.BotAPIDeleteMessage(f.ctx, bot, chatID, msg.ID); err != nil || !ok {
		t.Fatalf("BotAPIDeleteMessage = %v, %v", ok, err)
	}

	got, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{ActorUserID: bot, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	if len(got) != 1 || got[0].MessageID != msg.ID || got[0].Source != domain.DeletionSourceBot || got[0].ActorType != domain.DeletionActorBot {
		t.Fatalf("deletions = %+v, want a bot api deletion of message %d by bot %d", got, msg.ID, bot)
	}
}

type channelDeletionLogFixture struct {
	ctx       context.Context
	owner     int64
	member    int64
	channel   domain.Channel
	store     *memory.ChannelStore
	service   *appchannels.Service
	deletions *memory.MessageDeletionStore
	router    *Router
}

func newChannelDeletionLogFixture(t *testing.T) channelDeletionLogFixture {
	t.Helper()
	ctx := context.Background()
	const owner, member = int64(1000003201), int64(1000003202)
	store := memory.NewChannelStore()
	deletions := memory.NewMessageDeletionStore()
	store.AttachDeletionStore(deletions)
	created, err := store.CreateChannel(ctx, domain.CreateChannelRequest{
		CreatorUserID: owner,
		Title:         "rpc deletion log",
		Megagroup:     true,
		MemberUserIDs: []int64{member},
		Date:          1_700_320_000,
	})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	service := appchannels.NewService(store)
	router := New(Config{}, Deps{Channels: service, Sessions: &captureSessions{}}, zaptest.NewLogger(t), clock.System)
	return channelDeletionLogFixture{ctx: ctx, owner: owner, member: member, channel: created.Channel, store: store, service: service, deletions: deletions, router: router}
}

func (f channelDeletionLogFixture) send(t *testing.T, from int64, randomID int64, body string) domain.ChannelMessage {
	t.Helper()
	res, err := f.store.SendChannelMessage(f.ctx, domain.SendChannelMessageRequest{
		UserID: from, ChannelID: f.channel.ID, RandomID: randomID, Message: body, Date: 1_700_320_001,
	})
	if err != nil {
		t.Fatalf("send channel message: %v", err)
	}
	return res.Message
}

// expiringChannels 在真实频道服务上补一个固定的到期认领结果；memory store
// 不实现 TTL 认领，删除本身仍走真实 store。
type expiringChannels struct {
	*appchannels.Service
	claims []domain.DeleteChannelMessagesRequest
}

func (c *expiringChannels) ClaimExpiredMessages(context.Context, int, int) ([]domain.DeleteChannelMessagesRequest, error) {
	claims := c.claims
	c.claims = nil
	return claims, nil
}

func TestChannelsDeleteMessagesRecordsTheDeletingSession(t *testing.T) {
	f := newChannelDeletionLogFixture(t)
	msg := f.send(t, f.member, 320_101, "typo")
	authKeyID := [8]byte{0x32, 0, 0, 0, 0, 0, 0, 0x02}
	req := &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		ID:      []int{msg.ID},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatalf("encode request: %v", err)
	}

	if _, err := f.router.Dispatch(WithUserID(f.ctx, f.member), authKeyID, 88, &in); err != nil {
		t.Fatalf("dispatch channels.deleteMessages: %v", err)
	}

	got, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{ActorUserID: f.member, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	if len(got) != 1 || got[0].MessageID != msg.ID || got[0].OriginAuthKeyID != authKeyID || got[0].OriginSessionID != 88 {
		t.Fatalf("deletions = %+v, want message %d from session %x/88", got, msg.ID, authKeyID)
	}
}

func TestChannelsDeleteMessagesDefaultRejectsMixedBatchAtomically(t *testing.T) {
	f := newChannelDeletionLogFixture(t)
	own := f.send(t, f.member, 320_148, "member's own message")
	other := f.send(t, f.owner, 320_149, "owner's message")
	before, err := f.store.GetChannel(f.ctx, f.member, f.channel.ID)
	if err != nil {
		t.Fatal(err)
	}
	req := &tg.ChannelsDeleteMessagesRequest{
		Channel: &tg.InputChannel{ChannelID: f.channel.ID, AccessHash: f.channel.AccessHash},
		ID:      []int{own.ID, other.ID},
	}
	var in bin.Buffer
	if err := req.Encode(&in); err != nil {
		t.Fatal(err)
	}
	if _, err := f.router.Dispatch(WithUserID(f.ctx, f.member), [8]byte{48}, 48, &in); !tgerr.Is(err, "MESSAGE_DELETE_FORBIDDEN") {
		t.Fatalf("mixed MTProto deletion error = %v, want MESSAGE_DELETE_FORBIDDEN", err)
	}
	history, err := f.store.GetChannelMessages(f.ctx, f.owner, f.channel.ID, []int{own.ID, other.ID})
	if err != nil {
		t.Fatal(err)
	}
	if len(history.Messages) != 2 {
		t.Fatalf("messages after rejected batch = %+v, want both unchanged", history.Messages)
	}
	byID := make(map[int]string, len(history.Messages))
	for _, message := range history.Messages {
		byID[message.ID] = message.Body
	}
	if byID[own.ID] != own.Body || byID[other.ID] != other.Body || history.Channel.Pts != before.Channel.Pts {
		t.Fatalf("rejected batch changed history = %+v", history)
	}
	diff, err := f.store.ListChannelDifference(f.ctx, domain.ChannelDifferenceRequest{
		UserID: f.owner, ChannelID: f.channel.ID, Pts: before.Channel.Pts, Limit: 100,
	})
	if err != nil || diff.Pts != before.Channel.Pts || len(diff.OtherUpdates) != 0 || len(diff.NewMessages) != 0 {
		t.Fatalf("difference after rejected batch = %+v, %v; want no update", diff, err)
	}
	rows, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{ActorUserID: f.member, Limit: 10})
	if err != nil || len(rows) != 0 {
		t.Fatalf("audit after rejected batch = %+v, %v; want no deletion", rows, err)
	}
	// The application-only opt-in must not leak into the default MTProto route.
	// It uses the same service but deliberately permits this partial batch.
	skipped, err := f.service.DeleteMessages(f.ctx, f.member, domain.DeleteChannelMessagesRequest{
		ChannelID: f.channel.ID, IDs: []int{own.ID, other.ID}, SkipUnauthorized: true, Date: 1_700_320_048,
	})
	if err != nil || len(skipped.DeletedIDs) != 1 || skipped.DeletedIDs[0] != own.ID ||
		skipped.Event.PtsCount != 1 || skipped.Event.Pts != before.Channel.Pts+1 {
		t.Fatalf("application opt-in deletion = %+v, %v; want only own message", skipped, err)
	}
	req.ID = []int{other.ID}
	if _, err := f.router.onChannelsDeleteMessages(WithUserID(f.ctx, f.member), req); !tgerr.Is(err, "MESSAGE_DELETE_FORBIDDEN") {
		t.Fatalf("MTProto after opt-in error = %v, want MESSAGE_DELETE_FORBIDDEN", err)
	}
	history, err = f.store.GetChannelMessages(f.ctx, f.owner, f.channel.ID, []int{own.ID, other.ID})
	if err != nil || len(history.Messages) != 1 || history.Messages[0].ID != other.ID ||
		history.Channel.Pts != before.Channel.Pts+1 {
		t.Fatalf("history after opt-in = %+v, %v; want preserved other author's message", history, err)
	}
}

func TestExpiryDispatcherRecordsChannelTTLDeletionsAsSystem(t *testing.T) {
	f := newChannelDeletionLogFixture(t)
	msg := f.send(t, f.member, 320_102, "self destruct in group")
	// 存储层按群主身份认领到期消息（只为通过删除权限校验）。
	f.router.deps.Channels = &expiringChannels{
		Service: f.service,
		claims: []domain.DeleteChannelMessagesRequest{{
			UserID: f.owner, ChannelID: f.channel.ID, IDs: []int{msg.ID}, Date: 1_700_320_100,
		}},
	}

	if !NewExpiryDispatcher(f.router, zaptest.NewLogger(t)).DispatchOnce(f.ctx) {
		t.Fatal("expiry dispatcher found no work")
	}

	got, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{SenderUserID: f.member, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	if len(got) != 1 || got[0].Source != domain.DeletionSourceTTL || got[0].ActorType != domain.DeletionActorSystem || got[0].ActorUserID != 0 {
		t.Fatalf("deletions = %+v, want a system ttl deletion, not one attributed to the group owner", got)
	}
}

func TestExpiryDispatcherRecordsTTLDeletionsAsSystem(t *testing.T) {
	f := newDeletionLogFixture(t)
	sent := f.send(t, f.bob, f.alice, 310_002, "self destruct")
	f.router.deps.Messages = &expiringMessages{
		Service: f.service,
		claims: []domain.DeleteMessagesRequest{{
			OwnerUserID: f.alice.ID,
			IDs:         []int{sent.RecipientMessage.ID},
			Revoke:      true,
			Date:        1_700_310_100,
		}},
	}

	if !NewExpiryDispatcher(f.router, zaptest.NewLogger(t)).DispatchOnce(f.ctx) {
		t.Fatal("expiry dispatcher found no work")
	}

	got, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{SenderUserID: f.bob.ID, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("deletions = %+v, want both expired copies", got)
	}
	for _, row := range got {
		if row.Source != domain.DeletionSourceTTL || row.ActorType != domain.DeletionActorSystem ||
			row.ActorUserID != 0 || row.Scope != domain.DeletionScopeBoth {
			t.Fatalf("deletion = %+v, want a system ttl deletion of both copies, not one attributed to alice", row)
		}
	}
}

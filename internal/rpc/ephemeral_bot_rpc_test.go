package rpc

import (
	"context"
	"testing"
	"time"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"github.com/iamxvbaba/td/tlprofile"
	"go.uber.org/zap/zaptest"

	ephemeralapp "telesrv/internal/app/ephemeral"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// bot 经 MTProto 收发临时消息：Router 处理器 + 真实 app/ephemeral 业务层 + memory store。
const (
	ephemeralBotID      int64 = 1001
	ephemeralOtherBotID int64 = 1002
	ephemeralHumanID    int64 = 2001
	ephemeralChannelID  int64 = 3001
	ephemeralAccessHash int64 = 7
)

var (
	ephemeralBotKey   = [8]byte{1, 1, 1}
	ephemeralOtherKey = [8]byte{2, 2, 2}
	ephemeralHumanKey = [8]byte{3, 3, 3}
)

type ephemeralBotChannels struct {
	ChannelsService
	roles map[int64]domain.ChannelMemberRole
}

func (c *ephemeralBotChannels) channel() domain.Channel {
	return domain.Channel{ID: ephemeralChannelID, AccessHash: ephemeralAccessHash, Title: "Group", Megagroup: true}
}

func (c *ephemeralBotChannels) member(userID int64) domain.ChannelMember {
	return domain.ChannelMember{ChannelID: ephemeralChannelID, UserID: userID, Role: c.roles[userID], Status: domain.ChannelMemberActive}
}

func (c *ephemeralBotChannels) ResolveChannel(_ context.Context, userID, channelID int64) (domain.ChannelView, error) {
	if channelID != ephemeralChannelID {
		return domain.ChannelView{}, domain.ErrChannelInvalid
	}
	return domain.ChannelView{Channel: c.channel(), Self: c.member(userID)}, nil
}

func (c *ephemeralBotChannels) GetParticipant(_ context.Context, _, channelID, participantUserID int64) (domain.ChannelMember, error) {
	if channelID != ephemeralChannelID {
		return domain.ChannelMember{}, domain.ErrChannelInvalid
	}
	return c.member(participantUserID), nil
}

func (c *ephemeralBotChannels) GetForumTopicsByID(context.Context, int64, int64, []int) (domain.ChannelForumTopicList, error) {
	return domain.ChannelForumTopicList{Channel: c.channel()}, nil
}

type ephemeralBotCommands map[int64][]domain.BotCommand

func (b ephemeralBotCommands) ResolveBotCommands(_ context.Context, botUserIDs []int64, _ domain.BotCommandView) (map[int64][]domain.BotCommand, error) {
	out := make(map[int64][]domain.BotCommand, len(botUserIDs))
	for _, id := range botUserIDs {
		out[id] = b[id]
	}
	return out, nil
}

type ephemeralBotFixture struct {
	router   *Router
	sessions *ephemeralPushSessions
	peer     *tg.InputPeerChannel
}

func newEphemeralBotFixture(t *testing.T) *ephemeralBotFixture {
	t.Helper()
	users := mapUsersService{users: map[int64]domain.User{
		ephemeralBotID:      {ID: ephemeralBotID, FirstName: "Bot", Username: "private_bot", Bot: true},
		ephemeralOtherBotID: {ID: ephemeralOtherBotID, FirstName: "Other", Username: "other_bot", Bot: true},
		ephemeralHumanID:    {ID: ephemeralHumanID, FirstName: "Alice"},
	}}
	channels := &ephemeralBotChannels{roles: map[int64]domain.ChannelMemberRole{
		ephemeralBotID:      domain.ChannelRoleAdmin,
		ephemeralOtherBotID: domain.ChannelRoleAdmin,
		ephemeralHumanID:    domain.ChannelRoleMember,
	}}
	service := ephemeralapp.NewService(memory.NewEphemeralMessageStore(), channels, users,
		ephemeralBotCommands{ephemeralBotID: {{Command: "private", Description: "private", Ephemeral: true}}})
	sessions := &ephemeralPushSessions{online: true}
	router := New(Config{InstanceID: "test"}, Deps{
		Ephemeral: service, Users: users, Channels: channels, Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	return &ephemeralBotFixture{
		router: router, sessions: sessions,
		peer: &tg.InputPeerChannel{ChannelID: ephemeralChannelID, AccessHash: ephemeralAccessHash},
	}
}

func ephemeralCtx(userID int64, key [8]byte, sessionID int64) context.Context {
	return WithSessionID(WithAuthKeyID(WithUserID(context.Background(), userID), key), sessionID)
}

func (f *ephemeralBotFixture) botCtx() context.Context {
	return ephemeralCtx(ephemeralBotID, ephemeralBotKey, 11)
}

func (f *ephemeralBotFixture) humanCtx() context.Context {
	return ephemeralCtx(ephemeralHumanID, ephemeralHumanKey, 22)
}

func (f *ephemeralBotFixture) botSend(t *testing.T, text string, randomID int64) tg.EphemeralMessage {
	t.Helper()
	updates, err := f.router.onEphemeralSendMessage(f.botCtx(), &tg.EphemeralSendMessageRequest{
		Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralHumanID}, Message: text, RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("bot send: %v", err)
	}
	return singleEphemeralUpdate[*tg.UpdateNewEphemeralMessage](t, updates).Message
}

// lastPush returns the most recent push captured for userID, broadcast or device-targeted.
func (f *ephemeralBotFixture) lastPush(t *testing.T, userID int64) ephemeralPushCapture {
	t.Helper()
	f.sessions.mu.Lock()
	defer f.sessions.mu.Unlock()
	all := append(append([]ephemeralPushCapture(nil), f.sessions.broadcasts...), f.sessions.targeted...)
	var found *ephemeralPushCapture
	for i := range all {
		if all[i].userID == userID {
			found = &all[i]
		}
	}
	if found == nil {
		t.Fatalf("no push for user %d (broadcasts=%d targeted=%d)", userID, len(f.sessions.broadcasts), len(f.sessions.targeted))
	}
	return *found
}

func singleEphemeralUpdate[T tg.UpdateClass](t *testing.T, updates tg.UpdatesClass) T {
	t.Helper()
	result, ok := updates.(*tg.Updates)
	if !ok || len(result.Updates) != 1 || result.Seq != 0 {
		t.Fatalf("updates = %#v", updates)
	}
	update, ok := result.Updates[0].(T)
	if !ok {
		t.Fatalf("update type = %T", result.Updates[0])
	}
	return update
}

func TestEphemeralBotSendMessageReachesOnlineReceiver(t *testing.T) {
	f := newEphemeralBotFixture(t)
	sent := f.botSend(t, "only for you", 501)
	if !sent.Out || sent.ReceiverID != ephemeralHumanID || sent.Message != "only for you" || sent.ID <= 0 {
		t.Fatalf("sent = %+v", sent)
	}
	push := f.lastPush(t, ephemeralHumanID)
	if push.semantic != tlprofile.SemanticTypeUpdateNewEphemeralMessage {
		t.Fatalf("push semantic = %#x", push.semantic)
	}
	received := singleEphemeralUpdate[*tg.UpdateNewEphemeralMessage](t, push.message).Message
	if received.Out || received.ID != sent.ID || received.Message != "only for you" {
		t.Fatalf("received = %+v", received)
	}
}

func (f *ephemeralBotFixture) humanSendToBot(t *testing.T, text string, randomID int64) tg.EphemeralMessage {
	t.Helper()
	updates, err := f.router.onEphemeralSendMessage(f.humanCtx(), &tg.EphemeralSendMessageRequest{
		Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralBotID}, Message: text, RandomID: randomID,
	})
	if err != nil {
		t.Fatalf("human send: %v", err)
	}
	return singleEphemeralUpdate[*tg.UpdateNewEphemeralMessage](t, updates).Message
}

func TestEphemeralBotReceivesUserMessageAndRepliesToThatDevice(t *testing.T) {
	f := newEphemeralBotFixture(t)
	asked := f.humanSendToBot(t, "/private@private_bot ping", 601)
	toBot := f.lastPush(t, ephemeralBotID)
	if toBot.semantic != tlprofile.SemanticTypeUpdateNewEphemeralMessage {
		t.Fatalf("bot push semantic = %#x", toBot.semantic)
	}
	if got := singleEphemeralUpdate[*tg.UpdateNewEphemeralMessage](t, toBot.message).Message; got.ID != asked.ID || got.Out {
		t.Fatalf("bot received = %+v", got)
	}

	updates, err := f.router.onEphemeralSendMessage(f.botCtx(), &tg.EphemeralSendMessageRequest{
		Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralHumanID}, Message: "pong", RandomID: 602,
		ReplyTo: &tg.InputReplyToEphemeralMessage{ID: asked.ID},
	})
	if err != nil {
		t.Fatalf("bot reply: %v", err)
	}
	reply := singleEphemeralUpdate[*tg.UpdateNewEphemeralMessage](t, updates).Message
	header, ok := reply.ReplyTo.(*tg.MessageReplyHeader)
	if !ok || !header.ReplyToEphemeral || header.ReplyToMsgID != asked.ID {
		t.Fatalf("reply header = %#v", reply.ReplyTo)
	}
	push := f.lastPush(t, ephemeralHumanID)
	if push.authKey != ephemeralHumanKey {
		t.Fatalf("reply push targeted auth key %v, want the asking device %v", push.authKey, ephemeralHumanKey)
	}
}

func TestEphemeralCallbackReachesOnlineBotAsEphemeralCallbackQuery(t *testing.T) {
	f := newEphemeralBotFixture(t)
	updates, err := f.router.onEphemeralSendMessage(f.botCtx(), &tg.EphemeralSendMessageRequest{
		Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralHumanID}, Message: "choose", RandomID: 701,
		ReplyMarkup: &tg.ReplyInlineMarkup{Rows: []tg.KeyboardInlineButtonRow{{
			Buttons: []tg.KeyboardInlineButton{{Text: "Go", Type: &tg.InlineButtonTypeCallback{Data: []byte("go")}}},
		}}},
	})
	if err != nil {
		t.Fatalf("bot send: %v", err)
	}
	sent := singleEphemeralUpdate[*tg.UpdateNewEphemeralMessage](t, updates).Message
	if sent.ReplyMarkup == nil {
		t.Fatalf("bot inline markup dropped: %+v", sent)
	}

	// 没有 bot 应答：短超时让等待返回，只观察推送。
	ctx, cancel := context.WithTimeout(f.humanCtx(), 50*time.Millisecond)
	defer cancel()
	request := &tg.EphemeralGetCallbackAnswerRequest{Peer: f.peer, ID: sent.ID}
	request.SetData([]byte("go"))
	_, _ = f.router.onEphemeralGetCallbackAnswer(ctx, request)

	push := f.lastPush(t, ephemeralBotID)
	if push.semantic != tlprofile.SemanticTypeUpdateEphemeralBotCallbackQuery {
		t.Fatalf("callback push semantic = %#x", push.semantic)
	}
	callback := singleEphemeralUpdate[*tg.UpdateEphemeralBotCallbackQuery](t, push.message)
	if callback.UserID != ephemeralHumanID || callback.MsgID != sent.ID || string(callback.Data) != "go" ||
		callback.QueryID == 0 || callback.Message.ID != sent.ID || !callback.Message.Out {
		t.Fatalf("callback = %+v", callback)
	}
}

func (f *ephemeralBotFixture) editRequest(id int, text string) *tg.EphemeralEditMessageRequest {
	request := &tg.EphemeralEditMessageRequest{Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralHumanID}, ID: id}
	request.SetMessage(text)
	return request
}

func TestEphemeralBotEditMessageUpdatesReceiver(t *testing.T) {
	f := newEphemeralBotFixture(t)
	sent := f.botSend(t, "draft", 801)
	updates, err := f.router.onEphemeralEditMessage(f.botCtx(), f.editRequest(sent.ID, "final"))
	if err != nil {
		t.Fatalf("bot edit: %v", err)
	}
	if edited := singleEphemeralUpdate[*tg.UpdateEditEphemeralMessage](t, updates).Message; edited.Message != "final" || !edited.Out {
		t.Fatalf("edit result = %+v", edited)
	}
	push := f.lastPush(t, ephemeralHumanID)
	if push.semantic != tlprofile.SemanticTypeUpdateEditEphemeralMessage {
		t.Fatalf("edit push semantic = %#x", push.semantic)
	}
	if received := singleEphemeralUpdate[*tg.UpdateEditEphemeralMessage](t, push.message).Message; received.ID != sent.ID || received.Message != "final" {
		t.Fatalf("receiver saw = %+v", received)
	}
}

func TestEphemeralBotDeleteMessageUpdatesReceiver(t *testing.T) {
	f := newEphemeralBotFixture(t)
	sent := f.botSend(t, "oops", 901)
	ok, err := f.router.onEphemeralDeleteMessage(f.botCtx(), &tg.EphemeralDeleteMessageRequest{
		Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralHumanID}, ID: sent.ID,
	})
	if err != nil || !ok {
		t.Fatalf("bot delete = %v, %v", ok, err)
	}
	push := f.lastPush(t, ephemeralHumanID)
	if push.semantic != tlprofile.SemanticTypeUpdateDeleteEphemeralMessages {
		t.Fatalf("delete push semantic = %#x", push.semantic)
	}
	if deleted := singleEphemeralUpdate[*tg.UpdateDeleteEphemeralMessages](t, push.message); len(deleted.IDs) != 1 || deleted.IDs[0] != sent.ID {
		t.Fatalf("delete update = %+v", deleted)
	}
}

func TestEphemeralEditAndDeleteRequireOwnMessage(t *testing.T) {
	f := newEphemeralBotFixture(t)
	sent := f.botSend(t, "mine", 1001)
	otherBot := ephemeralCtx(ephemeralOtherBotID, ephemeralOtherKey, 33)

	if _, err := f.router.onEphemeralEditMessage(otherBot, f.editRequest(sent.ID, "hijack")); !tgerr.Is(err, "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("other bot edit err=%v, want CHAT_WRITE_FORBIDDEN", err)
	}
	if _, err := f.router.onEphemeralDeleteMessage(otherBot, &tg.EphemeralDeleteMessageRequest{
		Peer: f.peer, ReceiverID: &tg.InputUser{UserID: ephemeralHumanID}, ID: sent.ID,
	}); !tgerr.Is(err, "CHAT_WRITE_FORBIDDEN") {
		t.Fatalf("other bot delete err=%v, want CHAT_WRITE_FORBIDDEN", err)
	}
	// 非 bot 账号的非 welcome 编辑沿用原规则。
	if _, err := f.router.onEphemeralEditMessage(f.humanCtx(), f.editRequest(sent.ID, "human")); !tgerr.Is(err, "INPUT_REQUEST_INVALID") {
		t.Fatalf("human edit err=%v, want INPUT_REQUEST_INVALID", err)
	}
	updates, err := f.router.onEphemeralEditMessage(f.botCtx(), f.editRequest(sent.ID, "still mine"))
	if err != nil {
		t.Fatalf("owner edit after rejected attempts: %v", err)
	}
	if edited := singleEphemeralUpdate[*tg.UpdateEditEphemeralMessage](t, updates).Message; edited.Message != "still mine" {
		t.Fatalf("edited = %+v", edited)
	}
}

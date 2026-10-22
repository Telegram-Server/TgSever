package rpc

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/iamxvbaba/td/tg"
	"github.com/iamxvbaba/td/tgerr"
	"go.uber.org/zap/zaptest"

	appbots "telesrv/internal/app/bots"
	appchannels "telesrv/internal/app/channels"
	appmessages "telesrv/internal/app/messages"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// bannedSendAsFixture：host 建群；poster（Premium）是群成员，拥有两个广播频道 banned / other，
// 可以在群里以它们的身份发言。测试由 host 封禁 banned，验证各 send_as 入口被拒、解封后恢复，
// other 不受影响。
type bannedSendAsFixture struct {
	t        *testing.T
	ctx      context.Context
	clock    *scheduledTestClock
	router   *Router
	messages *appmessages.Service
	host     domain.User
	poster   domain.User
	bot      domain.User
	group    *tg.Channel
	banned   *tg.Channel
	other    *tg.Channel
}

func newBannedSendAsFixture(t *testing.T) bannedSendAsFixture {
	t.Helper()
	ctx := context.Background()
	userStore := memory.NewUserStore()
	host, err := userStore.Create(ctx, domain.User{AccessHash: 1381, Phone: "15550013801", FirstName: "Host"})
	if err != nil {
		t.Fatalf("create host: %v", err)
	}
	poster, err := userStore.Create(ctx, domain.User{AccessHash: 1382, Phone: "15550013802", FirstName: "Poster", PremiumUntil: int(time.Now().Add(72 * time.Hour).Unix())})
	if err != nil {
		t.Fatalf("create poster: %v", err)
	}
	dialogStore := memory.NewDialogStore()
	messageStore := memory.NewMessageStore(dialogStore)
	bots := appbots.NewService(userStore, memory.NewBotStore(userStore), messageStore)
	bot, _, err := bots.CreateBot(ctx, host.ID, "Send As Bot", "send_as_banned_bot")
	if err != nil {
		t.Fatalf("create bot: %v", err)
	}
	messages := appmessages.NewService(messageStore, dialogStore)
	clk := &scheduledTestClock{now: time.Now()}
	r := New(Config{DC: 2, IP: "127.0.0.1", Port: 2398}, Deps{
		Users:    appusers.NewService(userStore),
		Channels: appchannels.NewService(memory.NewChannelStore(), appchannels.WithBotProfileResolver(bots)),
		Messages: messages,
		Bots:     bots,
		Files:    albumFakeFiles(),
		Sessions: &captureSessions{},
	}, zaptest.NewLogger(t), clk)
	created, err := r.onMessagesCreateChat(WithUserID(ctx, host.ID), &tg.MessagesCreateChatRequest{
		Users: []tg.InputUserClass{&tg.InputUser{UserID: poster.ID, AccessHash: poster.AccessHash}},
		Title: "Banned Send As Group",
	})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	return bannedSendAsFixture{
		t:        t,
		ctx:      ctx,
		clock:    clk,
		router:   r,
		messages: messages,
		host:     host,
		poster:   poster,
		bot:      bot,
		group:    created.Updates.(*tg.Updates).Chats[0].(*tg.Channel),
		banned:   createBroadcastChannelRPC(t, r, poster.ID, "Banned Channel"),
		other:    createBroadcastChannelRPC(t, r, poster.ID, "Other Channel"),
	}
}

func (f bannedSendAsFixture) posterCtx() context.Context { return WithUserID(f.ctx, f.poster.ID) }

// setBanned 由群主经 channels.editBanned 封禁（true）或解封（false）banned 频道。
func (f bannedSendAsFixture) setBanned(banned bool) {
	f.t.Helper()
	rights := tg.ChatBannedRights{}
	if banned {
		rights = tg.ChatBannedRights{ViewMessages: true, SendMessages: true, UntilDate: 2147483647}
	}
	if _, err := f.router.onChannelsEditBanned(WithUserID(f.ctx, f.host.ID), &tg.ChannelsEditBannedRequest{
		Channel:      &tg.InputChannel{ChannelID: f.group.ID, AccessHash: f.group.AccessHash},
		Participant:  inputPeerChannel(f.banned),
		BannedRights: rights,
	}); err != nil {
		f.t.Fatalf("edit banned sender chat (%v): %v", banned, err)
	}
}

func wantSendAsPeerInvalid(t *testing.T, label string, err error) {
	t.Helper()
	if !tgerr.Is(err, "SEND_AS_PEER_INVALID") {
		t.Fatalf("%s: err = %v, want SEND_AS_PEER_INVALID", label, err)
	}
}

// channelMessageSenders 返回更新里所有新频道消息的 from_id。
func channelMessageSenders(t *testing.T, label string, updates tg.UpdatesClass) []int64 {
	t.Helper()
	up, ok := updates.(*tg.Updates)
	if !ok {
		t.Fatalf("%s: updates %T, want *tg.Updates", label, updates)
	}
	var out []int64
	for _, u := range up.Updates {
		v, ok := u.(*tg.UpdateNewChannelMessage)
		if !ok {
			continue
		}
		var fromID tg.PeerClass
		switch msg := v.Message.(type) {
		case *tg.Message:
			fromID = msg.FromID
		case *tg.MessageService: // createForumTopic 产出的话题创建服务消息
			fromID = msg.FromID
		default:
			continue
		}
		from, ok := fromID.(*tg.PeerChannel)
		if !ok {
			t.Fatalf("%s: from_id = %#v, want a channel", label, fromID)
		}
		out = append(out, from.ChannelID)
	}
	if len(out) == 0 {
		t.Fatalf("%s: no new channel message in %#v", label, up.Updates)
	}
	return out
}

func wantSentAs(t *testing.T, label string, updates tg.UpdatesClass, channelID int64) {
	t.Helper()
	for _, from := range channelMessageSenders(t, label, updates) {
		if from != channelID {
			t.Fatalf("%s: sent as channel %d, want %d", label, from, channelID)
		}
	}
}

func TestBannedSenderChatCannotSendMessage(t *testing.T) {
	f := newBannedSendAsFixture(t)
	send := func(sendAs *tg.Channel, randomID int64) (tg.UpdatesClass, error) {
		return f.router.onMessagesSendMessage(f.posterCtx(), &tg.MessagesSendMessageRequest{
			Peer: inputPeerChannel(f.group), Message: "hello", RandomID: randomID, SendAs: inputPeerChannel(sendAs),
		})
	}

	f.setBanned(true)
	_, err := send(f.banned, 138001)
	wantSendAsPeerInvalid(t, "send as banned channel", err)
	other, err := send(f.other, 138002)
	if err != nil {
		t.Fatalf("send as unbanned channel: %v", err)
	}
	wantSentAs(t, "send as unbanned channel", other, f.other.ID)

	f.setBanned(false)
	restored, err := send(f.banned, 138003)
	if err != nil {
		t.Fatalf("send after unban: %v", err)
	}
	wantSentAs(t, "send after unban", restored, f.banned.ID)
}

// failingSenderChatBans 让封禁查询失败，其余能力委托给真实服务。
type failingSenderChatBans struct{ ChannelsService }

func (failingSenderChatBans) IsSenderChatBanned(context.Context, int64, int64) (bool, error) {
	return false, errors.New("store down")
}

// 封禁查询失败时按拒绝处理：显式 send_as 返回 INTERNAL_SERVER_ERROR，不放行；以本人身份发言不查询，不受影响。
func TestBannedSenderChatLookupFailureFailsClosed(t *testing.T) {
	f := newBannedSendAsFixture(t)
	f.router.deps.Channels = failingSenderChatBans{f.router.deps.Channels}
	_, err := f.router.onMessagesSendMessage(f.posterCtx(), &tg.MessagesSendMessageRequest{
		Peer: inputPeerChannel(f.group), Message: "as channel", RandomID: 138901, SendAs: inputPeerChannel(f.other),
	})
	if !tgerr.Is(err, "INTERNAL_SERVER_ERROR") {
		t.Fatalf("send as channel with failing ban lookup: err = %v, want INTERNAL_SERVER_ERROR", err)
	}
	if _, err := f.router.onMessagesSendMessage(f.posterCtx(), &tg.MessagesSendMessageRequest{
		Peer: inputPeerChannel(f.group), Message: "as self", RandomID: 138902,
	}); err != nil {
		t.Fatalf("send as self with failing ban lookup: %v", err)
	}
}

// assertBannedThenRestored 对一个发言入口：封禁后以 banned 身份被拒（SEND_AS_PEER_INVALID），
// 解封后同一入口以 banned 身份发言成功。attempt 每次调用须使用新的 random_id。
func (f bannedSendAsFixture) assertBannedThenRestored(label string, attempt func(sendAs *tg.Channel) (tg.UpdatesClass, error)) {
	f.t.Helper()
	f.setBanned(true)
	_, err := attempt(f.banned)
	wantSendAsPeerInvalid(f.t, label+" while banned", err)
	f.setBanned(false)
	updates, err := attempt(f.banned)
	if err != nil {
		f.t.Fatalf("%s after unban: %v", label, err)
	}
	wantSentAs(f.t, label+" after unban", updates, f.banned.ID)
}

// randomIDs 返回一个每次调用递增的 random_id 发生器。
func randomIDs(base int64) func() int64 {
	next := base
	return func() int64 {
		next++
		return next
	}
}

func TestBannedSenderChatCannotSendMedia(t *testing.T) {
	f := newBannedSendAsFixture(t)
	nextID := randomIDs(138100)
	f.assertBannedThenRestored("sendMedia", func(sendAs *tg.Channel) (tg.UpdatesClass, error) {
		return f.router.onMessagesSendMedia(f.posterCtx(), &tg.MessagesSendMediaRequest{
			Peer:     inputPeerChannel(f.group),
			Media:    &tg.InputMediaDocument{ID: &tg.InputDocument{ID: 555, AccessHash: 5}},
			RandomID: nextID(),
			SendAs:   inputPeerChannel(sendAs),
		})
	})
}

func TestBannedSenderChatCannotSendAlbum(t *testing.T) {
	f := newBannedSendAsFixture(t)
	base := int64(138200)
	f.assertBannedThenRestored("sendMultiMedia", func(sendAs *tg.Channel) (tg.UpdatesClass, error) {
		base += 10
		return f.router.onMessagesSendMultiMedia(f.posterCtx(), &tg.MessagesSendMultiMediaRequest{
			Peer:       inputPeerChannel(f.group),
			MultiMedia: albumInputItems(base),
			SendAs:     inputPeerChannel(sendAs),
		})
	})
}

func TestBannedSenderChatCannotForward(t *testing.T) {
	f := newBannedSendAsFixture(t)
	source, err := f.router.onMessagesSendMessage(f.posterCtx(), &tg.MessagesSendMessageRequest{
		Peer: inputPeerChannel(f.group), Message: "source", RandomID: 138300,
	})
	if err != nil {
		t.Fatalf("send source message: %v", err)
	}
	sourceID := sentMessageIDFromUpdates(source)
	if sourceID == 0 {
		t.Fatalf("source message id missing in %#v", source)
	}
	nextID := randomIDs(138300)
	f.assertBannedThenRestored("forwardMessages", func(sendAs *tg.Channel) (tg.UpdatesClass, error) {
		return f.router.onMessagesForwardMessages(f.posterCtx(), &tg.MessagesForwardMessagesRequest{
			FromPeer: inputPeerChannel(f.group),
			ToPeer:   inputPeerChannel(f.group),
			ID:       []int{sourceID},
			RandomID: []int64{nextID()},
			SendAs:   inputPeerChannel(sendAs),
		})
	})
}

func TestBannedSenderChatCannotSendInlineBotResult(t *testing.T) {
	f := newBannedSendAsFixture(t)
	queryID, _ := f.router.inlines.register(f.clock.Now(), f.bot.ID, f.poster.ID, domain.Peer{Type: domain.PeerTypeChannel, ID: f.group.ID})
	if ok, err := f.router.onMessagesSetInlineBotResults(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSetInlineBotResultsRequest{
		QueryID: queryID,
		Results: []tg.InputBotInlineResultClass{inlineArticleResult("article", "inline")},
	}); err != nil || !ok {
		t.Fatalf("set inline results = %v,%v", ok, err)
	}
	nextID := randomIDs(138400)
	f.assertBannedThenRestored("sendInlineBotResult", func(sendAs *tg.Channel) (tg.UpdatesClass, error) {
		return f.router.onMessagesSendInlineBotResult(f.posterCtx(), &tg.MessagesSendInlineBotResultRequest{
			Peer:     inputPeerChannel(f.group),
			QueryID:  queryID,
			ID:       "article",
			RandomID: nextID(),
			SendAs:   inputPeerChannel(sendAs),
		})
	})
}

func TestBannedSenderChatCannotCreateForumTopic(t *testing.T) {
	f := newBannedSendAsFixture(t)
	if _, err := f.router.onChannelsToggleForum(WithUserID(f.ctx, f.host.ID), &tg.ChannelsToggleForumRequest{
		Channel: &tg.InputChannel{ChannelID: f.group.ID, AccessHash: f.group.AccessHash},
		Enabled: true,
	}); err != nil {
		t.Fatalf("enable forum: %v", err)
	}
	nextID := randomIDs(138500)
	f.assertBannedThenRestored("createForumTopic", func(sendAs *tg.Channel) (tg.UpdatesClass, error) {
		return f.router.onMessagesCreateForumTopic(f.posterCtx(), &tg.MessagesCreateForumTopicRequest{
			Peer:     inputPeerChannel(f.group),
			Title:    "topic",
			RandomID: nextID(),
			SendAs:   inputPeerChannel(sendAs),
		})
	})
}

// 默认身份：被封后不能再保存为默认；已保存的默认身份不再生效（channelFull 不显示、
// 不带 send_as 的发言回落为本人），解封后恢复。
func TestBannedSenderChatDefaultSendAs(t *testing.T) {
	f := newBannedSendAsFixture(t)
	save := func() error {
		_, err := f.router.onMessagesSaveDefaultSendAs(f.posterCtx(), &tg.MessagesSaveDefaultSendAsRequest{
			Peer: inputPeerChannel(f.group), SendAs: inputPeerChannel(f.banned),
		})
		return err
	}
	defaultSendAs := func() tg.PeerClass {
		t.Helper()
		full, err := f.router.onChannelsGetFullChannel(f.posterCtx(), &tg.InputChannel{ChannelID: f.group.ID, AccessHash: f.group.AccessHash})
		if err != nil {
			t.Fatalf("get full channel: %v", err)
		}
		def, _ := full.FullChat.(*tg.ChannelFull).GetDefaultSendAs()
		return def
	}
	nextID := randomIDs(138600)
	sendDefault := func() tg.PeerClass {
		t.Helper()
		sent, err := f.router.onMessagesSendMessage(f.posterCtx(), &tg.MessagesSendMessageRequest{
			Peer: inputPeerChannel(f.group), Message: "default", RandomID: nextID(),
		})
		if err != nil {
			t.Fatalf("send with default send_as: %v", err)
		}
		for _, u := range sent.(*tg.Updates).Updates {
			if v, ok := u.(*tg.UpdateNewChannelMessage); ok {
				return v.Message.(*tg.Message).FromID
			}
		}
		t.Fatalf("no new channel message in %#v", sent)
		return nil
	}
	isBanned := func(p tg.PeerClass) bool {
		ch, ok := p.(*tg.PeerChannel)
		return ok && ch.ChannelID == f.banned.ID
	}

	if err := save(); err != nil {
		t.Fatalf("save default before ban: %v", err)
	}
	f.setBanned(true)
	wantSendAsPeerInvalid(t, "save banned default", save())
	if def := defaultSendAs(); isBanned(def) {
		t.Fatalf("channelFull default_send_as = %#v while banned", def)
	}
	if from := sendDefault(); isBanned(from) {
		t.Fatalf("default send while banned from = %#v, want fallback", from)
	} else if u, ok := from.(*tg.PeerUser); !ok || u.UserID != f.poster.ID {
		t.Fatalf("default send while banned from = %#v, want poster", from)
	}

	f.setBanned(false)
	if def := defaultSendAs(); !isBanned(def) {
		t.Fatalf("channelFull default_send_as after unban = %#v, want banned channel back", def)
	}
	if from := sendDefault(); !isBanned(from) {
		t.Fatalf("default send after unban from = %#v, want banned channel", from)
	}
	if err := save(); err != nil {
		t.Fatalf("save default after unban: %v", err)
	}
}

// 定时消息：被封时不能排程；排程后才被封的，到点不发出，解封后再到点发出。
func TestBannedSenderChatScheduledMessage(t *testing.T) {
	f := newBannedSendAsFixture(t)
	schedule := func(randomID int64) error {
		req := &tg.MessagesSendMessageRequest{
			Peer: inputPeerChannel(f.group), Message: "later", RandomID: randomID, SendAs: inputPeerChannel(f.banned),
		}
		req.SetScheduleDate(int(f.clock.Now().Unix()) + 3600)
		_, err := f.router.onMessagesSendMessage(f.posterCtx(), req)
		return err
	}
	dispatch := func() {
		f.clock.advance(2 * time.Hour)
		NewScheduledDispatcher(f.router, zaptest.NewLogger(t)).DispatchOnce(context.Background())
	}
	sentAs := func(randomID int64) (*domain.Peer, bool) {
		t.Helper()
		history, err := f.router.deps.Channels.GetHistory(f.ctx, f.poster.ID, domain.ChannelHistoryFilter{ChannelID: f.group.ID, Limit: 50})
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		for _, msg := range history.Messages {
			if msg.RandomID == randomID {
				return msg.SendAs, true
			}
		}
		return nil, false
	}

	f.setBanned(true)
	wantSendAsPeerInvalid(t, "schedule as banned channel", schedule(138700))
	f.setBanned(false)

	if err := schedule(138701); err != nil {
		t.Fatalf("schedule before ban: %v", err)
	}
	f.setBanned(true)
	dispatch()
	if sendAs, sent := sentAs(138701); sent {
		t.Fatalf("scheduled message sent while banned (send_as %+v)", sendAs)
	}
	f.setBanned(false)
	dispatch()
	sendAs, sent := sentAs(138701)
	if !sent || sendAs == nil || *sendAs != (domain.Peer{Type: domain.PeerTypeChannel, ID: f.banned.ID}) {
		t.Fatalf("scheduled message after unban sent=%v send_as=%+v, want banned channel", sent, sendAs)
	}
}

// WebApp：请求时被封即拒绝；请求后才被封的，bot 发结果时拒绝，解封后可以发出。
func TestBannedSenderChatWebViewResult(t *testing.T) {
	f := newBannedSendAsFixture(t)
	request := func() (string, error) {
		req := &tg.MessagesRequestWebViewRequest{
			Peer:     inputPeerChannel(f.group),
			Bot:      &tg.InputUser{UserID: f.bot.ID, AccessHash: f.bot.AccessHash},
			Platform: "tdesktop",
		}
		req.SetSendAs(inputPeerChannel(f.banned))
		req.SetURL("https://example.test/app")
		got, err := f.router.onMessagesRequestWebView(f.posterCtx(), req)
		if err != nil {
			return "", err
		}
		return webViewInitDataFromURL(t, got.URL).Get("query_id"), nil
	}
	sendResult := func(botQueryID string) error {
		_, err := f.router.onMessagesSendWebViewResultMessage(WithUserID(f.ctx, f.bot.ID), &tg.MessagesSendWebViewResultMessageRequest{
			BotQueryID: botQueryID,
			Result:     inlineArticleResult("webview", "from webview"),
		})
		return err
	}

	f.setBanned(true)
	_, err := request()
	wantSendAsPeerInvalid(t, "request webview as banned channel", err)
	f.setBanned(false)

	botQueryID, err := request()
	if err != nil {
		t.Fatalf("request webview before ban: %v", err)
	}
	f.setBanned(true)
	wantSendAsPeerInvalid(t, "webview result while banned", sendResult(botQueryID))
	f.setBanned(false)
	if err := sendResult(botQueryID); err != nil {
		t.Fatalf("webview result after unban: %v", err)
	}
}

// channels.getSendAs 的候选不包含在该群被封禁的频道，解封后恢复（review #14）。
func TestBannedSenderChatExcludedFromGetSendAs(t *testing.T) {
	f := newBannedSendAsFixture(t)
	candidates := func() []tg.SendAsPeer {
		t.Helper()
		got, err := f.router.onChannelsGetSendAs(f.posterCtx(), &tg.ChannelsGetSendAsRequest{Peer: inputPeerChannel(f.group)})
		if err != nil {
			t.Fatalf("get send as: %v", err)
		}
		return got.Peers
	}
	f.setBanned(true)
	peers := candidates()
	if _, ok := findSendAsChannel(peers, f.banned.ID); ok {
		t.Fatalf("banned channel offered as send_as: %#v", peers)
	}
	if _, ok := findSendAsChannel(peers, f.other.ID); !ok {
		t.Fatalf("unbanned channel missing from send_as: %#v", peers)
	}
	f.setBanned(false)
	if _, ok := findSendAsChannel(candidates(), f.banned.ID); !ok {
		t.Fatal("channel not offered again after unban")
	}
}

// 广播频道（开启署名档案）里同样不能以被封禁的频道身份发言（review #1）。
func TestBannedSenderChatCannotPostInBroadcastChannel(t *testing.T) {
	f := newBannedSendAsFixture(t)
	target := createBroadcastChannelRPC(t, f.router, f.poster.ID, "Profiles Target")
	if _, err := f.router.onChannelsToggleSignatures(f.posterCtx(), &tg.ChannelsToggleSignaturesRequest{
		Channel: &tg.InputChannel{ChannelID: target.ID, AccessHash: target.AccessHash}, SignaturesEnabled: true, ProfilesEnabled: true,
	}); err != nil {
		t.Fatalf("enable signature profiles: %v", err)
	}
	if _, err := f.router.onChannelsEditBanned(f.posterCtx(), &tg.ChannelsEditBannedRequest{
		Channel:      &tg.InputChannel{ChannelID: target.ID, AccessHash: target.AccessHash},
		Participant:  inputPeerChannel(f.banned),
		BannedRights: tg.ChatBannedRights{ViewMessages: true, SendMessages: true, UntilDate: 2147483647},
	}); err != nil {
		t.Fatalf("ban sender chat in broadcast: %v", err)
	}
	send := func(sendAs *tg.Channel, randomID int64) (tg.UpdatesClass, error) {
		return f.router.onMessagesSendMessage(f.posterCtx(), &tg.MessagesSendMessageRequest{
			Peer: inputPeerChannel(target), Message: "post", RandomID: randomID, SendAs: inputPeerChannel(sendAs),
		})
	}
	_, err := send(f.banned, 138801)
	wantSendAsPeerInvalid(t, "post as banned channel in broadcast", err)
	other, err := send(f.other, 138802)
	if err != nil {
		t.Fatalf("post as unbanned channel in broadcast: %v", err)
	}
	wantSentAs(t, "post as unbanned channel in broadcast", other, f.other.ID)
}

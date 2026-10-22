package storetest

import (
	"context"
	"errors"
	"slices"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// MonoforumFixture 是新建的母频道群主与两个订阅者，以及对应的存储；每个子测试各要一份。
type MonoforumFixture struct {
	Channels store.ChannelStore
	// Dialogs 用于草稿往返（订阅者给 monoforum 存带 SuggestedPost 的草稿）。
	Dialogs           store.DialogStore
	Owner, Sub, Other int64
	// DeleteAsCreator 以母频道群主（creator 身份）删除 monoforum 里的消息并返回删除事件。
	// 公开的 DeleteChannelMessages 要求操作者是 monoforum 成员，而群主不是，所以由实现
	// 用各自的私有删除路径注入；为 nil 时跳过"删除后重放"。
	DeleteAsCreator func(t *testing.T, monoforumID, creatorID int64, ids []int, date int) domain.ChannelUpdateEvent
}

// monoforumEnv 是一个开了私信（monoforum）的广播频道，以及订阅者 Sub 的标准会话：
// Sub 发 "hi"（带转发与 SuggestedPost）、"again"，群主回复 "hi"，另一个订阅者 Other 发 "other"。
type monoforumEnv struct {
	MonoforumFixture
	broadcastID, monoID int64
	subPeer, otherPeer  domain.Peer
	forward             *domain.MessageForward
	suggestedPost       *domain.SuggestedPost
	m1, otherMessage    domain.SendChannelMessageResult
}

const monoforumBaseDate = 1_700_001_000

// channelReactionFinder 是 app/channels 按需断言的可选能力；两套实现都提供。
type channelReactionFinder interface {
	FindChannelMessageReaction(ctx context.Context, req domain.ChannelMessageReactionLookupRequest) (domain.ChannelMessageReactionLookup, bool, error)
}

func newMonoforumEnv(t *testing.T, f MonoforumFixture, seed bool) *monoforumEnv {
	t.Helper()
	ctx := context.Background()
	broadcast, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: f.Owner, Title: "Mono DM", Broadcast: true, Date: monoforumBaseDate})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	enabled, err := f.Channels.SetPaidMessagesPrice(ctx, f.Owner, broadcast.Channel.ID, 0, true)
	if err != nil {
		t.Fatalf("enable DM: %v", err)
	}
	e := &monoforumEnv{
		MonoforumFixture: f,
		broadcastID:      broadcast.Channel.ID,
		monoID:           enabled.Channel.LinkedMonoforumID,
		subPeer:          domain.Peer{Type: domain.PeerTypeUser, ID: f.Sub},
		otherPeer:        domain.Peer{Type: domain.PeerTypeUser, ID: f.Other},
		forward:          &domain.MessageForward{From: domain.Peer{Type: domain.PeerTypeUser, ID: f.Owner}, Date: monoforumBaseDate - 1},
		suggestedPost:    &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceStars, Amount: 10}, ScheduleDate: 1_700_100_000},
	}
	if e.monoID == 0 {
		t.Fatalf("no monoforum created")
	}
	if !seed {
		return e
	}
	e.m1 = e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: f.Sub, SavedPeer: e.subPeer, RandomID: 111, Message: "hi", Forward: e.forward, SuggestedPost: e.suggestedPost, Date: monoforumBaseDate + 1})
	e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: f.Sub, SavedPeer: e.subPeer, RandomID: 112, Message: "again", Date: monoforumBaseDate + 2})
	// 管理员回复：发件人是群主，saved_peer 仍是该订阅者（同一子会话）。
	e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: f.Owner, SavedPeer: e.subPeer, RandomID: 113, Message: "reply", ReplyTo: &domain.MessageReply{MessageID: e.m1.Message.ID, Peer: e.monoPeer()}, Date: monoforumBaseDate + 3})
	e.otherMessage = e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: f.Other, SavedPeer: e.otherPeer, RandomID: 201, Message: "other", Date: monoforumBaseDate + 5})
	return e
}

func (e *monoforumEnv) monoPeer() domain.Peer {
	return domain.Peer{Type: domain.PeerTypeChannel, ID: e.monoID}
}

func (e *monoforumEnv) send(t *testing.T, req domain.SendMonoforumMessageRequest) domain.SendChannelMessageResult {
	t.Helper()
	req.MonoforumID = e.monoID
	res, err := e.Channels.SendMonoforumMessage(context.Background(), req)
	if err != nil {
		t.Fatalf("send monoforum %q from %d: %v", req.Message, req.SenderUserID, err)
	}
	return res
}

func (e *monoforumEnv) pts(t *testing.T) int {
	t.Helper()
	ch, err := e.Channels.GetChannelByID(context.Background(), e.monoID)
	if err != nil {
		t.Fatalf("get monoforum: %v", err)
	}
	return ch.Pts
}

// MonoforumSendAndHistory 校验频道私信（monoforum）的发送与按订阅者读取：私信存进 monoforum 的
// saved_peer 子会话并复用频道 pts；订阅者只看得到自己的子会话，群主看得到全部；幂等按子会话去重。
// 每个子测试经 newFixture 新建自己的用户与频道，可以单独运行。
func MonoforumSendAndHistory(t *testing.T, newFixture func(t *testing.T) MonoforumFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("rich-only content and markup survive monoforum storage", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), false)
		rich := &domain.MessageRichMessage{Blocks: []byte{0x15, 0xc4, 0xb5, 0x1c, 1, 0, 0, 0, 0x8a, 0x7e, 0x56, 0x13}}
		markup := &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: "Open", Data: []byte("open")}}}}
		req := domain.SendMonoforumMessageRequest{MonoforumID: e.monoID, SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 87101, RichMessage: rich, ReplyMarkup: markup, Date: monoforumBaseDate + 10}
		sent := e.send(t, req)
		history, err := e.Channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: e.monoID, SavedPeer: e.subPeer, Limit: 10})
		if err != nil {
			t.Fatal(err)
		}
		found := false
		for _, message := range history.Messages {
			if message.ID != sent.Message.ID {
				continue
			}
			found = true
			if message.RichMessage == nil || !slices.Equal(message.RichMessage.Blocks, rich.Blocks) || message.ReplyMarkup == nil || len(message.ReplyMarkup.Inline) != 1 || string(message.ReplyMarkup.Inline[0][0].Data) != "open" {
				t.Fatalf("persisted rich/markup=%+v", message)
			}
		}
		if !found {
			t.Fatal("rich-only monoforum message missing from subscriber history")
		}
		replay := e.send(t, req)
		if !replay.Duplicate || replay.Message.ID != sent.Message.ID {
			t.Fatalf("rich replay=%+v", replay)
		}
		req.ReplyMarkup = &domain.MessageReplyMarkup{Inline: [][]domain.MarkupButton{{{Type: domain.MarkupButtonCallback, Text: "Changed", Data: []byte("changed")}}}}
		if _, err := e.Channels.SendMonoforumMessage(ctx, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
			t.Fatalf("changed markup reused receipt: %v", err)
		}
		req.ReplyMarkup = markup
		req.RichMessage = &domain.MessageRichMessage{Blocks: rich.Blocks, Rtl: true}
		if _, err := e.Channels.SendMonoforumMessage(ctx, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
			t.Fatalf("changed rich payload reused receipt: %v", err)
		}
	})

	t.Run("reply intent missing fallback cannot cross subscriber topics", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		req := domain.SendMonoforumMessageRequest{MonoforumID: e.monoID, SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 87201, Message: "missing reply fallback", ReplyTo: &domain.MessageReply{Peer: e.monoPeer(), MessageID: 999999, AllowMissing: true}, Date: monoforumBaseDate + 10}
		sent := e.send(t, req)
		if sent.Message.ReplyTo != nil {
			t.Fatalf("missing reply persisted: %+v", sent.Message.ReplyTo)
		}
		replay := e.send(t, req)
		if !replay.Duplicate || replay.Message.ID != sent.Message.ID {
			t.Fatalf("fallback replay=%+v", replay)
		}
		req.ReplyTo = domain.CloneMessageReply(req.ReplyTo)
		req.ReplyTo.AllowMissing = false
		if _, err := e.Channels.SendMonoforumMessage(ctx, req); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
			t.Fatalf("changed missing-reply policy reused receipt: %v", err)
		}
		req.RandomID++
		req.ReplyTo = &domain.MessageReply{Peer: e.monoPeer(), MessageID: e.otherMessage.Message.ID, AllowMissing: true}
		if _, err := e.Channels.SendMonoforumMessage(ctx, req); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
			t.Fatalf("cross-topic reply became missing fallback: %v", err)
		}
		req.ReplyTo = &domain.MessageReply{Peer: e.monoPeer(), MessageID: e.m1.Message.ID, QuoteText: "wrong"}
		if _, err := e.Channels.SendMonoforumMessage(ctx, req); !errors.Is(err, domain.ErrQuoteTextInvalid) {
			t.Fatalf("false quote persisted: %v", err)
		}
		req.ReplyTo.QuoteText = "hi"
		valid := e.send(t, req)
		if valid.Message.ReplyTo == nil || valid.Message.ReplyTo.QuoteText != "hi" {
			t.Fatalf("valid quote lost: %+v", valid.Message.ReplyTo)
		}
	})

	t.Run("send and recipients", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), false)
		if _, err := e.Channels.GetChannel(ctx, e.Sub, e.monoID); err != nil {
			t.Fatalf("subscriber get enabled monoforum without membership: %v", err)
		}
		if _, err := e.Channels.JoinChannel(ctx, e.monoID, e.Sub, monoforumBaseDate+1); !errors.Is(err, domain.ErrChannelMonoforumUnsupported) {
			t.Fatalf("subscriber join monoforum err = %v, want ErrChannelMonoforumUnsupported", err)
		}
		m1 := e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: e.Sub, SavedPeer: e.subPeer, RandomID: 111, Message: "hi", Date: monoforumBaseDate + 1})
		if m1.Message.SavedPeer != e.subPeer || m1.Message.ChannelID != e.monoID || m1.Message.Pts == 0 {
			t.Fatalf("m1 = %+v, want saved_peer sub + channel mono + pts>0", m1.Message)
		}
		if len(m1.Recipients) != 2 || !slices.Contains(m1.Recipients, e.Owner) || !slices.Contains(m1.Recipients, e.Sub) {
			t.Fatalf("m1 recipients = %v, want subscriber %d + parent admin %d", m1.Recipients, e.Sub, e.Owner)
		}
	})

	t.Run("main history only has the service message", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		mainHist, err := e.Channels.ListChannelHistory(ctx, e.Owner, domain.ChannelHistoryFilter{ChannelID: e.monoID, Limit: 10})
		if err != nil {
			t.Fatalf("main monoforum history: %v", err)
		}
		if mainHist.Count != 1 || len(mainHist.Messages) != 1 {
			t.Fatalf("main monoforum history count=%d len=%d, want only service message", mainHist.Count, len(mainHist.Messages))
		}
		// monoforum 的服务消息是创建消息（渲染 "Direct messages were enabled in this channel."），
		// paid_messages_price 只进母广播频道。
		if action := mainHist.Messages[0].Action; action == nil || action.Type != domain.ChannelActionCreate {
			t.Fatalf("main monoforum action = %+v, want channel_create", action)
		}
		if len(mainHist.Channels) != 1 || mainHist.Channels[0].ID != e.broadcastID {
			t.Fatalf("main monoforum extra channels = %+v, want parent %d", mainHist.Channels, e.broadcastID)
		}
	})

	t.Run("subscriber sees only own sublist", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		subscriberHist, err := e.Channels.ListChannelHistory(ctx, e.Sub, domain.ChannelHistoryFilter{ChannelID: e.monoID, Limit: 10})
		if err != nil {
			t.Fatalf("subscriber monoforum history: %v", err)
		}
		if subscriberHist.Count != 3 || len(subscriberHist.Messages) != 3 {
			t.Fatalf("subscriber monoforum history count=%d len=%d, want own 3", subscriberHist.Count, len(subscriberHist.Messages))
		}
		for _, message := range subscriberHist.Messages {
			if message.SavedPeer != e.subPeer {
				t.Fatalf("subscriber history leaked message %+v", message)
			}
		}
		exact, err := e.Channels.GetChannelMessages(ctx, e.Sub, e.monoID, []int{e.m1.Message.ID, e.otherMessage.Message.ID})
		if err != nil {
			t.Fatalf("subscriber exact monoforum messages: %v", err)
		}
		if len(exact.Messages) != 1 || exact.Messages[0].ID != e.m1.Message.ID {
			t.Fatalf("subscriber exact monoforum messages = %+v, want only own message %d", exact.Messages, e.m1.Message.ID)
		}

		// 子会话历史：倒序，读回回复、转发与 SuggestedPost。
		hist, err := e.Channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: e.monoID, SavedPeer: e.subPeer, Limit: 10})
		if err != nil {
			t.Fatalf("history: %v", err)
		}
		if hist.Count != 3 || len(hist.Messages) != 3 {
			t.Fatalf("history count=%d len=%d, want 3", hist.Count, len(hist.Messages))
		}
		for _, m := range hist.Messages {
			if m.SavedPeer != e.subPeer {
				t.Fatalf("history msg saved_peer = %+v, want sub", m.SavedPeer)
			}
		}
		if newest := hist.Messages[0]; newest.Body != "reply" || newest.ReplyTo == nil || newest.ReplyTo.MessageID != e.m1.Message.ID {
			t.Fatalf("history[0] = %q reply %+v, want newest 'reply' to message %d", newest.Body, newest.ReplyTo, e.m1.Message.ID)
		}
		oldest := hist.Messages[len(hist.Messages)-1]
		if oldest.Forward == nil || oldest.Forward.From.ID != e.Owner || oldest.Forward.Date != e.forward.Date {
			t.Fatalf("persisted monoforum forward = %+v, want source user %d/date %d", oldest.Forward, e.Owner, e.forward.Date)
		}
		if sp := oldest.SuggestedPost; sp == nil || sp.Price == nil || sp.Price.Kind != domain.SuggestedPostPriceStars || sp.Price.Amount != 10 || sp.ScheduleDate != e.suggestedPost.ScheduleDate {
			t.Fatalf("persisted suggested post = %+v, want 10 Stars + schedule", oldest.SuggestedPost)
		}
		if _, err := e.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: e.monoID, SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 114, Message: "bad reply", ReplyTo: &domain.MessageReply{MessageID: 999999, Peer: e.monoPeer()}, Date: monoforumBaseDate + 4}); !errors.Is(err, domain.ErrReplyMessageIDInvalid) {
			t.Fatalf("invalid monoforum reply err = %v, want ErrReplyMessageIDInvalid", err)
		}
	})

	t.Run("idempotent replay and conflicting intent", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		dup := e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: e.Sub, SavedPeer: e.subPeer, RandomID: 111, Message: "hi", Forward: e.forward, SuggestedPost: e.suggestedPost, Date: monoforumBaseDate + 4})
		if !dup.Duplicate || dup.Message.ID != e.m1.Message.ID {
			t.Fatalf("dup = %+v, want duplicate of m1 id %d", dup.Message, e.m1.Message.ID)
		}
		if _, err := e.Channels.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: e.monoID, SenderUserID: e.Sub, SavedPeer: e.subPeer, RandomID: 111, Message: "changed", Date: monoforumBaseDate + 4}); !errors.Is(err, domain.ErrMessageRandomIDDuplicate) {
			t.Fatalf("conflicting monoforum replay err = %v, want ErrMessageRandomIDDuplicate", err)
		}
	})

	t.Run("subscribers do not cross-talk", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		subHist, err := e.Channels.ListMonoforumHistory(ctx, domain.MonoforumHistoryFilter{MonoforumID: e.monoID, SavedPeer: e.subPeer, Limit: 10})
		if err != nil || subHist.Count != 3 {
			t.Fatalf("sub history with another subscriber = %d, %v; want own 3", subHist.Count, err)
		}
		otherHist, err := e.Channels.ListChannelHistory(ctx, e.Other, domain.ChannelHistoryFilter{ChannelID: e.monoID, Limit: 10})
		if err != nil || otherHist.Count != 1 || len(otherHist.Messages) != 1 || otherHist.Messages[0].ID != e.otherMessage.Message.ID {
			t.Fatalf("other subscriber history = %+v, %v; want only own message", otherHist.Messages, err)
		}
		// 订阅者子会话列表：按 top 消息 id 倒序（other 最后发，排首）。
		dialogs, err := e.Channels.ListMonoforumDialogs(ctx, domain.MonoforumDialogsFilter{MonoforumID: e.monoID, Limit: 10})
		if err != nil {
			t.Fatalf("list dialogs: %v", err)
		}
		if dialogs.Count != 2 || len(dialogs.Dialogs) != 2 {
			t.Fatalf("dialogs count=%d len=%d, want 2 subscribers", dialogs.Count, len(dialogs.Dialogs))
		}
		if dialogs.Dialogs[0].SavedPeer != e.otherPeer {
			t.Fatalf("dialogs[0] saved_peer = %+v, want newest 'other'", dialogs.Dialogs[0].SavedPeer)
		}
		if dialogs.Dialogs[1].SavedPeer != e.subPeer || dialogs.Dialogs[1].TopMessageID == 0 {
			t.Fatalf("dialogs[1] = %+v, want sub with top message", dialogs.Dialogs[1])
		}
	})

	t.Run("views", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		m1, other := e.m1.Message.ID, e.otherMessage.Message.ID
		ptsBefore := e.pts(t)
		views := func(t *testing.T, user int64, date int, ids ...int) map[int]int {
			t.Helper()
			res, err := e.Channels.GetChannelMessageViews(ctx, domain.ChannelMessageViewsRequest{UserID: user, ChannelID: e.monoID, IDs: ids, Increment: true, Date: date})
			if err != nil {
				t.Fatalf("views by %d: %v", user, err)
			}
			return res.Views
		}
		if got := views(t, e.Sub, monoforumBaseDate+6, m1, other); len(got) != 1 || got[m1] != 1 {
			t.Fatalf("subscriber monoforum views = %+v, want only own message %d at 1", got, m1)
		}
		if got := views(t, e.Sub, monoforumBaseDate+7, m1); got[m1] != 1 {
			t.Fatalf("repeated subscriber monoforum views = %+v, want idempotent 1", got)
		}
		// 订阅者看不到的消息不计数：群主第一次看时 other 才到 1。
		if got := views(t, e.Owner, monoforumBaseDate+8, m1, other); len(got) != 2 || got[m1] != 2 || got[other] != 1 {
			t.Fatalf("admin monoforum views = %+v, want both saved peers at 2/1", got)
		}
		if after := e.pts(t); after != ptsBefore {
			t.Fatalf("message views advanced monoforum pts = %d, want unchanged %d", after, ptsBefore)
		}
	})

	t.Run("reactions", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		m1, other := e.m1.Message.ID, e.otherMessage.Message.ID
		react := func(user int64, id int, emoticon string) error {
			_, err := e.Channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{
				UserID: user, ChannelID: e.monoID, MessageID: id,
				Reactions: []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: emoticon}},
				Date:      monoforumBaseDate + 6,
			})
			return err
		}
		if err := react(e.Sub, m1, "\U0001f44d"); err != nil {
			t.Fatalf("subscriber react to own monoforum message: %v", err)
		}
		if err := react(e.Sub, other, "\U0001f525"); !errors.Is(err, domain.ErrMessageIDInvalid) {
			t.Fatalf("subscriber react to another saved_peer err = %v, want ErrMessageIDInvalid", err)
		}
		subReactions, err := e.Channels.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{UserID: e.Sub, ChannelID: e.monoID, IDs: []int{m1, other}})
		if err != nil || len(subReactions.Messages) != 1 || subReactions.Messages[0].ID != m1 {
			t.Fatalf("subscriber monoforum reactions = %+v, %v; want only own message %d", subReactions.Messages, err, m1)
		}
		adminReactions, err := e.Channels.GetChannelMessageReactions(ctx, domain.ChannelMessageReactionsRequest{UserID: e.Owner, ChannelID: e.monoID, IDs: []int{m1, other}})
		if err != nil || len(adminReactions.Messages) != 2 {
			t.Fatalf("admin monoforum reactions = %+v, %v; want both subscriber messages", adminReactions.Messages, err)
		}
		reactionList, err := e.Channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{UserID: e.Sub, ChannelID: e.monoID, MessageID: m1, Limit: 10})
		if err != nil || reactionList.Count != 1 || len(reactionList.Reactions) != 1 {
			t.Fatalf("subscriber monoforum reaction list = %+v, %v; want one", reactionList, err)
		}
		if _, err := e.Channels.ListChannelMessageReactions(ctx, domain.ChannelMessageReactionsListRequest{UserID: e.Sub, ChannelID: e.monoID, MessageID: other, Limit: 10}); !errors.Is(err, domain.ErrMessageIDInvalid) {
			t.Fatalf("subscriber list another saved_peer reactions err = %v, want ErrMessageIDInvalid", err)
		}
		finder, ok := e.Channels.(channelReactionFinder)
		if !ok {
			t.Fatalf("%T does not implement FindChannelMessageReaction", e.Channels)
		}
		lookup, found, err := finder.FindChannelMessageReaction(ctx, domain.ChannelMessageReactionLookupRequest{ViewerUserID: e.Sub, ChannelID: e.monoID, MessageID: m1, ReactorUserID: e.Sub})
		if err != nil || !found || len(lookup.Reactions) != 1 {
			t.Fatalf("subscriber monoforum reaction lookup = %+v, %v, %v; want one", lookup, found, err)
		}
		if _, _, err := finder.FindChannelMessageReaction(ctx, domain.ChannelMessageReactionLookupRequest{ViewerUserID: e.Sub, ChannelID: e.monoID, MessageID: other, ReactorUserID: e.Other}); !errors.Is(err, domain.ErrMessageIDInvalid) {
			t.Fatalf("subscriber lookup another saved_peer reaction err = %v, want ErrMessageIDInvalid", err)
		}
	})

	t.Run("difference", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		diff, err := e.Channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: e.Sub, ChannelID: e.monoID, Pts: 0, Limit: 100})
		if err != nil {
			t.Fatalf("subscriber channel difference: %v", err)
		}
		if pts := e.pts(t); diff.Pts != pts {
			t.Fatalf("subscriber difference pts = %d, want channel pts %d despite filtered events", diff.Pts, pts)
		}
		if len(diff.NewMessages) != 3 {
			t.Fatalf("subscriber difference messages = %d, want own 3", len(diff.NewMessages))
		}
		for _, message := range diff.NewMessages {
			if message.SavedPeer != e.subPeer {
				t.Fatalf("subscriber difference leaked message %+v", message)
			}
		}
		active, err := e.Channels.ListActiveChannelIDsForUser(ctx, e.Sub, 0, 10)
		if err != nil || !slices.Contains(active, e.monoID) {
			t.Fatalf("subscriber active channels = %v, %v; want monoforum %d for offline recovery", active, err, e.monoID)
		}
	})

	// 去重按订阅者子会话维度：同一发件人（群主）用相同 random_id 向两个不同订阅者发，不得互相去重；
	// 同一子会话真重发仍去重。
	t.Run("dedup is per sublist", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), false)
		a := e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 9001, Message: "to sub", Date: monoforumBaseDate + 10})
		b := e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: e.Owner, SavedPeer: e.otherPeer, RandomID: 9001, Message: "to other", Date: monoforumBaseDate + 11})
		if b.Duplicate || b.Message.ID == a.Message.ID {
			t.Fatalf("cross-sublist same random_id wrongly deduped: a=%d b=%d dup=%v", a.Message.ID, b.Message.ID, b.Duplicate)
		}
		again := e.send(t, domain.SendMonoforumMessageRequest{SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 9001, Message: "to sub", Date: monoforumBaseDate + 12})
		if !again.Duplicate || again.Message.ID != a.Message.ID {
			t.Fatalf("same-sublist retry not deduped: again=%+v want dup of %d", again.Message, a.Message.ID)
		}
	})

	// 删除后重放：返回首次快照与持久化的删除事件，不再推进 pts、不再写事件。
	t.Run("replay after delete", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), true)
		if e.DeleteAsCreator == nil {
			t.Skip("store has no creator delete hook")
		}
		req := domain.SendMonoforumMessageRequest{SenderUserID: e.Owner, SavedPeer: e.subPeer, RandomID: 9001, Message: "to sub", Date: monoforumBaseDate + 10}
		a := e.send(t, req)
		deleteEvent := e.DeleteAsCreator(t, e.monoID, e.Owner, []int{a.Message.ID}, monoforumBaseDate+13)
		deleteDiff, err := e.Channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: e.Sub, ChannelID: e.monoID, Pts: deleteEvent.Pts - deleteEvent.PtsCount, Limit: 10})
		if err != nil {
			t.Fatalf("subscriber difference after own delete: %v", err)
		}
		if deleteDiff.Pts != deleteEvent.Pts || len(deleteDiff.OtherUpdates) != 1 || len(deleteDiff.OtherUpdates[0].MessageIDs) != 1 || deleteDiff.OtherUpdates[0].MessageIDs[0] != a.Message.ID {
			t.Fatalf("subscriber delete difference = %+v, want own deleted id %d at pts %d", deleteDiff, a.Message.ID, deleteEvent.Pts)
		}
		req.Date = monoforumBaseDate + 14
		replay := e.send(t, req)
		if !replay.Duplicate || replay.Message.ID != a.Message.ID || replay.Message.Body != "to sub" || replay.ReplayDeleteEvent == nil || replay.ReplayDeleteEvent.Pts != deleteEvent.Pts {
			t.Fatalf("deleted monoforum replay = %+v, want first snapshot + durable delete %+v", replay, deleteEvent)
		}
		if pts := e.pts(t); pts != deleteEvent.Pts {
			t.Fatalf("deleted monoforum replay advanced pts to %d, want %d", pts, deleteEvent.Pts)
		}
		after, err := e.Channels.ListChannelDifference(ctx, domain.ChannelDifferenceRequest{UserID: e.Owner, ChannelID: e.monoID, Pts: deleteEvent.Pts, Limit: 10})
		if err != nil || after.Pts != deleteEvent.Pts || len(after.NewMessages) != 0 || len(after.OtherUpdates) != 0 {
			t.Fatalf("difference after replay = %+v, %v; want no new events", after, err)
		}
	})

	t.Run("draft keeps a suggested post", func(t *testing.T) {
		e := newMonoforumEnv(t, newFixture(t), false)
		draft := domain.DialogDraft{Peer: e.monoPeer(), Message: "pending suggested post", Date: monoforumBaseDate + 1, SuggestedPost: e.suggestedPost}
		if err := e.Dialogs.SaveDraft(ctx, e.Sub, draft); err != nil {
			t.Fatalf("save subscriber monoforum draft: %v", err)
		}
		loaded, found, err := e.Dialogs.GetDraft(ctx, e.Sub, draft.Peer, 0)
		if err != nil || !found || loaded.SuggestedPost == nil || loaded.SuggestedPost.Price == nil || loaded.SuggestedPost.Price.Amount != 10 || loaded.SuggestedPost.ScheduleDate != e.suggestedPost.ScheduleDate {
			t.Fatalf("loaded subscriber monoforum draft = %+v, %v, %v; want suggested post", loaded, found, err)
		}
	})
}

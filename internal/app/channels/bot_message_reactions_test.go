package channels

import (
	"context"
	"reflect"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type botReactionFixture struct {
	t       *testing.T
	ctx     context.Context
	users   *memory.UserStore
	updates *memory.BotUpdateStore
	svc     *Service
	now     int
}

func newBotReactionFixture(t *testing.T) *botReactionFixture {
	t.Helper()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	st := memory.NewChannelStore()
	st.AttachBotUpdateStore(updates)
	return &botReactionFixture{t: t, ctx: context.Background(), users: users, updates: updates, svc: NewService(st), now: int(time.Now().Unix())}
}

func (f *botReactionFixture) user(bot bool) int64 {
	f.t.Helper()
	u, err := f.users.Create(f.ctx, domain.User{AccessHash: 5, FirstName: "u", Bot: bot})
	if err != nil {
		f.t.Fatal(err)
	}
	return u.ID
}

func (f *botReactionFixture) must(step string, err error) {
	f.t.Helper()
	if err != nil {
		f.t.Fatalf("%s: %v", step, err)
	}
}

func (f *botReactionFixture) promote(owner, channelID, memberID int64) {
	f.t.Helper()
	_, err := f.svc.EditAdmin(f.ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: memberID, AdminRights: domain.ChannelAdminRights{DeleteMessages: true, PostMessages: true}, Date: f.now})
	f.must("promote", err)
}

func (f *botReactionFixture) react(userID, channelID int64, msgID int, emoticons ...string) {
	f.t.Helper()
	reactions := make([]domain.MessageReaction, 0, len(emoticons))
	for _, e := range emoticons {
		reactions = append(reactions, domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: e})
	}
	_, err := f.svc.SetMessageReactions(f.ctx, userID, domain.SetChannelMessageReactionsRequest{ChannelID: channelID, MessageID: msgID, Reactions: reactions, ReactionsPerUserMax: 3, Date: f.now})
	f.must("react", err)
}

// since 返回 bot 在 qts 之后收到的反应类记录（成员变动记录不计）。
func (f *botReactionFixture) reactionUpdatesSince(bot int64, qts int) []domain.BotUpdate {
	f.t.Helper()
	rows, err := f.updates.ListBotUpdates(f.ctx, bot, qts, 100)
	f.must("list bot updates", err)
	out := make([]domain.BotUpdate, 0, len(rows))
	for _, row := range rows {
		if row.MessageReaction != nil || row.MessageReactions != nil {
			out = append(out, row)
		}
	}
	return out
}

func (f *botReactionFixture) qts(bot int64) int {
	f.t.Helper()
	st, err := f.updates.BotUpdatesState(f.ctx, bot)
	f.must("state", err)
	return st.Qts
}

func emoji(e ...string) []domain.MessageReaction {
	out := make([]domain.MessageReaction, 0, len(e))
	for _, v := range e {
		out = append(out, domain.MessageReaction{Type: domain.MessageReactionEmoji, Emoticon: v})
	}
	return out
}

// 超级群里用户改反应（#150）：作为管理员的 bot 收到带旧反应与新反应的 updateBotMessageReaction；
// 只是普通成员的 bot（privacy 模式下看不到这条消息）、bot 自己设的反应、没有变化的重复设置都不产生记录。
func TestSupergroupReactionChangesReachAdminBots(t *testing.T) {
	f := newBotReactionFixture(t)
	owner, human, adminBot, memberBot, otherBot := f.user(false), f.user(false), f.user(true), f.user(true), f.user(true)
	created, err := f.svc.CreateChannel(f.ctx, owner, domain.CreateChannelRequest{Title: "votes", Megagroup: true, Date: f.now})
	f.must("create", err)
	channelID := created.Channel.ID
	_, err = f.svc.InviteToChannel(f.ctx, owner, channelID, []int64{human, adminBot, memberBot, otherBot}, f.now)
	f.must("invite", err)
	f.promote(owner, channelID, adminBot)
	sent, err := f.svc.SendMessage(f.ctx, owner, domain.SendChannelMessageRequest{ChannelID: channelID, RandomID: 1, Message: "poll: which?", Date: f.now})
	f.must("send", err)
	msgID := sent.Message.ID
	adminFrom, memberFrom := f.qts(adminBot), f.qts(memberBot)

	f.react(human, channelID, msgID, "👍")
	f.react(human, channelID, msgID, "👍") // 没有变化
	f.react(human, channelID, msgID, "❤", "👍")
	f.react(human, channelID, msgID)
	f.react(otherBot, channelID, msgID, "🔥") // bot 设的反应不送达

	got := f.reactionUpdatesSince(adminBot, adminFrom)
	want := []struct{ old, new []domain.MessageReaction }{
		{nil, emoji("👍")},
		{emoji("👍"), emoji("❤", "👍")},
		{emoji("❤", "👍"), nil},
	}
	if len(got) != len(want) {
		t.Fatalf("admin bot reaction updates = %+v, want %d", got, len(want))
	}
	for i, w := range want {
		r := got[i].MessageReaction
		if r == nil {
			t.Fatalf("update %d = %+v, want MessageReaction", i, got[i])
		}
		if r.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}) || r.MessageID != msgID ||
			r.Actor != (domain.Peer{Type: domain.PeerTypeUser, ID: human}) || got[i].Date != f.now {
			t.Fatalf("update %d header = %+v date %d", i, r, got[i].Date)
		}
		if !reactionsEqualForTest(r.Old, w.old) || !reactionsEqualForTest(r.New, w.new) {
			t.Fatalf("update %d old/new = %v -> %v, want %v -> %v", i, r.Old, r.New, w.old, w.new)
		}
		if i > 0 && got[i].Qts != got[i-1].Qts+1 {
			t.Fatalf("qts not contiguous: %d then %d", got[i-1].Qts, got[i].Qts)
		}
	}
	if rows := f.reactionUpdatesSince(memberBot, memberFrom); len(rows) != 0 {
		t.Fatalf("non-admin bot got reaction updates %+v", rows)
	}
}

// 广播频道里的反应是匿名的（#150）：管理员 bot 收到带反应计数的 updateBotMessageReactions，
// 不暴露是谁反应的。
func TestChannelAnonymousReactionsReachAdminBotsAsCounts(t *testing.T) {
	f := newBotReactionFixture(t)
	owner, sub1, sub2, adminBot := f.user(false), f.user(false), f.user(false), f.user(true)
	created, err := f.svc.CreateChannel(f.ctx, owner, domain.CreateChannelRequest{Title: "news", Broadcast: true, Date: f.now})
	f.must("create", err)
	channelID := created.Channel.ID
	_, err = f.svc.InviteToChannel(f.ctx, owner, channelID, []int64{sub1, sub2, adminBot}, f.now)
	f.must("invite", err)
	f.promote(owner, channelID, adminBot)
	sent, err := f.svc.SendMessage(f.ctx, owner, domain.SendChannelMessageRequest{ChannelID: channelID, RandomID: 1, Message: "post", Date: f.now})
	f.must("send", err)
	msgID := sent.Message.ID
	from := f.qts(adminBot)

	f.react(sub1, channelID, msgID, "👍")
	f.react(sub2, channelID, msgID, "👍")
	f.react(sub2, channelID, msgID)

	got := f.reactionUpdatesSince(adminBot, from)
	wantCounts := []int{1, 2, 1}
	if len(got) != len(wantCounts) {
		t.Fatalf("admin bot updates = %+v, want %d count updates", got, len(wantCounts))
	}
	for i, n := range wantCounts {
		r := got[i].MessageReactions
		if r == nil || got[i].MessageReaction != nil {
			t.Fatalf("update %d = %+v, want only MessageReactions", i, got[i])
		}
		if r.Peer != (domain.Peer{Type: domain.PeerTypeChannel, ID: channelID}) || r.MessageID != msgID {
			t.Fatalf("update %d header = %+v", i, r)
		}
		if len(r.Reactions) != 1 || r.Reactions[0].Count != n || !reflect.DeepEqual(r.Reactions[0].Reaction, emoji("👍")[0]) {
			t.Fatalf("update %d counts = %+v, want 👍 x%d", i, r.Reactions, n)
		}
	}
}

func reactionsEqualForTest(a, b []domain.MessageReaction) bool {
	if len(a) == 0 && len(b) == 0 {
		return true
	}
	return reflect.DeepEqual(a, b)
}

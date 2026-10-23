package storetest

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// BotUpdatesChannelStore 是契约用到的频道写入口：成员变动在写入成员记录的同一事务里
// 写 bot qts 记录。
type BotUpdatesChannelStore interface {
	CreateChannel(ctx context.Context, req domain.CreateChannelRequest) (domain.CreateChannelResult, error)
	InviteToChannel(ctx context.Context, channelID, inviterUserID int64, userIDs []int64, date int) (domain.CreateChannelResult, error)
	LeaveChannel(ctx context.Context, channelID, userID int64, date int) (domain.CreateChannelResult, error)
	EditChannelAdmin(ctx context.Context, req domain.EditChannelAdminRequest) (domain.EditChannelAdminResult, error)
	EditChannelBanned(ctx context.Context, req domain.EditChannelBannedRequest) (domain.EditChannelBannedResult, error)
	EditChannelMemberRank(ctx context.Context, req domain.EditChannelMemberRankRequest) (domain.EditChannelAdminResult, error)
}

// BotUpdatesFixture 是同一份底层数据上的 bot qts 序列 store 与频道 store。NewUser 每次
// 新建一个账号，bot=true 时是 bot 账号。
type BotUpdatesFixture struct {
	Updates  store.BotUpdateStore
	Channels BotUpdatesChannelStore
	NewUser  func(t *testing.T, bot bool) int64
}

// BotUpdatesContract 是 bot qts 序列的存储契约（#145）：每个 bot 的初始 qts 是 1（TDLib
// 拒收 qts <= 1 的在线推送），第一条记录分配 qts 2，之后连续分配、
// 互相独立；按 qts 翻页读取；保留期回收后已分配的 qts 不回退、最小保留 qts 前移；
// bot 自己的成员变动与 qts 记录由频道 store 一起写入，普通用户的变动不写。
func BotUpdatesContract(t *testing.T, newFixture func(t *testing.T) BotUpdatesFixture) {
	t.Helper()
	ctx := context.Background()
	now := int(time.Now().Unix())
	record := func(botID int64, date, channelID int64) domain.BotUpdate {
		return domain.BotUpdate{BotUserID: botID, Date: int(date), ChannelParticipant: &domain.BotChannelParticipantUpdate{
			Channel:     domain.Channel{ID: channelID, Title: "qts", Megagroup: true},
			ActorUserID: 7,
			Current:     domain.ChannelMember{ChannelID: channelID, UserID: botID, Role: domain.ChannelRoleMember, Status: domain.ChannelMemberActive},
		}}
	}
	list := func(t *testing.T, f BotUpdatesFixture, botID int64, after, limit int) []domain.BotUpdate {
		t.Helper()
		rows, err := f.Updates.ListBotUpdates(ctx, botID, after, limit)
		if err != nil {
			t.Fatalf("list bot updates: %v", err)
		}
		return rows
	}
	state := func(t *testing.T, f BotUpdatesFixture, botID int64) domain.BotUpdatesState {
		t.Helper()
		st, err := f.Updates.BotUpdatesState(ctx, botID)
		if err != nil {
			t.Fatalf("bot updates state: %v", err)
		}
		return st
	}
	qtsOf := func(rows []domain.BotUpdate) []int {
		out := make([]int, 0, len(rows))
		for _, row := range rows {
			out = append(out, row.Qts)
		}
		return out
	}
	equalInts := func(a, b []int) bool {
		if len(a) != len(b) {
			return false
		}
		for i := range a {
			if a[i] != b[i] {
				return false
			}
		}
		return true
	}

	t.Run("qts is contiguous and independent per bot", func(t *testing.T) {
		f := newFixture(t)
		a, b := f.NewUser(t, true), f.NewUser(t, true)
		if st := state(t, f, a); st != (domain.BotUpdatesState{Qts: 1}) {
			t.Fatalf("fresh bot state = %+v, want qts 1 and nothing retained", st)
		}
		got, err := f.Updates.AppendBotUpdates(ctx, []domain.BotUpdate{
			record(a, int64(now), 501), record(b, int64(now), 502), record(a, int64(now), 503),
		})
		if err != nil {
			t.Fatalf("append: %v", err)
		}
		if len(got) != 3 || got[0].Qts != 2 || got[1].Qts != 2 || got[2].Qts != 3 {
			t.Fatalf("assigned qts = %v, want [2 2 3]", qtsOf(got))
		}
		if _, err := f.Updates.AppendBotUpdates(ctx, []domain.BotUpdate{record(a, int64(now), 504)}); err != nil {
			t.Fatalf("append again: %v", err)
		}
		rows := list(t, f, a, 0, 10)
		if !equalInts(qtsOf(rows), []int{2, 3, 4}) {
			t.Fatalf("bot a qts = %v, want [2 3 4]", qtsOf(rows))
		}
		if p := rows[1].ChannelParticipant; p == nil || p.Channel.ID != 503 || p.ActorUserID != 7 ||
			p.Current.UserID != a || p.Current.Status != domain.ChannelMemberActive || rows[1].BotUserID != a || rows[1].Date != now {
			t.Fatalf("round trip = %+v (payload %+v)", rows[1], rows[1].ChannelParticipant)
		}
		if bRows := list(t, f, b, 0, 10); !equalInts(qtsOf(bRows), []int{2}) {
			t.Fatalf("bot b qts = %v, want [2]", qtsOf(bRows))
		}
		if st := state(t, f, a); st != (domain.BotUpdatesState{Qts: 4, MinRetainedQts: 2}) {
			t.Fatalf("bot a state = %+v, want qts 4 min 2", st)
		}
	})

	t.Run("lists after a qts with a limit", func(t *testing.T) {
		f := newFixture(t)
		bot := f.NewUser(t, true)
		for i := 0; i < 5; i++ {
			if _, err := f.Updates.AppendBotUpdates(ctx, []domain.BotUpdate{record(bot, int64(now), int64(600+i))}); err != nil {
				t.Fatalf("append %d: %v", i, err)
			}
		}
		if got := qtsOf(list(t, f, bot, 3, 2)); !equalInts(got, []int{4, 5}) {
			t.Fatalf("after 3 limit 2 = %v, want [4 5]", got)
		}
		if got := list(t, f, bot, 6, 10); len(got) != 0 {
			t.Fatalf("after last = %v, want empty", qtsOf(got))
		}
	})

	t.Run("retention removes old records without rewinding qts", func(t *testing.T) {
		f := newFixture(t)
		bot := f.NewUser(t, true)
		old := now - 2*24*3600
		if _, err := f.Updates.AppendBotUpdates(ctx, []domain.BotUpdate{
			record(bot, int64(old), 701), record(bot, int64(old), 702), record(bot, int64(now), 703),
		}); err != nil {
			t.Fatalf("append: %v", err)
		}
		deleted, err := f.Updates.DeleteExpiredBotUpdates(ctx, time.Unix(int64(now-24*3600), 0), 100)
		if err != nil || deleted < 2 {
			t.Fatalf("delete expired = %d, %v; want at least the 2 old records", deleted, err)
		}
		st := state(t, f, bot)
		if st != (domain.BotUpdatesState{Qts: 4, MinRetainedQts: 4}) {
			t.Fatalf("state after retention = %+v, want qts 4 min 4", st)
		}
		if got := qtsOf(list(t, f, bot, 0, 10)); !equalInts(got, []int{4}) {
			t.Fatalf("retained = %v, want [4]", got)
		}
		if _, err := f.Updates.DeleteExpiredBotUpdates(ctx, time.Unix(int64(now+1), 0), 100); err != nil {
			t.Fatalf("delete all: %v", err)
		}
		st = state(t, f, bot)
		if st != (domain.BotUpdatesState{Qts: 4}) {
			t.Fatalf("state with nothing retained = %+v, want qts 4 and nothing retained", st)
		}
		next, err := f.Updates.AppendBotUpdates(ctx, []domain.BotUpdate{record(bot, int64(now), 704)})
		if err != nil || len(next) != 1 || next[0].Qts != 5 {
			t.Fatalf("append after retention = %v, %v; want qts 5", qtsOf(next), err)
		}
	})

	t.Run("bot membership changes write qts records with the change", func(t *testing.T) {
		f := newFixture(t)
		owner, bot, human := f.NewUser(t, false), f.NewUser(t, true), f.NewUser(t, false)
		created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "bot qts", Megagroup: true, Date: now})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		channelID := created.Channel.ID
		invited, err := f.Channels.InviteToChannel(ctx, channelID, owner, []int64{bot, human}, now)
		if err != nil {
			t.Fatalf("invite: %v", err)
		}
		if len(invited.BotUpdates) != 1 || invited.BotUpdates[0].BotUserID != bot || invited.BotUpdates[0].Qts != 2 {
			t.Fatalf("invite result bot updates = %+v, want one for the bot at qts 2", invited.BotUpdates)
		}
		promoted, err := f.Channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: bot, AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: now})
		if err != nil {
			t.Fatalf("promote: %v", err)
		}
		if len(promoted.BotUpdates) != 1 || promoted.BotUpdates[0].Qts != 3 {
			t.Fatalf("promote result bot updates = %+v, want qts 3", promoted.BotUpdates)
		}
		// 成员 tag（rank）是参与者看得见的权限状态：改 tag 同样写 qts 记录。
		tagged, err := f.Channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{UserID: owner, ChannelID: channelID, MemberID: bot, Rank: "helper", Date: now})
		if err != nil {
			t.Fatalf("edit rank: %v", err)
		}
		if len(tagged.BotUpdates) != 1 || tagged.BotUpdates[0].Qts != 4 || tagged.BotUpdates[0].ChannelParticipant.Current.Rank != "helper" {
			t.Fatalf("edit rank result bot updates = %+v, want qts 4 carrying the tag", tagged.BotUpdates)
		}
		kicked, err := f.Channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: channelID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: bot}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: now})
		if err != nil {
			t.Fatalf("kick: %v", err)
		}
		if len(kicked.BotUpdates) != 1 || kicked.BotUpdates[0].Qts != 5 {
			t.Fatalf("kick result bot updates = %+v, want qts 5", kicked.BotUpdates)
		}
		if _, err := f.Channels.LeaveChannel(ctx, channelID, human, now); err != nil {
			t.Fatalf("human leave: %v", err)
		}
		rows := list(t, f, bot, 0, 10)
		if !equalInts(qtsOf(rows), []int{2, 3, 4, 5}) {
			t.Fatalf("bot qts = %v, want [2 3 4 5]", qtsOf(rows))
		}
		last := rows[3].ChannelParticipant
		if last == nil || last.Channel.ID != channelID || last.ActorUserID != owner ||
			last.Previous.Role != domain.ChannelRoleAdmin || last.Current.Status != domain.ChannelMemberKicked {
			t.Fatalf("kick record = %+v, want admin -> kicked by owner", last)
		}
		if got := list(t, f, human, 0, 10); len(got) != 0 {
			t.Fatalf("human qts records = %v, want none", qtsOf(got))
		}
	})
}

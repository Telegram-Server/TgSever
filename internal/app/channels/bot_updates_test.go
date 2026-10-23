package channels

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// bot 自己在超级群/频道里的成员状态变化生成哪些 qts 记录（#145）：加入、移出、设为或
// 撤销管理员、限制、封禁、自己退出各一条，按发生顺序取连续 qts，带变化前后的参与者；
// 普通用户的同样变化不生成。
func TestBotSelfMembershipChangesGenerateQtsUpdates(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	store := memory.NewChannelStore()
	store.AttachBotUpdateStore(updates)
	svc := NewService(store)
	newUser := func(bot bool) int64 {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: 5, FirstName: "u", Bot: bot})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	owner, bot, human := newUser(false), newUser(true), newUser(false)
	now := int(time.Now().Unix())
	for _, broadcast := range []bool{false, true} {
		created, err := svc.CreateChannel(ctx, owner, domain.CreateChannelRequest{Title: "matrix", Megagroup: !broadcast, Broadcast: broadcast, Date: now})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		channelID := created.Channel.ID
		user := func(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeUser, ID: id} }
		mustOK := func(step string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("broadcast=%v %s: %v", broadcast, step, err)
			}
		}
		before, err := updates.BotUpdatesState(ctx, bot)
		mustOK("state", err)

		_, err = svc.InviteToChannel(ctx, owner, channelID, []int64{bot, human}, now)
		mustOK("invite", err)
		_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: bot, AdminRights: domain.ChannelAdminRights{DeleteMessages: true, PostMessages: broadcast}, Date: now})
		mustOK("promote", err)
		_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: bot, Date: now})
		mustOK("demote", err)
		_, err = svc.EditBanned(ctx, owner, domain.EditChannelBannedRequest{ChannelID: channelID, Participant: user(bot), BannedRights: domain.ChannelBannedRights{SendMessages: true}, Date: now})
		mustOK("restrict", err)
		_, err = svc.EditBanned(ctx, owner, domain.EditChannelBannedRequest{ChannelID: channelID, Participant: user(bot), BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: now})
		mustOK("ban", err)
		_, err = svc.EditBanned(ctx, owner, domain.EditChannelBannedRequest{ChannelID: channelID, Participant: user(bot), Date: now})
		mustOK("unban", err)
		_, err = svc.InviteToChannel(ctx, owner, channelID, []int64{bot}, now)
		mustOK("re-invite", err)
		_, err = svc.LeaveChannel(ctx, bot, channelID, now)
		mustOK("bot leaves", err)
		// 普通用户的变化不进任何 bot 的序列。
		_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: human, AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: now})
		mustOK("promote human", err)
		_, err = svc.LeaveChannel(ctx, human, channelID, now)
		mustOK("human leaves", err)

		rows, err := updates.ListBotUpdates(ctx, bot, before.Qts, 100)
		mustOK("list", err)
		type step struct {
			name           string
			actor          int64
			hadPrevious    bool
			prevStatus     domain.ChannelMemberStatus
			prevRole       domain.ChannelMemberRole
			status         domain.ChannelMemberStatus
			role           domain.ChannelMemberRole
			restrictedSend bool
		}
		want := []step{
			{"invited", owner, false, "", "", domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"promoted", owner, true, domain.ChannelMemberActive, domain.ChannelRoleMember, domain.ChannelMemberActive, domain.ChannelRoleAdmin, false},
			{"demoted", owner, true, domain.ChannelMemberActive, domain.ChannelRoleAdmin, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"restricted", owner, true, domain.ChannelMemberActive, domain.ChannelRoleMember, domain.ChannelMemberActive, domain.ChannelRoleMember, true},
			{"banned", owner, true, domain.ChannelMemberActive, domain.ChannelRoleMember, domain.ChannelMemberKicked, domain.ChannelRoleMember, false},
			{"unbanned", owner, true, domain.ChannelMemberKicked, domain.ChannelRoleMember, domain.ChannelMemberLeft, domain.ChannelRoleMember, false},
			{"re-invited", owner, true, domain.ChannelMemberLeft, domain.ChannelRoleMember, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"left", bot, true, domain.ChannelMemberActive, domain.ChannelRoleMember, domain.ChannelMemberLeft, domain.ChannelRoleMember, false},
		}
		if len(rows) != len(want) {
			t.Fatalf("broadcast=%v: %d qts records, want %d: %+v", broadcast, len(rows), len(want), rows)
		}
		for i, w := range want {
			row := rows[i]
			p := row.ChannelParticipant
			if row.BotUserID != bot || row.Qts != before.Qts+i+1 || p == nil || p.Channel.ID != channelID || p.ActorUserID != w.actor {
				t.Fatalf("broadcast=%v %s: record %+v (payload %+v)", broadcast, w.name, row, p)
			}
			if (p.Previous.UserID != 0) != w.hadPrevious || (w.hadPrevious && (p.Previous.Status != w.prevStatus || p.Previous.Role != w.prevRole)) {
				t.Fatalf("broadcast=%v %s: previous %+v, want had=%v %s/%s", broadcast, w.name, p.Previous, w.hadPrevious, w.prevStatus, w.prevRole)
			}
			if p.Current.UserID != bot || p.Current.Status != w.status || p.Current.Role != w.role || p.Current.BannedRights.SendMessages != w.restrictedSend {
				t.Fatalf("broadcast=%v %s: current %+v, want %s/%s send-restricted=%v", broadcast, w.name, p.Current, w.status, w.role, w.restrictedSend)
			}
		}
		if st, _ := updates.BotUpdatesState(ctx, human); st != (domain.BotUpdatesState{Qts: domain.BotInitialQts}) {
			t.Fatalf("human has a qts sequence: %+v", st)
		}
	}
}

// 把从不在群的 bot 直接设为管理员：对 bot 而言是"加入并成为管理员"，变化前没有参与者。
func TestPromotingAnOutsideBotRecordsAJoin(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	store := memory.NewChannelStore()
	store.AttachBotUpdateStore(updates)
	svc := NewService(store)
	owner, _ := users.Create(ctx, domain.User{AccessHash: 5, FirstName: "owner"})
	bot, _ := users.Create(ctx, domain.User{AccessHash: 6, FirstName: "bot", Bot: true})
	created, err := svc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "outside", Megagroup: true, Date: int(time.Now().Unix())})
	if err != nil {
		t.Fatal(err)
	}
	res, err := svc.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: created.Channel.ID, MemberID: bot.ID, AdminRights: domain.ChannelAdminRights{PinMessages: true}})
	if err != nil {
		t.Fatal(err)
	}
	if len(res.BotUpdates) != 1 {
		t.Fatalf("bot updates = %+v, want one", res.BotUpdates)
	}
	p := res.BotUpdates[0].ChannelParticipant
	if p.Previous.UserID != 0 || p.Current.Role != domain.ChannelRoleAdmin || p.Current.Status != domain.ChannelMemberActive {
		t.Fatalf("outside bot promotion record = %+v, want no previous and an active admin", p)
	}
}

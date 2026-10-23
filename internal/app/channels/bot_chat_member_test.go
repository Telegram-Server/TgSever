package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 超级群/频道里其他成员的变动发给群里每个是管理员的 bot（#146，TDLib 转成 chat_member）：
// 加入、拉入、离开、通过邀请链接入群、入群申请被批准、限制、封禁、解封、设为或撤销管理员
// 各一条，qts 按发生顺序递增；不是管理员的 bot 收不到其他成员的变动。
func TestOtherMemberChangesGoToEveryAdminBot(t *testing.T) {
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
	owner, adminA, adminB, plainBot := newUser(false), newUser(true), newUser(true), newUser(true)
	now := int(time.Now().Unix())
	for _, broadcast := range []bool{false, true} {
		human, linkJoiner, requester := newUser(false), newUser(false), newUser(false)
		mustOK := func(step string, err error) {
			t.Helper()
			if err != nil {
				t.Fatalf("broadcast=%v %s: %v", broadcast, step, err)
			}
		}
		created, err := svc.CreateChannel(ctx, owner, domain.CreateChannelRequest{Title: "audience", Megagroup: !broadcast, Broadcast: broadcast, Date: now})
		mustOK("create", err)
		channelID := created.Channel.ID
		user := func(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeUser, ID: id} }
		_, err = svc.InviteToChannel(ctx, owner, channelID, []int64{adminA, adminB, plainBot}, now)
		mustOK("invite bots", err)
		for _, bot := range []int64{adminA, adminB} {
			_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: bot, AdminRights: domain.ChannelAdminRights{DeleteMessages: true, BanUsers: true, InviteUsers: true}, Date: now})
			mustOK("promote bot", err)
		}
		link, err := svc.ExportInvite(ctx, owner, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, Date: now})
		mustOK("export link", err)
		approval, err := svc.ExportInvite(ctx, owner, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, RequestNeeded: true, Date: now})
		mustOK("export approval link", err)
		before := map[int64]int{}
		for _, bot := range []int64{adminA, adminB, plainBot} {
			st, err := updates.BotUpdatesState(ctx, bot)
			mustOK("state", err)
			before[bot] = st.Qts
		}

		_, err = svc.JoinChannel(ctx, human, channelID, now)
		mustOK("join", err)
		_, err = svc.LeaveChannel(ctx, human, channelID, now)
		mustOK("leave", err)
		_, err = svc.InviteToChannel(ctx, owner, channelID, []int64{human}, now)
		mustOK("invite", err)
		_, err = svc.ImportInvite(ctx, linkJoiner, domain.ImportChannelInviteRequest{UserID: linkJoiner, Hash: link.Invite.Hash, Date: now})
		mustOK("join by link", err)
		if _, err = svc.ImportInvite(ctx, requester, domain.ImportChannelInviteRequest{UserID: requester, Hash: approval.Invite.Hash, Date: now}); !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("broadcast=%v request to join: %v, want request sent", broadcast, err)
		}
		_, err = svc.HideChatJoinRequest(ctx, owner, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: channelID, TargetUserID: requester, Approved: true, Date: now})
		mustOK("approve request", err)
		_, err = svc.EditBanned(ctx, owner, domain.EditChannelBannedRequest{ChannelID: channelID, Participant: user(human), BannedRights: domain.ChannelBannedRights{SendMessages: true}, Date: now})
		mustOK("restrict", err)
		_, err = svc.EditBanned(ctx, adminA, domain.EditChannelBannedRequest{ChannelID: channelID, Participant: user(human), BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: now})
		mustOK("ban by admin bot", err)
		_, err = svc.EditBanned(ctx, owner, domain.EditChannelBannedRequest{ChannelID: channelID, Participant: user(human), Date: now})
		mustOK("unban", err)
		_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: linkJoiner, AdminRights: domain.ChannelAdminRights{PinMessages: true, PostMessages: broadcast}, Date: now})
		mustOK("promote", err)
		_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: linkJoiner, Date: now})
		mustOK("demote", err)

		type step struct {
			name        string
			member      int64
			actor       int64
			hadPrevious bool
			status      domain.ChannelMemberStatus
			role        domain.ChannelMemberRole
			restricted  bool
		}
		want := []step{
			{"joined", human, human, false, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"left", human, human, true, domain.ChannelMemberLeft, domain.ChannelRoleMember, false},
			{"invited", human, owner, true, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"joined by link", linkJoiner, linkJoiner, false, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"request approved", requester, owner, false, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
			{"restricted", human, owner, true, domain.ChannelMemberActive, domain.ChannelRoleMember, true},
			{"banned", human, adminA, true, domain.ChannelMemberKicked, domain.ChannelRoleMember, false},
			{"unbanned", human, owner, true, domain.ChannelMemberLeft, domain.ChannelRoleMember, false},
			{"promoted", linkJoiner, owner, true, domain.ChannelMemberActive, domain.ChannelRoleAdmin, false},
			{"demoted", linkJoiner, owner, true, domain.ChannelMemberActive, domain.ChannelRoleMember, false},
		}
		for _, bot := range []int64{adminA, adminB} {
			all, err := updates.ListBotUpdates(ctx, bot, before[bot], 100)
			mustOK("list", err)
			// 入群申请本身另有 updateBotChatInviteRequester（#149），这里只看成员变动；qts 仍
			// 单调递增，按出现顺序核对。
			var rows []domain.BotUpdate
			for _, row := range all {
				if row.ChannelParticipant != nil {
					rows = append(rows, row)
				}
			}
			if len(rows) != len(want) {
				t.Fatalf("broadcast=%v bot %d: %d qts records, want %d: %+v", broadcast, bot, len(rows), len(want), rows)
			}
			lastQts := before[bot]
			for i, w := range want {
				row := rows[i]
				p := row.ChannelParticipant
				if row.BotUserID != bot || row.Qts <= lastQts || p == nil || p.Channel.ID != channelID || p.ActorUserID != w.actor {
					t.Fatalf("broadcast=%v bot %d %s: record %+v (payload %+v)", broadcast, bot, w.name, row, p)
				}
				if (p.Previous.UserID != 0) != w.hadPrevious {
					t.Fatalf("broadcast=%v bot %d %s: previous %+v, want had=%v", broadcast, bot, w.name, p.Previous, w.hadPrevious)
				}
				if p.Current.UserID != w.member || p.Current.Status != w.status || p.Current.Role != w.role || p.Current.BannedRights.SendMessages != w.restricted {
					t.Fatalf("broadcast=%v bot %d %s: current %+v, want member %d %s/%s restricted=%v", broadcast, bot, w.name, p.Current, w.member, w.status, w.role, w.restricted)
				}
				lastQts = row.Qts
				// 接收者自己在群里的身份随记录保存，在线推送时用它投影 chat。
				if p.Viewer.UserID != bot || p.Viewer.Role != domain.ChannelRoleAdmin {
					t.Fatalf("broadcast=%v bot %d %s: viewer %+v, want the admin bot itself", broadcast, bot, w.name, p.Viewer)
				}
			}
		}
		if rows, _ := updates.ListBotUpdates(ctx, plainBot, before[plainBot], 100); len(rows) != 0 {
			t.Fatalf("broadcast=%v: non-admin bot got other members' changes: %+v", broadcast, rows)
		}
	}
}

// 管理员 bot 也会收到另一个 bot 的变动；被变动的 bot 自己只收一条（自己的 my_chat_member），
// 被撤销管理员后不再收到其他成员的变动。
func TestAdminBotAudienceFollowsAdminStatus(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	store := memory.NewChannelStore()
	store.AttachBotUpdateStore(updates)
	svc := NewService(store)
	owner, _ := users.Create(ctx, domain.User{AccessHash: 5, FirstName: "owner"})
	watcher, _ := users.Create(ctx, domain.User{AccessHash: 6, FirstName: "watcher", Bot: true})
	other, _ := users.Create(ctx, domain.User{AccessHash: 7, FirstName: "other", Bot: true})
	human, _ := users.Create(ctx, domain.User{AccessHash: 8, FirstName: "human"})
	now := int(time.Now().Unix())
	created, err := svc.CreateChannel(ctx, owner.ID, domain.CreateChannelRequest{Title: "bots", Megagroup: true, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	channelID := created.Channel.ID
	if _, err := svc.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: watcher.ID, AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	res, err := svc.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: other.ID, AdminRights: domain.ChannelAdminRights{DeleteMessages: true}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	got := map[int64]int{}
	for _, u := range res.BotUpdates {
		got[u.BotUserID]++
		if u.ChannelParticipant.Current.UserID != other.ID {
			t.Fatalf("record about %d, want %d", u.ChannelParticipant.Current.UserID, other.ID)
		}
	}
	if got[watcher.ID] != 1 || got[other.ID] != 1 || len(got) != 2 {
		t.Fatalf("promoting a bot: records per bot %v, want one each for the promoted bot and the watching admin bot", got)
	}
	if _, err := svc.EditAdmin(ctx, owner.ID, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: watcher.ID, Date: now}); err != nil {
		t.Fatal(err)
	}
	joined, err := svc.InviteToChannel(ctx, owner.ID, channelID, []int64{human.ID}, now)
	if err != nil {
		t.Fatal(err)
	}
	if len(joined.BotUpdates) != 1 || joined.BotUpdates[0].BotUserID != other.ID {
		t.Fatalf("after demoting the watcher: records %+v, want only the remaining admin bot", joined.BotUpdates)
	}
}

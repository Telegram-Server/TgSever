package channels

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 入群申请发给管理员 bot（#149）：用户经需要审批的邀请链接或公开群的入群申请提交申请时，
// 群里有邀请权限的每个管理员 bot 各收到一条 updateBotChatInviteRequester 记录（申请人、
// 邀请链接、申请人简介）；没有邀请权限的管理员 bot、普通成员 bot、人类管理员都收不到。
// 有邀请权限的 bot 随后以自己的身份批准，申请人入群。
func TestJoinRequestReachesInviteAdminBots(t *testing.T) {
	ctx := context.Background()
	users := memory.NewUserStore()
	updates := memory.NewBotUpdateStore(users)
	store := memory.NewChannelStore()
	store.AttachBotUpdateStore(updates)
	svc := NewService(store)
	newUser := func(bot bool, about string) int64 {
		t.Helper()
		u, err := users.Create(ctx, domain.User{AccessHash: 5, FirstName: "u", Bot: bot, About: about})
		if err != nil {
			t.Fatal(err)
		}
		return u.ID
	}
	owner := newUser(false, "")
	inviteBot, noInviteBot, memberBot, humanAdmin := newUser(true, ""), newUser(true, ""), newUser(true, ""), newUser(false, "")
	applicant := newUser(false, "let me in")
	publicApplicant := newUser(false, "public bio")
	now := int(time.Now().Unix())
	mustOK := func(step string, err error) {
		t.Helper()
		if err != nil {
			t.Fatalf("%s: %v", step, err)
		}
	}

	created, err := svc.CreateChannel(ctx, owner, domain.CreateChannelRequest{Title: "requests", Megagroup: true, Date: now})
	mustOK("create channel", err)
	channelID := created.Channel.ID
	_, err = svc.InviteToChannel(ctx, owner, channelID, []int64{inviteBot, noInviteBot, memberBot, humanAdmin}, now)
	mustOK("invite members", err)
	_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: inviteBot, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: now})
	mustOK("promote invite bot", err)
	_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: noInviteBot, AdminRights: domain.ChannelAdminRights{DeleteMessages: true, ChangeInfo: true}, Date: now})
	mustOK("promote bot without invite right", err)
	_, err = svc.EditAdmin(ctx, owner, domain.EditChannelAdminRequest{ChannelID: channelID, MemberID: humanAdmin, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: now})
	mustOK("promote human", err)

	stateOf := func(id int64) domain.BotUpdatesState {
		t.Helper()
		st, err := updates.BotUpdatesState(ctx, id)
		mustOK("state", err)
		return st
	}
	inviteBotBefore, noInviteBefore, memberBefore := stateOf(inviteBot), stateOf(noInviteBot), stateOf(memberBot)

	exported, err := svc.ExportInvite(ctx, owner, domain.ExportChannelInviteRequest{ChannelID: channelID, Title: "apply", RequestNeeded: true, Date: now})
	mustOK("export invite", err)
	res, err := svc.ImportInvite(ctx, applicant, domain.ImportChannelInviteRequest{Hash: exported.Invite.Hash, Date: now})
	if !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("import invite err = %v, want ErrInviteRequestSent", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].BotUserID != inviteBot {
		t.Fatalf("import invite bot updates = %+v, want one for the invite bot", res.BotUpdates)
	}

	rows, err := updates.ListBotUpdates(ctx, inviteBot, inviteBotBefore.Qts, 10)
	mustOK("list invite bot", err)
	if len(rows) != 1 || rows[0].Qts != inviteBotBefore.Qts+1 {
		t.Fatalf("invite bot rows = %+v, want one at qts %d", rows, inviteBotBefore.Qts+1)
	}
	got := rows[0].ChatInviteRequester
	if got == nil {
		t.Fatalf("invite bot row has no chat invite requester: %+v", rows[0])
	}
	if got.Channel.ID != channelID || got.RequesterUserID != applicant || got.About != "let me in" ||
		got.Invite.Hash != exported.Invite.Hash || got.Invite.Title != "apply" || !got.Invite.RequestNeeded ||
		got.Admin.UserID != inviteBot || !got.Admin.AdminRights.InviteUsers || rows[0].Date != now {
		t.Fatalf("chat invite requester = %+v (date %d)", got, rows[0].Date)
	}

	// 公开群的入群申请：没有邀请链接，Invite 为零值。
	_, err = svc.UpdateUsername(ctx, owner, domain.UpdateChannelUsernameRequest{ChannelID: channelID, Username: "joinrequestsbots"})
	mustOK("set username", err)
	_, err = svc.SetJoinRequest(ctx, owner, channelID, true)
	mustOK("enable join request", err)
	res, err = svc.JoinChannel(ctx, publicApplicant, channelID, now)
	if !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("join channel err = %v, want ErrInviteRequestSent", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].BotUserID != inviteBot {
		t.Fatalf("public join bot updates = %+v, want one for the invite bot", res.BotUpdates)
	}
	rows, err = updates.ListBotUpdates(ctx, inviteBot, inviteBotBefore.Qts+1, 10)
	mustOK("list invite bot after public request", err)
	if len(rows) != 1 || rows[0].ChatInviteRequester == nil {
		t.Fatalf("public request rows = %+v", rows)
	}
	if p := rows[0].ChatInviteRequester; p.RequesterUserID != publicApplicant || p.About != "public bio" || p.Invite.InviteID != 0 || p.Invite.Hash != "" {
		t.Fatalf("public chat invite requester = %+v", p)
	}

	for name, st := range map[string]struct {
		id     int64
		before domain.BotUpdatesState
	}{"bot without invite right": {noInviteBot, noInviteBefore}, "member bot": {memberBot, memberBefore}} {
		if after := stateOf(st.id); after.Qts != st.before.Qts {
			t.Fatalf("%s qts %d -> %d, want unchanged", name, st.before.Qts, after.Qts)
		}
	}
	if st := stateOf(humanAdmin); st.Qts != domain.BotInitialQts {
		t.Fatalf("human admin got bot qts %d", st.Qts)
	}

	// 重复提交不是新的申请，不产生记录。
	if _, err := svc.ImportInvite(ctx, applicant, domain.ImportChannelInviteRequest{Hash: exported.Invite.Hash, Date: now}); !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("repeat import err = %v", err)
	}
	if st := stateOf(inviteBot); st.Qts != inviteBotBefore.Qts+2 {
		t.Fatalf("invite bot qts after repeat = %d, want %d", st.Qts, inviteBotBefore.Qts+2)
	}

	// bot 以自己的身份批准申请，申请人入群。
	approved, err := svc.HideChatJoinRequest(ctx, inviteBot, domain.HideChannelJoinRequestRequest{ChannelID: channelID, TargetUserID: applicant, Approved: true, Date: now})
	mustOK("bot approves", err)
	if len(approved.Members) != 1 || approved.Members[0].UserID != applicant || approved.Members[0].Status != domain.ChannelMemberActive {
		t.Fatalf("approved members = %+v", approved.Members)
	}
}

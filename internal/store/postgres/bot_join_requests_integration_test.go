package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// 入群申请与发给管理员 bot 的 updateBotChatInviteRequester 同事务写入（#149）：申请提交后
// 有邀请权限的管理员 bot 能按 qts 读回记录（申请人、链接、简介），没有邀请权限的 bot 收不到；
// 写 qts 记录失败时申请本身也不留下。
func TestJoinRequestBotUpdatesWrittenWithRequestPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channels := NewChannelStore(pool)
	updates := NewBotUpdateStore(pool)
	owner := createBotUpdateTestUser(t, pool, false)
	inviteBot, otherBot := createBotUpdateTestUser(t, pool, true), createBotUpdateTestUser(t, pool, true)
	applicant, publicApplicant, failedApplicant := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, false)
	if _, err := pool.Exec(ctx, `UPDATE users SET about = 'please' WHERE id = $1`, applicant); err != nil {
		t.Fatalf("set about: %v", err)
	}
	now := int(time.Now().Unix())
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "requests", Megagroup: true, MemberUserIDs: []int64{inviteBot, otherBot}, Date: now})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	for bot, rights := range map[int64]domain.ChannelAdminRights{inviteBot: {InviteUsers: true}, otherBot: {ChangeInfo: true, DeleteMessages: true}} {
		if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: bot, AdminRights: rights, Date: now}); err != nil {
			t.Fatalf("promote bot %d: %v", bot, err)
		}
	}
	inviteBefore, _ := updates.BotUpdatesState(ctx, inviteBot)
	otherBefore, _ := updates.BotUpdatesState(ctx, otherBot)

	exported, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, Title: "apply", RequestNeeded: true, Date: now})
	if err != nil {
		t.Fatalf("export invite: %v", err)
	}
	res, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: applicant, Hash: exported.Invite.Hash, Date: now})
	if !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("import invite err = %v", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].BotUserID != inviteBot || res.BotUpdates[0].Qts != inviteBefore.Qts+1 {
		t.Fatalf("import bot updates = %+v", res.BotUpdates)
	}
	rows, err := updates.ListBotUpdates(ctx, inviteBot, inviteBefore.Qts, 10)
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(rows) != 1 || rows[0].ChatInviteRequester == nil {
		t.Fatalf("invite bot rows = %+v", rows)
	}
	if p := rows[0].ChatInviteRequester; p.Channel.ID != channelID || p.RequesterUserID != applicant || p.About != "please" ||
		p.Invite.Hash != exported.Invite.Hash || p.Invite.Title != "apply" || p.Admin.UserID != inviteBot {
		t.Fatalf("chat invite requester = %+v", p)
	}

	if _, err := channels.UpdateUsername(ctx, domain.UpdateChannelUsernameRequest{UserID: owner, ChannelID: channelID, Username: fmt.Sprintf("joinreqbots%d", channelID)}); err != nil {
		t.Fatalf("set username: %v", err)
	}
	if _, err := channels.SetJoinRequest(ctx, owner, channelID, true); err != nil {
		t.Fatalf("enable join request: %v", err)
	}
	res, err = channels.JoinChannel(ctx, channelID, publicApplicant, now)
	if !errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("join channel err = %v", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].ChatInviteRequester == nil || res.BotUpdates[0].ChatInviteRequester.Invite.InviteID != 0 ||
		res.BotUpdates[0].ChatInviteRequester.RequesterUserID != publicApplicant {
		t.Fatalf("public join bot updates = %+v", res.BotUpdates)
	}
	if st, _ := updates.BotUpdatesState(ctx, otherBot); st.Qts != otherBefore.Qts {
		t.Fatalf("bot without invite right qts %d -> %d", otherBefore.Qts, st.Qts)
	}

	// 写 qts 记录失败：申请随事务回滚。
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION fail_bot_join_request_qts() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.bot_user_id = %d THEN
        RAISE EXCEPTION 'injected bot update failure';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER fail_bot_join_request_qts BEFORE INSERT ON bot_updates
FOR EACH ROW EXECUTE FUNCTION fail_bot_join_request_qts();`, inviteBot)); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}
	t.Cleanup(func() {
		_, _ = pool.Exec(context.Background(), `DROP TRIGGER IF EXISTS fail_bot_join_request_qts ON bot_updates; DROP FUNCTION IF EXISTS fail_bot_join_request_qts()`)
	})
	if _, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: failedApplicant, Hash: exported.Invite.Hash, Date: now}); err == nil || errors.Is(err, domain.ErrInviteRequestSent) {
		t.Fatalf("import with failing bot update err = %v, want a failure", err)
	}
	var pending int
	if err := pool.QueryRow(ctx, `SELECT count(*) FROM channel_invite_importers WHERE channel_id = $1 AND user_id = $2`, channelID, failedApplicant).Scan(&pending); err != nil {
		t.Fatalf("count importers: %v", err)
	}
	if pending != 0 {
		t.Fatalf("join request survived the failed bot update")
	}
}

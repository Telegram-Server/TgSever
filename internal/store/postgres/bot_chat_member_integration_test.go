package postgres

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// 其他成员变动的每条写入路径都在源事件的事务里给管理员 bot 写 qts 记录（#146）；
// 不是管理员的 bot 没有记录。生成规则的完整矩阵在 app/channels 的业务层测试里。
func TestOtherMemberChangesReachAdminBotsPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channels := NewChannelStore(pool)
	updates := NewBotUpdateStore(pool)
	owner, adminBot, plainBot := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, true), createBotUpdateTestUser(t, pool, true)
	human, linkJoiner, requester := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, false)
	now := int(time.Now().Unix())
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "audience", Megagroup: true, MemberUserIDs: []int64{adminBot, plainBot}, Date: now})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: adminBot, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
	link, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, Date: now})
	if err != nil {
		t.Fatalf("export link: %v", err)
	}
	approval, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, RequestNeeded: true, Date: now})
	if err != nil {
		t.Fatalf("export approval link: %v", err)
	}
	start, err := updates.BotUpdatesState(ctx, adminBot)
	if err != nil {
		t.Fatal(err)
	}
	plainStart, err := updates.BotUpdatesState(ctx, plainBot)
	if err != nil {
		t.Fatal(err)
	}
	human2 := domain.Peer{Type: domain.PeerTypeUser, ID: human}
	steps := []struct {
		name   string
		member int64
		run    func() ([]domain.BotUpdate, error)
	}{
		{"join", human, func() ([]domain.BotUpdate, error) {
			res, err := channels.JoinChannel(ctx, channelID, human, now)
			return res.BotUpdates, err
		}},
		{"leave", human, func() ([]domain.BotUpdate, error) {
			res, err := channels.LeaveChannel(ctx, channelID, human, now)
			return res.BotUpdates, err
		}},
		{"invite", human, func() ([]domain.BotUpdate, error) {
			res, err := channels.InviteToChannel(ctx, channelID, owner, []int64{human}, now)
			return res.BotUpdates, err
		}},
		{"join by link", linkJoiner, func() ([]domain.BotUpdate, error) {
			res, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: linkJoiner, Hash: link.Invite.Hash, Date: now})
			return res.BotUpdates, err
		}},
		{"request approved", requester, func() ([]domain.BotUpdate, error) {
			if _, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: requester, Hash: approval.Invite.Hash, Date: now}); !errors.Is(err, domain.ErrInviteRequestSent) {
				return nil, fmt.Errorf("request to join: %v", err)
			}
			res, err := channels.HideChatJoinRequest(ctx, domain.HideChannelJoinRequestRequest{UserID: owner, ChannelID: channelID, TargetUserID: requester, Approved: true, Date: now})
			return res.BotUpdates, err
		}},
		{"ban", human, func() ([]domain.BotUpdate, error) {
			res, err := channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: channelID, Participant: human2, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: now})
			return res.BotUpdates, err
		}},
		{"promote", linkJoiner, func() ([]domain.BotUpdate, error) {
			res, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: linkJoiner, AdminRights: domain.ChannelAdminRights{PinMessages: true}, Date: now})
			return res.BotUpdates, err
		}},
	}
	for i, step := range steps {
		got, err := step.run()
		if err != nil {
			t.Fatalf("%s: %v", step.name, err)
		}
		if len(got) != 1 || got[0].BotUserID != adminBot || got[0].Qts != start.Qts+i+1 ||
			got[0].ChannelParticipant.Current.UserID != step.member || got[0].ChannelParticipant.Viewer.UserID != adminBot {
			t.Fatalf("%s: result bot updates %+v, want one for the admin bot at qts %d", step.name, got, start.Qts+i+1)
		}
	}
	rows, err := updates.ListBotUpdates(ctx, adminBot, start.Qts, 100)
	if err != nil || len(rows) != len(steps) {
		t.Fatalf("admin bot records = %d, %v; want %d", len(rows), err, len(steps))
	}
	if rows[3].ChannelParticipant.Viewer.Role != domain.ChannelRoleAdmin || rows[3].ChannelParticipant.ActorUserID != linkJoiner {
		t.Fatalf("join-by-link record %+v, want the link joiner as actor and the admin bot as viewer", rows[3].ChannelParticipant)
	}
	if st, err := updates.BotUpdatesState(ctx, plainBot); err != nil || st.Qts != plainStart.Qts {
		t.Fatalf("non-admin bot qts state = %+v, %v; want unchanged at %d", st, err, plainStart.Qts)
	}
}

// 入群（邀请链接）在写入管理员 bot 的 qts 记录之后失败回滚：不留记录，也不消耗 qts。
func TestAdminBotRecordRollsBackWithJoinByLinkPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	channels := NewChannelStore(pool)
	updates := NewBotUpdateStore(pool)
	owner, adminBot, joiner := createBotUpdateTestUser(t, pool, false), createBotUpdateTestUser(t, pool, true), createBotUpdateTestUser(t, pool, false)
	now := int(time.Now().Unix())
	created, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "rollback", Megagroup: true, Date: now})
	if err != nil {
		t.Fatalf("create channel: %v", err)
	}
	channelID := created.Channel.ID
	if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: adminBot, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatalf("promote bot: %v", err)
	}
	link, err := channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, Date: now})
	if err != nil {
		t.Fatalf("export link: %v", err)
	}
	before, err := updates.BotUpdatesState(ctx, adminBot)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := pool.Exec(ctx, fmt.Sprintf(`
CREATE FUNCTION fail_admin_bot_join_log() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    IF NEW.channel_id = %d THEN
        RAISE EXCEPTION 'injected admin log failure';
    END IF;
    RETURN NEW;
END $$;
CREATE TRIGGER fail_admin_bot_join_log BEFORE INSERT ON channel_admin_log_events
FOR EACH ROW EXECUTE FUNCTION fail_admin_bot_join_log();`, channelID)); err != nil {
		t.Fatalf("install failing trigger: %v", err)
	}
	if _, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: joiner, Hash: link.Invite.Hash, Date: now}); err == nil {
		t.Fatal("join by link succeeded despite the injected failure")
	}
	if st, err := updates.BotUpdatesState(ctx, adminBot); err != nil || st.Qts != before.Qts {
		t.Fatalf("admin bot qts after rollback = %+v, %v; want %d", st, err, before.Qts)
	}
	if _, err := pool.Exec(ctx, `DROP TRIGGER fail_admin_bot_join_log ON channel_admin_log_events`); err != nil {
		t.Fatalf("drop trigger: %v", err)
	}
	res, err := channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: joiner, Hash: link.Invite.Hash, Date: now})
	if err != nil {
		t.Fatalf("join by link after dropping trigger: %v", err)
	}
	if len(res.BotUpdates) != 1 || res.BotUpdates[0].Qts != before.Qts+1 {
		t.Fatalf("bot updates after the join = %+v, want qts %d", res.BotUpdates, before.Qts+1)
	}
}

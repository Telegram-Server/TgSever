package storetest

import (
	"context"
	"errors"
	"testing"
	"time"

	"telesrv/internal/domain"
)

// BotUpdateAudienceContract 是"哪些 bot 收到"的存储契约，memory 与 Postgres 共跑：
//
//   - 其他成员的变动（#146）：群里的管理员 bot（含群主）各收一条，普通成员 bot 收不到，
//     被撤销管理员之后也不再收；
//   - 入群申请（#149）：只有群主与带邀请用户权限的管理员 bot 收到；
//   - 消息反应（#150）：管理员 bot 收到别人的反应，普通成员 bot 收不到；bot 自己在超级群里
//     的反应不发给任何 bot。
func BotUpdateAudienceContract(t *testing.T, newFixture func(t *testing.T) BotUpdatesFixture) {
	t.Helper()
	ctx := context.Background()
	now := int(time.Now().Unix())

	// since 记下每个 bot 当前的 qts，返回读出其后新记录的函数。
	since := func(t *testing.T, f BotUpdatesFixture, bots ...int64) func(bot int64) []domain.BotUpdate {
		t.Helper()
		start := make(map[int64]int, len(bots))
		for _, bot := range bots {
			st, err := f.Updates.BotUpdatesState(ctx, bot)
			if err != nil {
				t.Fatalf("bot updates state: %v", err)
			}
			start[bot] = st.Qts
		}
		return func(bot int64) []domain.BotUpdate {
			t.Helper()
			rows, err := f.Updates.ListBotUpdates(ctx, bot, start[bot], 100)
			if err != nil {
				t.Fatalf("list bot updates: %v", err)
			}
			return rows
		}
	}
	newGroup := func(t *testing.T, f BotUpdatesFixture, owner int64, members ...int64) int64 {
		t.Helper()
		created, err := f.Channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "audience", Megagroup: true, MemberUserIDs: members, Date: now})
		if err != nil {
			t.Fatalf("create channel: %v", err)
		}
		return created.Channel.ID
	}
	promote := func(t *testing.T, f BotUpdatesFixture, owner, channelID, member int64, rights domain.ChannelAdminRights) {
		t.Helper()
		if _, err := f.Channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: channelID, MemberID: member, AdminRights: rights, Date: now}); err != nil {
			t.Fatalf("edit admin %d: %v", member, err)
		}
	}

	t.Run("other members' changes reach admin bots only", func(t *testing.T) {
		f := newFixture(t)
		owner, adminBot, plainBot, human, joiner := f.NewUser(t, false), f.NewUser(t, true), f.NewUser(t, true), f.NewUser(t, false), f.NewUser(t, false)
		channelID := newGroup(t, f, owner, adminBot, plainBot, human)
		promote(t, f, owner, channelID, adminBot, domain.ChannelAdminRights{BanUsers: true})
		recorded := since(t, f, adminBot, plainBot)

		if _, err := f.Channels.InviteToChannel(ctx, channelID, owner, []int64{joiner}, now); err != nil {
			t.Fatalf("invite: %v", err)
		}
		if _, err := f.Channels.EditChannelMemberRank(ctx, domain.EditChannelMemberRankRequest{UserID: owner, ChannelID: channelID, MemberID: human, Rank: "tag", Date: now}); err != nil {
			t.Fatalf("edit rank: %v", err)
		}
		if _, err := f.Channels.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: channelID, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: human}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: now}); err != nil {
			t.Fatalf("kick: %v", err)
		}
		rows := recorded(adminBot)
		wantSubjects := []int64{joiner, human, human}
		if len(rows) != len(wantSubjects) {
			t.Fatalf("admin bot records = %+v, want %d about other members", rows, len(wantSubjects))
		}
		for i, row := range rows {
			p := row.ChannelParticipant
			if p == nil || p.Current.UserID != wantSubjects[i] || p.Viewer.UserID != adminBot || p.Channel.ID != channelID {
				t.Fatalf("admin bot record %d = %+v, want a change of %d seen as the admin bot", i, p, wantSubjects[i])
			}
		}
		if extra := recorded(plainBot); len(extra) != 0 {
			t.Fatalf("plain member bot records = %+v, want none", extra)
		}

		promote(t, f, owner, channelID, adminBot, domain.ChannelAdminRights{})
		demoted := since(t, f, adminBot)
		if _, err := f.Channels.LeaveChannel(ctx, channelID, joiner, now); err != nil {
			t.Fatalf("leave: %v", err)
		}
		if extra := demoted(adminBot); len(extra) != 0 {
			t.Fatalf("demoted bot records = %+v, want none", extra)
		}
	})

	t.Run("join requests reach admin bots that can invite", func(t *testing.T) {
		f := newFixture(t)
		owner, inviteBot, otherAdminBot, plainBot, applicant := f.NewUser(t, false), f.NewUser(t, true), f.NewUser(t, true), f.NewUser(t, true), f.NewUser(t, false)
		channelID := newGroup(t, f, owner, inviteBot, otherAdminBot, plainBot)
		promote(t, f, owner, channelID, inviteBot, domain.ChannelAdminRights{InviteUsers: true})
		promote(t, f, owner, channelID, otherAdminBot, domain.ChannelAdminRights{DeleteMessages: true})
		exported, err := f.Channels.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: channelID, RequestNeeded: true, Date: now})
		if err != nil {
			t.Fatalf("export invite: %v", err)
		}
		recorded := since(t, f, inviteBot, otherAdminBot, plainBot)
		res, err := f.Channels.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: applicant, Hash: exported.Invite.Hash, Date: now})
		if !errors.Is(err, domain.ErrInviteRequestSent) {
			t.Fatalf("import invite err = %v, want ErrInviteRequestSent", err)
		}
		rows := recorded(inviteBot)
		if len(rows) != 1 || rows[0].ChatInviteRequester == nil || rows[0].ChatInviteRequester.RequesterUserID != applicant {
			t.Fatalf("invite bot records = %+v, want one join request from the applicant", rows)
		}
		if len(res.BotUpdates) != 1 || res.BotUpdates[0].BotUserID != inviteBot || res.BotUpdates[0].Qts != rows[0].Qts {
			t.Fatalf("import result bot updates = %+v, want the invite bot's record", res.BotUpdates)
		}
		for name, bot := range map[string]int64{"admin bot without invite right": otherAdminBot, "plain member bot": plainBot} {
			if extra := recorded(bot); len(extra) != 0 {
				t.Fatalf("%s records = %+v, want none", name, extra)
			}
		}
	})

	t.Run("reactions reach admin bots but not the reacting bot", func(t *testing.T) {
		f := newFixture(t)
		owner, adminBot, plainBot, human := f.NewUser(t, false), f.NewUser(t, true), f.NewUser(t, true), f.NewUser(t, false)
		channelID := newGroup(t, f, owner, adminBot, plainBot, human)
		promote(t, f, owner, channelID, adminBot, domain.ChannelAdminRights{DeleteMessages: true})
		sent, err := f.Channels.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: owner, ChannelID: channelID, RandomID: time.Now().UnixNano(), Message: "vote", Date: now})
		if err != nil {
			t.Fatalf("send: %v", err)
		}
		thumbs := []domain.MessageReaction{{Type: domain.MessageReactionEmoji, Emoticon: "👍"}}
		react := func(user int64) {
			t.Helper()
			if _, err := f.Channels.SetChannelMessageReactions(ctx, domain.SetChannelMessageReactionsRequest{UserID: user, ChannelID: channelID, MessageID: sent.Message.ID, Reactions: thumbs, ReactionsPerUserMax: 1, Date: now}); err != nil {
				t.Fatalf("react as %d: %v", user, err)
			}
		}
		recorded := since(t, f, adminBot, plainBot)
		react(human)
		react(plainBot)
		rows := recorded(adminBot)
		if len(rows) != 1 || rows[0].MessageReaction == nil || rows[0].MessageReaction.Actor != (domain.Peer{Type: domain.PeerTypeUser, ID: human}) {
			t.Fatalf("admin bot records = %+v, want only the human's reaction", rows)
		}
		if extra := recorded(plainBot); len(extra) != 0 {
			t.Fatalf("plain member bot records = %+v, want none", extra)
		}
	})
}

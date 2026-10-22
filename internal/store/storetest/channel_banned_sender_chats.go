package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelBannedSenderChatStore 是频道 peer 封禁契约需要的存储能力。
type ChannelBannedSenderChatStore interface {
	store.ChannelStore
	store.ChannelBannedSenderChatStore
}

// ChannelBannedSenderChats 校验频道 peer 封禁记录（#137）：权限、只接受全封/解封、
// 目标频道校验、幂等、管理日志、查询与分页。
// users：群主 O、有封禁权的管理员 B、没有封禁权的管理员 I、普通成员 M。
func ChannelBannedSenderChats(t *testing.T, s ChannelBannedSenderChatStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, banAdmin, plainAdmin, member := users[0], users[1], users[2], users[3]
	now := baseNow()
	group, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Sender chat bans", Megagroup: true, MemberUserIDs: []int64{banAdmin, plainAdmin, member}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	groupID := group.Channel.ID
	for _, promote := range []struct {
		id     int64
		rights domain.ChannelAdminRights
	}{{banAdmin, domain.ChannelAdminRights{BanUsers: true}}, {plainAdmin, domain.ChannelAdminRights{InviteUsers: true}}} {
		if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: groupID, MemberID: promote.id, AdminRights: promote.rights, Date: now}); err != nil {
			t.Fatal(err)
		}
	}
	newBroadcast := func(title string) int64 {
		t.Helper()
		res, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: member, Title: title, Broadcast: true, Date: now})
		if err != nil {
			t.Fatal(err)
		}
		return res.Channel.ID
	}
	alpha := newBroadcast("Alpha News")
	beta := newBroadcast("Beta Daily")

	full := domain.ChannelBannedRights{ViewMessages: true, SendMessages: true, UntilDate: 2147483647}
	edit := func(actor, target int64, rights domain.ChannelBannedRights, date int) (domain.EditChannelBannedSenderChatResult, error) {
		return s.EditChannelBannedSenderChat(ctx, domain.EditChannelBannedSenderChatRequest{UserID: actor, ChannelID: groupID, SenderChatID: target, BannedRights: rights, Date: date})
	}
	adminLog := func() []domain.ChannelAdminLogEvent {
		t.Helper()
		res, err := s.ListAdminLog(ctx, domain.ChannelAdminLogRequest{UserID: owner, ChannelID: groupID, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var out []domain.ChannelAdminLogEvent
		for _, event := range res.Events {
			if (event.NewParticipant != nil && event.NewParticipant.SenderChatID != 0) || (event.PrevParticipant != nil && event.PrevParticipant.SenderChatID != 0) {
				out = append(out, event)
			}
		}
		return out
	}
	list := func(viewer int64, query string, offset, limit int) domain.ChannelBannedSenderChatList {
		t.Helper()
		res, err := s.ListChannelBannedSenderChats(ctx, viewer, groupID, query, offset, limit)
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	banned := func(target int64) bool {
		t.Helper()
		_, ok, err := s.GetChannelBannedSenderChat(ctx, groupID, target)
		if err != nil {
			t.Fatal(err)
		}
		return ok
	}

	t.Run("没有封禁权限的被拒绝", func(t *testing.T) {
		for _, actor := range []int64{member, plainAdmin} {
			if _, err := edit(actor, alpha, full, now); !errors.Is(err, domain.ErrChannelAdminRequired) {
				t.Fatalf("actor %d ban = %v, want ErrChannelAdminRequired", actor, err)
			}
		}
		if banned(alpha) {
			t.Fatal("rejected ban was recorded")
		}
	})

	t.Run("只接受全封和解封", func(t *testing.T) {
		if _, err := edit(owner, alpha, domain.ChannelBannedRights{SendMessages: true}, now); !errors.Is(err, domain.ErrChannelBannedRightsInvalid) {
			t.Fatalf("partial ban = %v, want ErrChannelBannedRightsInvalid", err)
		}
		if banned(alpha) {
			t.Fatal("partial ban was recorded")
		}
	})

	t.Run("目标频道必须存在且不是本群", func(t *testing.T) {
		for _, target := range []int64{groupID, alpha + 1000000} {
			if _, err := edit(owner, target, full, now); !errors.Is(err, domain.ErrChannelSenderChatInvalid) {
				t.Fatalf("ban %d = %v, want ErrChannelSenderChatInvalid", target, err)
			}
			// 解封同样拒绝从未存在的频道和本群（已删除的频道可以解封，见下）。
			if _, err := edit(owner, target, domain.ChannelBannedRights{}, now); !errors.Is(err, domain.ErrChannelSenderChatInvalid) {
				t.Fatalf("unban %d = %v, want ErrChannelSenderChatInvalid", target, err)
			}
		}
	})

	t.Run("封禁、重复封禁幂等、查询可见", func(t *testing.T) {
		res, err := edit(banAdmin, alpha, full, now)
		if err != nil {
			t.Fatal(err)
		}
		want := domain.ChannelBannedSenderChat{ChannelID: groupID, SenderChatID: alpha, BannedByUserID: banAdmin, Date: now}
		if !res.Changed || !res.Banned || res.Ban != want || res.Channel.ID != groupID {
			t.Fatalf("ban result = %+v, want changed ban %+v", res, want)
		}
		got, ok, err := s.GetChannelBannedSenderChat(ctx, groupID, alpha)
		if err != nil || !ok || got != want {
			t.Fatalf("get = %+v %v %v, want %+v", got, ok, err, want)
		}
		again, err := edit(owner, alpha, domain.ChannelBannedRights{ViewMessages: true}, now+10)
		if err != nil {
			t.Fatal(err)
		}
		if again.Changed || !again.Banned || again.Ban != want {
			t.Fatalf("repeat ban = %+v, want unchanged %+v", again, want)
		}
		events := adminLog()
		if len(events) != 1 {
			t.Fatalf("admin log sender chat events = %d, want 1", len(events))
		}
		event := events[0]
		if event.Type != domain.ChannelAdminLogParticipantKick || event.UserID != banAdmin || event.Date != now ||
			event.NewParticipant == nil || event.NewParticipant.SenderChatID != alpha || event.NewParticipant.Status != domain.ChannelMemberKicked ||
			!event.NewParticipant.BannedRights.ViewMessages || event.NewParticipant.InviterUserID != banAdmin ||
			event.PrevParticipant == nil || event.PrevParticipant.SenderChatID != alpha || event.PrevParticipant.Status != domain.ChannelMemberLeft {
			t.Fatalf("ban admin log = %+v prev=%+v new=%+v", event, event.PrevParticipant, event.NewParticipant)
		}
		page := list(owner, "", 0, 10)
		if page.Count != 1 || len(page.Bans) != 1 || page.Bans[0] != want || len(page.SenderChats) != 1 || page.SenderChats[0].ID != alpha || page.SenderChats[0].Title != "Alpha News" {
			t.Fatalf("list = %+v", page)
		}
		if page := list(member, "", 0, 10); page.Count != 0 || len(page.Bans) != 0 {
			t.Fatalf("non-admin list = %+v, want empty", page)
		}
	})

	t.Run("分页与搜索", func(t *testing.T) {
		if _, err := edit(owner, beta, full, now); err != nil {
			t.Fatal(err)
		}
		lo, hi := alpha, beta
		if lo > hi {
			lo, hi = hi, lo
		}
		first := list(owner, "", 0, 1)
		second := list(owner, "", 1, 1)
		if first.Count != 2 || len(first.Bans) != 1 || first.Bans[0].SenderChatID != lo ||
			second.Count != 2 || len(second.Bans) != 1 || second.Bans[0].SenderChatID != hi {
			t.Fatalf("pages = %+v / %+v, want %d then %d", first, second, lo, hi)
		}
		if countOnly := list(owner, "", 0, 0); countOnly.Count != 2 || len(countOnly.Bans) != 0 {
			t.Fatalf("count-only = %+v", countOnly)
		}
		found := list(owner, "beta", 0, 10)
		if found.Count != 1 || len(found.Bans) != 1 || found.Bans[0].SenderChatID != beta {
			t.Fatalf("search beta = %+v", found)
		}
	})

	t.Run("解封、重复解封幂等", func(t *testing.T) {
		res, err := edit(banAdmin, alpha, domain.ChannelBannedRights{UntilDate: 123}, now+20)
		if err != nil {
			t.Fatal(err)
		}
		if !res.Changed || res.Banned || res.Ban.SenderChatID != alpha {
			t.Fatalf("unban = %+v", res)
		}
		if banned(alpha) {
			t.Fatal("alpha still banned after unban")
		}
		again, err := edit(owner, alpha, domain.ChannelBannedRights{}, now+30)
		if err != nil {
			t.Fatal(err)
		}
		if again.Changed || again.Banned {
			t.Fatalf("repeat unban = %+v, want unchanged", again)
		}
		events := adminLog()
		// alpha 封禁 + beta 封禁 + alpha 解封；重复解封不写日志。
		if len(events) != 3 {
			t.Fatalf("admin log sender chat events = %d, want 3", len(events))
		}
		unban := events[0] // 新的在前
		if unban.Type != domain.ChannelAdminLogParticipantUnkick || unban.UserID != banAdmin ||
			unban.PrevParticipant == nil || unban.PrevParticipant.SenderChatID != alpha || unban.PrevParticipant.Status != domain.ChannelMemberKicked ||
			unban.NewParticipant == nil || unban.NewParticipant.SenderChatID != alpha || unban.NewParticipant.Status != domain.ChannelMemberLeft {
			t.Fatalf("unban admin log = %+v prev=%+v new=%+v", unban, unban.PrevParticipant, unban.NewParticipant)
		}
		page := list(owner, "", 0, 10)
		if page.Count != 1 || len(page.Bans) != 1 || page.Bans[0].SenderChatID != beta {
			t.Fatalf("list after unban = %+v", page)
		}
	})

	t.Run("被封频道删除后不出现在列表里且仍可解封", func(t *testing.T) {
		doomed := newBroadcast("Doomed")
		if _, err := edit(owner, doomed, full, now); err != nil {
			t.Fatal(err)
		}
		if _, err := s.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: member, ChannelID: doomed, Date: now}); err != nil {
			t.Fatal(err)
		}
		page := list(owner, "", 0, 10)
		if page.Count != 1 || len(page.Bans) != 1 || page.Bans[0].SenderChatID != beta || len(page.SenderChats) != 1 || page.SenderChats[0].ID != beta {
			t.Fatalf("list after banned channel deleted = %+v, want only beta", page)
		}
		if countOnly := list(owner, "", 0, 0); countOnly.Count != 1 {
			t.Fatalf("count after banned channel deleted = %d, want 1", countOnly.Count)
		}
		res, err := edit(owner, doomed, domain.ChannelBannedRights{}, now+40)
		if err != nil {
			t.Fatalf("unban deleted channel: %v", err)
		}
		if !res.Changed || res.Banned || banned(doomed) {
			t.Fatalf("unban deleted channel = %+v, still banned=%v", res, banned(doomed))
		}
	})

	t.Run("已删除的频道不能被封禁", func(t *testing.T) {
		gone := newBroadcast("Gone")
		if _, err := s.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: member, ChannelID: gone, Date: now}); err != nil {
			t.Fatal(err)
		}
		if _, err := edit(owner, gone, full, now); !errors.Is(err, domain.ErrChannelSenderChatInvalid) {
			t.Fatalf("ban deleted channel = %v, want ErrChannelSenderChatInvalid", err)
		}
	})

	t.Run("批量读取频道 peer 基础行", func(t *testing.T) {
		gone := newBroadcast("Gone Too")
		if _, err := s.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: member, ChannelID: gone, Date: now}); err != nil {
			t.Fatal(err)
		}
		got, err := s.GetSenderChatsByIDs(ctx, []int64{beta, alpha, alpha, 0, gone, alpha + 1000000})
		if err != nil {
			t.Fatal(err)
		}
		lo, hi := alpha, beta
		if lo > hi {
			lo, hi = hi, lo
		}
		if len(got) != 2 || got[0].ID != lo || got[1].ID != hi || got[0].Title == "" {
			t.Fatalf("sender chats by ids = %+v, want %d and %d", got, lo, hi)
		}
	})
}

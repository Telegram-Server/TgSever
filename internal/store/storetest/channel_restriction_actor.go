package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelRestrictionActor 校验"谁执行的限制、何时限制的"单独记录，不借用邀请人字段：
// 设置限制（含踢出）时写 RestrictedBy/RestrictedAt 且邀请人不变；限制随成员重新入群
// （自行加入、被拉回）保留时，执行人和时间一起保留；解除限制时两项清零。
// users：群主、有封禁权的管理员 A、在群成员 M、在群成员 K。
func ChannelRestrictionActor(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, m, k := users[0], users[1], users[2], users[3]
	now := baseNow()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Restriction actor", Megagroup: true, MemberUserIDs: []int64{a, m, k}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: a, AdminRights: domain.ChannelAdminRights{BanUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	participant := func(target int64) domain.ChannelMember {
		t.Helper()
		got, err := s.GetParticipant(ctx, owner, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return got
	}
	listed := func(kind domain.ChannelParticipantsFilterKind, target int64) domain.ChannelMember {
		t.Helper()
		page, err := s.GetParticipants(ctx, owner, id, domain.ChannelParticipantsFilter{Kind: kind}, 0, 100)
		if err != nil {
			t.Fatal(err)
		}
		for _, p := range page.Participants {
			if p.UserID == target {
				return p
			}
		}
		t.Fatalf("%s participants = %v, want %d listed", kind, participantIDs(page.Participants), target)
		return domain.ChannelMember{}
	}
	restrict := func(actor, target int64, rights domain.ChannelBannedRights, date int) domain.ChannelMember {
		t.Helper()
		res, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: actor, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: date})
		if err != nil {
			t.Fatal(err)
		}
		return res.Participant
	}
	expect := func(step string, got domain.ChannelMember, inviter, by int64, at int) {
		t.Helper()
		if got.InviterUserID != inviter || got.RestrictedBy != by || got.RestrictedAt != at {
			t.Fatalf("%s: inviter=%d restricted_by=%d restricted_at=%d, want inviter=%d restricted_by=%d restricted_at=%d",
				step, got.InviterUserID, got.RestrictedBy, got.RestrictedAt, inviter, by, at)
		}
	}

	inviterM, inviterK := participant(m).InviterUserID, participant(k).InviterUserID
	if inviterM == a || inviterK == a {
		t.Fatalf("fixture: members must not be invited by the restricting admin (inviters %d, %d)", inviterM, inviterK)
	}
	mute := domain.ChannelBannedRights{SendMessages: true, SendMedia: true}

	// 1. 管理员 A 禁言在群成员：执行人与时间写入，邀请人不变。
	mutedAt := now + 10
	expect("mute result", restrict(a, m, mute, mutedAt), inviterM, a, mutedAt)
	expect("mute participant", participant(m), inviterM, a, mutedAt)
	expect("mute listed", listed(domain.ChannelParticipantsBanned, m), inviterM, a, mutedAt)

	// 2. 管理员 A 踢出在群成员：同样写入，邀请人不变。
	kickedAt := now + 20
	expect("kick result", restrict(a, k, domain.ChannelBannedRights{ViewMessages: true}, kickedAt), inviterK, a, kickedAt)
	expect("kick participant", participant(k), inviterK, a, kickedAt)
	expect("kick listed", listed(domain.ChannelParticipantsKicked, k), inviterK, a, kickedAt)

	// 3. 退群后自行加入：限制随人保留，执行人和时间一起保留。
	if _, err := s.LeaveChannel(ctx, id, m, now+30); err != nil {
		t.Fatal(err)
	}
	if _, err := s.JoinChannel(ctx, id, m, now+40); err != nil {
		t.Fatal(err)
	}
	if got := participant(m); got.RestrictedBy != a || got.RestrictedAt != mutedAt || got.BannedRights != mute {
		t.Fatalf("self rejoin: %+v, want restriction %+v by %d at %d kept", got, mute, a, mutedAt)
	}

	// 4. 退群后被群主拉回：同样保留。
	if _, err := s.LeaveChannel(ctx, id, m, now+50); err != nil {
		t.Fatal(err)
	}
	if _, err := s.InviteToChannel(ctx, id, owner, []int64{m}, now+60); err != nil {
		t.Fatal(err)
	}
	if got := participant(m); got.RestrictedBy != a || got.RestrictedAt != mutedAt || got.BannedRights != mute {
		t.Fatalf("invite back: %+v, want restriction %+v by %d at %d kept", got, mute, a, mutedAt)
	}

	// 5. 群主解除限制：两项清零，邀请人不变。
	inviterM = participant(m).InviterUserID
	expect("lift result", restrict(owner, m, domain.ChannelBannedRights{}, now+70), inviterM, 0, 0)
	expect("lift participant", participant(m), inviterM, 0, 0)
	expect("unkick result", restrict(owner, k, domain.ChannelBannedRights{}, now+80), inviterK, 0, 0)

	// 6. 被踢出者被拉回等同解封：不带旧的执行人和时间。
	restrict(a, k, domain.ChannelBannedRights{ViewMessages: true}, now+90)
	if _, err := s.InviteToChannel(ctx, id, owner, []int64{k}, now+100); err != nil {
		t.Fatal(err)
	}
	if got := participant(k); got.RestrictedBy != 0 || got.RestrictedAt != 0 || !got.BannedRights.Empty() {
		t.Fatalf("kicked user invited back: %+v, want no restriction, actor or date", got)
	}
}

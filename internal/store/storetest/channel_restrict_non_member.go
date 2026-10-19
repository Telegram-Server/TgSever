package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelRestrictNonMember 校验对不在群里的用户设置部分限制（没有 view_messages）后：记录表达
// 为"不在群里 + 带着限制"（官方 channelParticipantBanned left=true），不阻止他入群——可以自行
// 加入、可以被只有邀请权的管理员拉回、可以经邀请链接加入，入群后限制保留；只有 view_messages
// 踢出才阻止入群。users：群主、只有邀请权的管理员 A、从未入群的 N、退过群的 L、被踢的 K。
func ChannelRestrictNonMember(t *testing.T, s store.ChannelStore, users [5]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, n, l, k := users[0], users[1], users[2], users[3], users[4]
	now := baseNow()
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Restrict non-member", Megagroup: true, MemberUserIDs: []int64{a, l, k}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: owner, ChannelID: id, MemberID: a, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: now}); err != nil {
		t.Fatal(err)
	}
	restrict := func(target int64, rights domain.ChannelBannedRights) domain.EditChannelBannedResult {
		t.Helper()
		res, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: now})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	participant := func(target int64) domain.ChannelMember {
		t.Helper()
		m, err := s.GetParticipant(ctx, owner, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	expectJoined := func(step string, res domain.CreateChannelResult, target int64, want domain.ChannelBannedRights) {
		t.Helper()
		for _, m := range res.Members {
			if m.UserID == target && (m.Status != domain.ChannelMemberActive || m.BannedRights != want) {
				t.Fatalf("%s: result member = %+v, want active with %+v", step, m, want)
			}
		}
		if m := participant(target); m.Status != domain.ChannelMemberActive || m.BannedRights != want {
			t.Fatalf("%s: participant = %+v, want active with %+v", step, m, want)
		}
	}

	limited := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: now + 3600}

	// 1. 限制从未入群的人：不在群里但带着限制。
	res := restrict(n, limited)
	if res.Participant.Status == domain.ChannelMemberActive || res.Participant.Status == domain.ChannelMemberKicked || res.Participant.BannedRights != limited {
		t.Fatalf("restricted non-member = %+v, want not in the channel and restricted with %+v", res.Participant, limited)
	}
	if m := participant(n); m.Status == domain.ChannelMemberActive || m.Status == domain.ChannelMemberKicked || m.BannedRights != limited {
		t.Fatalf("restricted non-member participant = %+v, want not in the channel and restricted", m)
	}
	banned, err := s.GetParticipants(ctx, owner, id, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsBanned}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := participantIDs(banned.Participants); !sameIDs(got, []int64{n}) {
		t.Fatalf("banned participants = %v, want %v", got, []int64{n})
	}

	// 2. 他可以自行加入，限制保留。
	joined, err := s.JoinChannel(ctx, id, n, now)
	if err != nil {
		t.Fatalf("restricted non-member join = %v, want allowed", err)
	}
	expectJoined("self join", joined, n, limited)

	// 3. 退群后被限制的人可以被只有邀请权的管理员拉回，限制保留。
	if _, err := s.LeaveChannel(ctx, id, l, now); err != nil {
		t.Fatal(err)
	}
	restrict(l, limited)
	invited, err := s.InviteToChannel(ctx, id, a, []int64{l}, now)
	if err != nil {
		t.Fatalf("invite restricted non-member without ban right = %v, want allowed", err)
	}
	expectJoined("invite", invited, l, limited)

	// 4. 经邀请链接加入同样允许，限制保留。
	if _, err := s.LeaveChannel(ctx, id, l, now); err != nil {
		t.Fatal(err)
	}
	restrict(l, limited)
	link, err := s.ExportInvite(ctx, domain.ExportChannelInviteRequest{UserID: owner, ChannelID: id, Title: "restricted", Date: now})
	if err != nil {
		t.Fatal(err)
	}
	if check, err := s.CheckInvite(ctx, l, link.Invite.Hash, now); err != nil || check.Already {
		t.Fatalf("check invite for restricted non-member = %+v, %v; want joinable", check, err)
	}
	imported, err := s.ImportInvite(ctx, domain.ImportChannelInviteRequest{UserID: l, Hash: link.Invite.Hash, Date: now})
	if err != nil {
		t.Fatalf("import invite as restricted non-member = %v, want allowed", err)
	}
	expectJoined("import invite", imported, l, limited)

	// 5. view_messages 踢出仍阻止入群。
	restrict(k, domain.ChannelBannedRights{ViewMessages: true, SendMessages: true})
	if _, err := s.JoinChannel(ctx, id, k, now); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("kicked join = %v, want ErrChannelUserBanned", err)
	}
	if _, err := s.InviteToChannel(ctx, id, a, []int64{k}, now); !errors.Is(err, domain.ErrUserKicked) {
		t.Fatalf("kicked invite without ban right = %v, want ErrUserKicked", err)
	}
}

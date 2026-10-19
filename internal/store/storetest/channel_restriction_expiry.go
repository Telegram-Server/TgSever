package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelRestrictionExpiry 校验限制期限（UntilDate）在读取时生效：到期的部分限制读为无限制，
// 未到期与永久（UntilDate == 0）的限制保持。期限相对真实时钟：设置请求的 Date 放在过去，
// 使"设置时有效、现在已过期"的期限能通过官方 30 秒/366 天规则。
// users：群主、成员 R、成员 P、成员 Q。
func ChannelRestrictionExpiry(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, r, p, q := users[0], users[1], users[2], users[3]
	now := baseNow()
	past := now - 7200
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Restriction expiry", Megagroup: true, MemberUserIDs: []int64{r, p, q}, Date: past})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	restrict := func(target int64, rights domain.ChannelBannedRights, date int) {
		t.Helper()
		if _, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: date}); err != nil {
			t.Fatal(err)
		}
	}
	participant := func(target int64) domain.ChannelMember {
		t.Helper()
		m, err := s.GetParticipant(ctx, owner, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}

	expired := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: now - 3600}
	pending := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: now + 3600}
	forever := domain.ChannelBannedRights{SendMessages: true, SendMedia: true}
	restrict(r, expired, past)
	restrict(p, pending, now)
	restrict(q, forever, now)

	// 1. 到期的限制读为无限制，未到期与永久的限制保持。
	if m := participant(r); m.Status != domain.ChannelMemberActive || m.BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("expired restriction participant = %+v, want active and unrestricted", m)
	}
	if m := participant(p); m.Status != domain.ChannelMemberActive || m.BannedRights != pending {
		t.Fatalf("pending restriction participant = %+v, want active with %+v", m, pending)
	}
	if m := participant(q); m.Status != domain.ChannelMemberActive || m.BannedRights != forever {
		t.Fatalf("permanent restriction participant = %+v, want active with %+v", m, forever)
	}

	// 2. 受限名单只列仍生效的限制。
	banned, err := s.GetParticipants(ctx, owner, id, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsBanned}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := participantIDs(banned.Participants); !sameIDs(got, []int64{p, q}) {
		t.Fatalf("banned participants = %v, want %v", got, []int64{p, q})
	}

	// 3. 成员自己看到的 Self 也是生效值。
	view, err := s.GetChannel(ctx, r, id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Self.BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("expired self rights = %+v, want none", view.Self.BannedRights)
	}
	view, err = s.GetChannel(ctx, p, id)
	if err != nil {
		t.Fatal(err)
	}
	if view.Self.BannedRights != pending {
		t.Fatalf("pending self rights = %+v, want %+v", view.Self.BannedRights, pending)
	}

	// 4. 发消息：到期者可发，未到期者仍被拒。
	send := func(user int64, randomID int64) error {
		t.Helper()
		_, err := s.SendChannelMessage(ctx, domain.SendChannelMessageRequest{UserID: user, ChannelID: id, RandomID: randomID, Message: "hello", Date: now})
		return err
	}
	if err := send(r, 9_100_001); err != nil {
		t.Fatalf("send after expiry = %v, want allowed", err)
	}
	if err := send(p, 9_100_002); !errors.Is(err, domain.ErrChannelWriteForbidden) {
		t.Fatalf("send before expiry = %v, want ErrChannelWriteForbidden", err)
	}

	// 5. 限时踢出：到期后视为已解封（left，可自行加入且不带限制）；未到期与永久踢出仍阻止加入。
	restrict(r, domain.ChannelBannedRights{ViewMessages: true, SendMessages: true, UntilDate: now - 3600}, past)
	restrict(p, domain.ChannelBannedRights{ViewMessages: true, SendMessages: true, UntilDate: now + 3600}, now)
	restrict(q, domain.ChannelBannedRights{ViewMessages: true, SendMessages: true}, now)
	kicked, err := s.GetParticipants(ctx, owner, id, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, 0, 100)
	if err != nil {
		t.Fatal(err)
	}
	if got := participantIDs(kicked.Participants); !sameIDs(got, []int64{p, q}) {
		t.Fatalf("kicked participants = %v, want %v", got, []int64{p, q})
	}
	if m := participant(r); m.Status != domain.ChannelMemberLeft || m.BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("expired kick participant = %+v, want left and unrestricted", m)
	}
	joined, err := s.JoinChannel(ctx, id, r, now)
	if err != nil {
		t.Fatalf("join after kick expiry = %v", err)
	}
	if len(joined.Members) != 1 || joined.Members[0].Status != domain.ChannelMemberActive || joined.Members[0].BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("join after kick expiry members = %+v, want active and unrestricted", joined.Members)
	}
	if m := participant(r); m.Status != domain.ChannelMemberActive || m.BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("rejoined participant = %+v, want active and unrestricted", m)
	}
	if _, err := s.JoinChannel(ctx, id, p, now); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("join before kick expiry = %v, want ErrChannelUserBanned", err)
	}
	if _, err := s.JoinChannel(ctx, id, q, now); !errors.Is(err, domain.ErrChannelUserBanned) {
		t.Fatalf("join with permanent kick = %v, want ErrChannelUserBanned", err)
	}

	// 6. 改限制时的 previous 也是生效值：到期的踢出之前就已经是 left。
	restrict(r, domain.ChannelBannedRights{ViewMessages: true, UntilDate: now - 3600}, past)
	lifted, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: owner, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: r}, Date: now})
	if err != nil {
		t.Fatal(err)
	}
	if lifted.Previous.Status != domain.ChannelMemberLeft || lifted.Previous.BannedRights != (domain.ChannelBannedRights{}) {
		t.Fatalf("previous of expired kick = %+v, want left and unrestricted", lifted.Previous)
	}
}

func participantIDs(members []domain.ChannelMember) []int64 {
	out := make([]int64, 0, len(members))
	for _, m := range members {
		out = append(out, m.UserID)
	}
	return out
}

func sameIDs(got, want []int64) bool {
	if len(got) != len(want) {
		return false
	}
	seen := make(map[int64]int, len(want))
	for _, id := range want {
		seen[id]++
	}
	for _, id := range got {
		if seen[id] == 0 {
			return false
		}
		seen[id]--
	}
	return true
}

package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// rejoinFixture 是一个新建的超级群：群主、只有邀请权的管理员 A、被限制成员 R、普通成员 N。
type rejoinFixture struct {
	t                 *testing.T
	s                 store.ChannelStore
	ctx               context.Context
	id                int64
	owner, a, r, n    int64
	requestInviteHash string
}

func newRejoinFixture(t *testing.T, s store.ChannelStore, users [4]int64) *rejoinFixture {
	t.Helper()
	ctx := context.Background()
	f := &rejoinFixture{t: t, s: s, ctx: ctx, owner: users[0], a: users[1], r: users[2], n: users[3]}
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: f.owner, Title: "Rejoin restrictions", Megagroup: true, MemberUserIDs: []int64{f.a, f.r, f.n}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	f.id = created.Channel.ID
	if _, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: f.owner, ChannelID: f.id, MemberID: f.a, AdminRights: domain.ChannelAdminRights{InviteUsers: true}, Date: baseNow()}); err != nil {
		t.Fatal(err)
	}
	return f
}

func (f *rejoinFixture) restrict(target int64, rights domain.ChannelBannedRights) {
	f.t.Helper()
	if _, err := f.s.EditChannelBanned(f.ctx, domain.EditChannelBannedRequest{UserID: f.owner, ChannelID: f.id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: baseNow()}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *rejoinFixture) leave(target int64) {
	f.t.Helper()
	if _, err := f.s.LeaveChannel(f.ctx, f.id, target, baseNow()); err != nil {
		f.t.Fatal(err)
	}
}

func (f *rejoinFixture) export(requestNeeded bool) string {
	f.t.Helper()
	res, err := f.s.ExportInvite(f.ctx, domain.ExportChannelInviteRequest{UserID: f.owner, ChannelID: f.id, RequestNeeded: requestNeeded, Title: "rejoin", Date: baseNow()})
	if err != nil {
		f.t.Fatal(err)
	}
	return res.Invite.Hash
}

// request 让 target 经需审批的邀请链接提交入群申请。
func (f *rejoinFixture) request(target int64) {
	f.t.Helper()
	if f.requestInviteHash == "" {
		f.requestInviteHash = f.export(true)
	}
	if _, err := f.s.ImportInvite(f.ctx, domain.ImportChannelInviteRequest{UserID: target, Hash: f.requestInviteHash, Date: baseNow()}); !errors.Is(err, domain.ErrInviteRequestSent) {
		f.t.Fatalf("join request of %d = %v", target, err)
	}
}

func (f *rejoinFixture) expectRights(step string, res domain.CreateChannelResult, target int64, want domain.ChannelBannedRights) {
	f.t.Helper()
	found := false
	for _, m := range res.Members {
		if m.UserID != target {
			continue
		}
		found = true
		if m.BannedRights != want {
			f.t.Fatalf("%s: result member rights = %+v, want %+v", step, m.BannedRights, want)
		}
	}
	if !found {
		f.t.Fatalf("%s: %d missing from result members %+v", step, target, res.Members)
	}
	m, err := f.s.GetParticipant(f.ctx, f.owner, f.id, target)
	if err != nil {
		f.t.Fatal(err)
	}
	if m.Status != domain.ChannelMemberActive || m.BannedRights != want {
		f.t.Fatalf("%s: stored participant = %+v, want active with %+v", step, m, want)
	}
}

// ChannelRejoinKeepsRestrictions 校验被限制（有部分禁止位、没有 view_messages 禁令）的成员
// 退群后无论经哪条路径重新入群，未到期的限制与期限都保持不变，已到期的限制被清除。
// 到期/未到期/永久的换算本身由 domain.TestChannelMemberEffectiveAt 覆盖；这里只证明每条
// 入群路径都按入群时刻套用了它。被踢者不能自行加入、无封禁权拉不回也只在这里断言。
// users：群主、只有邀请权的管理员 A、被限制成员 R、普通成员 N。每个子测试新建自己的群。
func ChannelRejoinKeepsRestrictions(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	type rejoinPath struct {
		name string
		// rejoin 让 f.r 在 date 时刻经此路径重新入群（R 已退群）。
		rejoin func(f *rejoinFixture, date int) (domain.CreateChannelResult, error)
	}
	paths := []rejoinPath{
		{"invite", func(f *rejoinFixture, date int) (domain.CreateChannelResult, error) {
			return f.s.InviteToChannel(f.ctx, f.id, f.a, []int64{f.r}, date)
		}},
		{"approve", func(f *rejoinFixture, date int) (domain.CreateChannelResult, error) {
			f.request(f.r)
			return f.s.HideChatJoinRequest(f.ctx, domain.HideChannelJoinRequestRequest{UserID: f.owner, ChannelID: f.id, TargetUserID: f.r, Approved: true, Date: date})
		}},
		{"approveAll", func(f *rejoinFixture, date int) (domain.CreateChannelResult, error) {
			f.request(f.r)
			return f.s.HideAllChatJoinRequests(f.ctx, domain.HideChannelJoinRequestsRequest{UserID: f.owner, ChannelID: f.id, Approved: true, Limit: 10, Date: date})
		}},
		{"import", func(f *rejoinFixture, date int) (domain.CreateChannelResult, error) {
			return f.s.ImportInvite(f.ctx, domain.ImportChannelInviteRequest{UserID: f.r, Hash: f.export(false), Date: date})
		}},
		{"join", func(f *rejoinFixture, date int) (domain.CreateChannelResult, error) {
			return f.s.JoinChannel(f.ctx, f.id, f.r, date)
		}},
	}
	for _, p := range paths {
		t.Run(p.name+"/pending kept", func(t *testing.T) {
			f := newRejoinFixture(t, s, users)
			limited := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: baseNow() + 3600}
			f.restrict(f.r, limited)
			f.leave(f.r)
			res, err := p.rejoin(f, baseNow())
			if err != nil {
				t.Fatal(err)
			}
			f.expectRights(p.name, res, f.r, limited)
		})
		t.Run(p.name+"/expired cleared", func(t *testing.T) {
			f := newRejoinFixture(t, s, users)
			f.restrict(f.r, domain.ChannelBannedRights{SendMessages: true, UntilDate: baseNow() + 60})
			f.leave(f.r)
			res, err := p.rejoin(f, baseNow()+120)
			if err != nil {
				t.Fatal(err)
			}
			f.expectRights(p.name+" after expiry", res, f.r, domain.ChannelBannedRights{})
		})
	}

	t.Run("never restricted stays unrestricted", func(t *testing.T) {
		f := newRejoinFixture(t, s, users)
		f.leave(f.n)
		invited, err := s.InviteToChannel(f.ctx, f.id, f.a, []int64{f.n}, baseNow())
		if err != nil {
			t.Fatal(err)
		}
		f.expectRights("invite unrestricted", invited, f.n, domain.ChannelBannedRights{})
	})

	// 被踢出者：不能自行加入，没有封禁权的管理员拉不回，有封禁权的管理员拉回等同解封。
	t.Run("kicked", func(t *testing.T) {
		f := newRejoinFixture(t, s, users)
		f.restrict(f.n, domain.ChannelBannedRights{ViewMessages: true, SendMessages: true})
		if _, err := s.JoinChannel(f.ctx, f.id, f.n, baseNow()); !errors.Is(err, domain.ErrChannelUserBanned) {
			t.Fatalf("kicked join = %v", err)
		}
		if _, err := s.InviteToChannel(f.ctx, f.id, f.a, []int64{f.n}, baseNow()); !errors.Is(err, domain.ErrUserKicked) {
			t.Fatalf("kicked invite without ban right = %v", err)
		}
		invited, err := s.InviteToChannel(f.ctx, f.id, f.owner, []int64{f.n}, baseNow())
		if err != nil {
			t.Fatal(err)
		}
		f.expectRights("invite kicked by owner", invited, f.n, domain.ChannelBannedRights{})
	})

	t.Run("batch invite keeps each member's rights", func(t *testing.T) {
		f := newRejoinFixture(t, s, users)
		limited := domain.ChannelBannedRights{SendMessages: true, SendMedia: true, UntilDate: baseNow() + 3600}
		f.restrict(f.r, limited)
		f.leave(f.r)
		f.leave(f.n)
		invited, err := s.InviteToChannel(f.ctx, f.id, f.a, []int64{f.r, f.n}, baseNow())
		if err != nil {
			t.Fatal(err)
		}
		f.expectRights("batch invite restricted", invited, f.r, limited)
		f.expectRights("batch invite unrestricted", invited, f.n, domain.ChannelBannedRights{})
	})
}

package channels

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/app/readmodel"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// senderChatBanFixture：群主 O、有封禁权的 bot 管理员 B、没有封禁权的管理员 I、普通成员 M、
// 被 O 踢出的用户 K，以及成员 M 名下的两个广播频道。
type senderChatBanFixture struct {
	t                               *testing.T
	ctx                             context.Context
	svc                             *Service
	group                           int64
	alpha, beta                     int64
	owner, bot, plain, member, kick int64
}

const senderChatBanDate = 1_700_100_000

func newSenderChatBanFixture(t *testing.T, opts ...Option) *senderChatBanFixture {
	t.Helper()
	f := &senderChatBanFixture{t: t, ctx: context.Background(), owner: 5001, bot: 5002, plain: 5003, member: 5004, kick: 5005}
	f.svc = NewService(memory.NewChannelStore(), opts...)
	created, err := f.svc.CreateChannel(f.ctx, f.owner, domain.CreateChannelRequest{Title: "Sender chat bans", Megagroup: true, MemberUserIDs: []int64{f.bot, f.plain, f.member, f.kick}, Date: senderChatBanDate})
	if err != nil {
		t.Fatal(err)
	}
	f.group = created.Channel.ID
	for id, rights := range map[int64]domain.ChannelAdminRights{f.bot: {BanUsers: true}, f.plain: {InviteUsers: true}} {
		if _, err := f.svc.EditAdmin(f.ctx, f.owner, domain.EditChannelAdminRequest{ChannelID: f.group, MemberID: id, AdminRights: rights, Date: senderChatBanDate}); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := f.svc.EditBanned(f.ctx, f.owner, domain.EditChannelBannedRequest{ChannelID: f.group, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: f.kick}, BannedRights: domain.ChannelBannedRights{ViewMessages: true}, Date: senderChatBanDate}); err != nil {
		t.Fatal(err)
	}
	f.alpha = f.broadcast("Alpha")
	f.beta = f.broadcast("Beta")
	return f
}

func (f *senderChatBanFixture) broadcast(title string) int64 {
	f.t.Helper()
	res, err := f.svc.CreateChannel(f.ctx, f.member, domain.CreateChannelRequest{Title: title, Broadcast: true, Date: senderChatBanDate})
	if err != nil {
		f.t.Fatal(err)
	}
	return res.Channel.ID
}

func (f *senderChatBanFixture) edit(actor, target int64, rights domain.ChannelBannedRights) (domain.EditChannelBannedSenderChatResult, error) {
	return f.svc.EditBannedSenderChat(f.ctx, actor, domain.EditChannelBannedSenderChatRequest{ChannelID: f.group, SenderChatID: target, BannedRights: rights, Date: senderChatBanDate + 1})
}

func (f *senderChatBanFixture) ban(target int64) {
	f.t.Helper()
	if _, err := f.edit(f.owner, target, domain.ChannelBannedRights{ViewMessages: true}); err != nil {
		f.t.Fatal(err)
	}
}

func (f *senderChatBanFixture) banned(target int64) bool {
	f.t.Helper()
	ok, err := f.svc.IsSenderChatBanned(f.ctx, f.group, target)
	if err != nil {
		f.t.Fatal(err)
	}
	return ok
}

func (f *senderChatBanFixture) kicked(offset, limit int) domain.ChannelParticipantList {
	f.t.Helper()
	list, err := f.svc.GetParticipants(f.ctx, f.owner, f.group, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, offset, limit)
	if err != nil {
		f.t.Fatal(err)
	}
	return list
}

func TestEditBannedSenderChatPermissionMatrix(t *testing.T) {
	full := domain.ChannelBannedRights{ViewMessages: true}
	cases := []struct {
		name    string
		actor   func(*senderChatBanFixture) int64
		wantErr error
	}{
		{"群主", func(f *senderChatBanFixture) int64 { return f.owner }, nil},
		{"有封禁权的 bot 管理员", func(f *senderChatBanFixture) int64 { return f.bot }, nil},
		{"没有封禁权的管理员", func(f *senderChatBanFixture) int64 { return f.plain }, domain.ErrChannelAdminRequired},
		{"普通成员", func(f *senderChatBanFixture) int64 { return f.member }, domain.ErrChannelAdminRequired},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSenderChatBanFixture(t)
			actor := tc.actor(f)
			_, err := f.edit(actor, f.alpha, full)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("ban err = %v, want %v", err, tc.wantErr)
			}
			if got := f.banned(f.alpha); got != (tc.wantErr == nil) {
				t.Fatalf("banned after ban = %v", got)
			}
			if tc.wantErr != nil {
				return
			}
			f.ban(f.alpha) // 让解封有东西可解，再由同一执行者解封
			if _, err := f.edit(actor, f.alpha, domain.ChannelBannedRights{}); err != nil {
				t.Fatalf("unban: %v", err)
			}
			if f.banned(f.alpha) {
				t.Fatal("still banned after unban")
			}
		})
	}
	t.Run("没有封禁权的人也不能解封", func(t *testing.T) {
		f := newSenderChatBanFixture(t)
		f.ban(f.alpha)
		if _, err := f.edit(f.plain, f.alpha, domain.ChannelBannedRights{}); !errors.Is(err, domain.ErrChannelAdminRequired) {
			t.Fatalf("unban err = %v, want ErrChannelAdminRequired", err)
		}
		if !f.banned(f.alpha) {
			t.Fatal("rejected unban removed the ban")
		}
	})
}

func TestEditBannedSenderChatAcceptsOnlyFullBanAndUnban(t *testing.T) {
	cases := []struct {
		name       string
		rights     domain.ChannelBannedRights
		wantBanned bool
		wantErr    error
	}{
		{"view_messages 全封", domain.ChannelBannedRights{ViewMessages: true}, true, nil},
		{"全封带其余位和期限", domain.ChannelBannedRights{ViewMessages: true, SendMessages: true, SendMedia: true, UntilDate: 2147483647}, true, nil},
		{"全空解封", domain.ChannelBannedRights{}, false, nil},
		{"只带期限也算解封", domain.ChannelBannedRights{UntilDate: 1}, false, nil},
		{"只禁发消息", domain.ChannelBannedRights{SendMessages: true}, false, domain.ErrChannelBannedRightsInvalid},
		{"只禁媒体", domain.ChannelBannedRights{SendMedia: true, UntilDate: 2147483647}, false, domain.ErrChannelBannedRightsInvalid},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newSenderChatBanFixture(t)
			res, err := f.edit(f.owner, f.alpha, tc.rights)
			if !errors.Is(err, tc.wantErr) {
				t.Fatalf("err = %v, want %v", err, tc.wantErr)
			}
			if err == nil && res.Banned != tc.wantBanned {
				t.Fatalf("banned = %v, want %v", res.Banned, tc.wantBanned)
			}
			if f.banned(f.alpha) != tc.wantBanned {
				t.Fatalf("IsSenderChatBanned = %v, want %v", !tc.wantBanned, tc.wantBanned)
			}
		})
	}
}

func TestEditBannedSenderChatRejectsInvalidTarget(t *testing.T) {
	f := newSenderChatBanFixture(t)
	for _, target := range []int64{f.group, f.alpha + 999_999} {
		if _, err := f.edit(f.owner, target, domain.ChannelBannedRights{ViewMessages: true}); !errors.Is(err, domain.ErrChannelSenderChatInvalid) {
			t.Fatalf("ban %d err = %v, want ErrChannelSenderChatInvalid", target, err)
		}
	}
}

func TestEditBannedSenderChatIsIdempotentAndLogged(t *testing.T) {
	f := newSenderChatBanFixture(t)
	senderChatLog := func() []domain.ChannelAdminLogEvent {
		t.Helper()
		res, err := f.svc.ListAdminLog(f.ctx, f.owner, domain.ChannelAdminLogRequest{ChannelID: f.group, Limit: 100})
		if err != nil {
			t.Fatal(err)
		}
		var out []domain.ChannelAdminLogEvent
		for _, event := range res.Events {
			if event.NewParticipant != nil && event.NewParticipant.SenderChatID == f.alpha {
				out = append(out, event)
			}
		}
		if len(out) > 0 && (len(res.SenderChats) == 0 || res.SenderChats[0].ID != f.alpha || res.SenderChats[0].Title != "Alpha") {
			t.Fatalf("admin log sender chats = %+v, want Alpha", res.SenderChats)
		}
		return out
	}
	first, err := f.edit(f.bot, f.alpha, domain.ChannelBannedRights{ViewMessages: true})
	if err != nil || !first.Changed {
		t.Fatalf("first ban = %+v, %v", first, err)
	}
	again, err := f.edit(f.owner, f.alpha, domain.ChannelBannedRights{ViewMessages: true})
	if err != nil || again.Changed || again.Ban.BannedByUserID != f.bot {
		t.Fatalf("repeat ban = %+v, %v; want unchanged and still credited to the bot", again, err)
	}
	if events := senderChatLog(); len(events) != 1 || events[0].Type != domain.ChannelAdminLogParticipantKick || events[0].UserID != f.bot {
		t.Fatalf("log after bans = %+v, want one kick by the bot", events)
	}
	if res, err := f.edit(f.owner, f.alpha, domain.ChannelBannedRights{}); err != nil || !res.Changed {
		t.Fatalf("unban = %+v, %v", res, err)
	}
	if res, err := f.edit(f.owner, f.alpha, domain.ChannelBannedRights{}); err != nil || res.Changed {
		t.Fatalf("repeat unban = %+v, %v; want unchanged", res, err)
	}
	events := senderChatLog()
	if len(events) != 2 || events[0].Type != domain.ChannelAdminLogParticipantUnkick || events[0].UserID != f.owner {
		t.Fatalf("log after unbans = %+v, want kick + one unkick", events)
	}
}

func TestKickedParticipantsListBannedSenderChatsAfterUsers(t *testing.T) {
	f := newSenderChatBanFixture(t)
	f.ban(f.alpha)
	f.ban(f.beta)
	lo, hi := f.alpha, f.beta
	if lo > hi {
		lo, hi = hi, lo
	}
	all := f.kicked(0, 10)
	if all.Count != 3 || len(all.Participants) != 3 {
		t.Fatalf("kicked = %+v, want kicked user + two channels", all)
	}
	if p := all.Participants[0]; p.UserID != f.kick || p.SenderChatID != 0 {
		t.Fatalf("first kicked = %+v, want the kicked user", p)
	}
	for i, want := range []int64{lo, hi} {
		p := all.Participants[i+1]
		if p.SenderChatID != want || p.UserID != 0 || p.Status != domain.ChannelMemberKicked || !p.BannedRights.ViewMessages || p.InviterUserID != f.owner || p.LeftAt != senderChatBanDate+1 {
			t.Fatalf("kicked[%d] = %+v, want banned channel %d", i+1, p, want)
		}
	}
	if len(all.SenderChats) != 2 {
		t.Fatalf("sender chats = %+v, want both channels", all.SenderChats)
	}

	// 分页跨过用户与频道的边界：offset/limit 作用在"用户在前、频道在后"的合并序列上。
	page := f.kicked(1, 1)
	if page.Count != 3 || len(page.Participants) != 1 || page.Participants[0].SenderChatID != lo || len(page.SenderChats) != 1 || page.SenderChats[0].ID != lo {
		t.Fatalf("page(1,1) = %+v, want only %d", page, lo)
	}
	page = f.kicked(0, 2)
	if len(page.Participants) != 2 || page.Participants[0].UserID != f.kick || page.Participants[1].SenderChatID != lo {
		t.Fatalf("page(0,2) = %+v", page.Participants)
	}
	if tail := f.kicked(3, 5); tail.Count != 3 || len(tail.Participants) != 0 {
		t.Fatalf("page(3,5) = %+v, want empty page with full count", tail)
	}

	search, err := f.svc.GetParticipants(f.ctx, f.owner, f.group, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked, Query: "beta"}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(search.Participants) != 1 || search.Participants[0].SenderChatID != f.beta {
		t.Fatalf("search beta = %+v", search.Participants)
	}

	if _, err := f.edit(f.owner, f.alpha, domain.ChannelBannedRights{}); err != nil {
		t.Fatal(err)
	}
	after := f.kicked(0, 10)
	if after.Count != 2 || len(after.Participants) != 2 || after.Participants[1].SenderChatID != f.beta {
		t.Fatalf("kicked after unban = %+v, want user + beta", after.Participants)
	}

	nonAdmin, err := f.svc.GetParticipants(f.ctx, f.member, f.group, domain.ChannelParticipantsFilter{Kind: domain.ChannelParticipantsKicked}, 0, 10)
	if err != nil {
		t.Fatal(err)
	}
	if len(nonAdmin.Participants) != 0 {
		t.Fatalf("non-admin kicked = %+v, want empty", nonAdmin.Participants)
	}
}

func TestGetSenderChatParticipant(t *testing.T) {
	f := newSenderChatBanFixture(t)
	if _, _, err := f.svc.GetSenderChatParticipant(f.ctx, f.owner, f.group, f.alpha); !errors.Is(err, domain.ErrUserNotParticipant) {
		t.Fatalf("not banned err = %v, want ErrUserNotParticipant", err)
	}
	f.ban(f.alpha)
	member, channel, err := f.svc.GetSenderChatParticipant(f.ctx, f.owner, f.group, f.alpha)
	if err != nil {
		t.Fatal(err)
	}
	if member.SenderChatID != f.alpha || member.Status != domain.ChannelMemberKicked || !member.BannedRights.ViewMessages || member.InviterUserID != f.owner || channel.ID != f.alpha || channel.Title != "Alpha" {
		t.Fatalf("participant = %+v channel = %+v", member, channel)
	}
	if _, _, err := f.svc.GetSenderChatParticipant(f.ctx, f.member, f.group, f.alpha); !errors.Is(err, domain.ErrChannelAdminRequired) {
		t.Fatalf("non-admin err = %v, want ErrChannelAdminRequired", err)
	}
	if _, err := f.edit(f.owner, f.alpha, domain.ChannelBannedRights{}); err != nil {
		t.Fatal(err)
	}
	if _, _, err := f.svc.GetSenderChatParticipant(f.ctx, f.owner, f.group, f.alpha); !errors.Is(err, domain.ErrUserNotParticipant) {
		t.Fatalf("after unban err = %v, want ErrUserNotParticipant", err)
	}
}

func TestKickedParticipantsCacheSeesSenderChatBan(t *testing.T) {
	versions := &fakeReadModelVersions{hashes: map[store.ReadModelKey]int64{}}
	f := newSenderChatBanFixture(t, WithReadModelVersions(versions))
	peer := domain.Peer{Type: domain.PeerTypeChannel, ID: f.group}
	versions.hashes[store.ReadModelKey{Model: readmodel.ModelChannelBase, PeerType: peer.Type, PeerID: peer.ID}] = 301
	versions.hashes[store.ReadModelKey{Model: readmodel.ModelChannelParticipants, PeerType: peer.Type, PeerID: peer.ID}] = 302
	if before := f.kicked(0, 10); len(before.Participants) != 1 {
		t.Fatalf("kicked before = %+v", before.Participants)
	}
	f.ban(f.alpha)
	if after := f.kicked(0, 10); len(after.Participants) != 2 || after.Participants[1].SenderChatID != f.alpha {
		t.Fatalf("kicked after ban = %+v, want cached page invalidated", after.Participants)
	}
}

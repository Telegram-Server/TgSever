package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// CommunityBanAdminProtection 校验社区封禁沿用频道层的管理员保护：在任一关联频道里，
// 操作者既不是群主、也不是目标的提拔人，而目标是该频道在任管理员时，整次社区封禁被拒
// （ErrUserAdminInvalid），且任何频道、关联与社区成员状态都不变；群主与提拔人可以封禁，
// 普通成员照常封禁，解封不受限。返回建出的社区 ID 供调用方清理。
// users：群主 O、社区管理员 A、频道 Y 里由 O 提拔的管理员 T、由 A 提拔的管理员 P、成员 B。
func CommunityBanAdminProtection(t *testing.T, communities store.CommunityStore, channels store.ChannelStore, users [5]int64) int64 {
	t.Helper()
	ctx := context.Background()
	owner, a, target, p, b := users[0], users[1], users[2], users[3], users[4]
	date := baseNow()
	newChannel := func(creator int64, title string, members ...int64) int64 {
		t.Helper()
		res, err := channels.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: creator, Title: title, Megagroup: true, MemberUserIDs: members, Date: date})
		if err != nil {
			t.Fatal(err)
		}
		return res.Channel.ID
	}
	// X 先建，Y 后建：按 ID 顺序处理时，T 只是普通成员的 X 会先于 T 是管理员的 Y。
	x := newChannel(owner, "Community ban X", a, target, p, b)
	y := newChannel(owner, "Community ban Y", a, target, p, b)
	z := newChannel(target, "Community ban Z (T owned)")

	created, err := communities.CreateCommunity(ctx, domain.CreateCommunityRequest{CreatorUserID: owner, Title: "Community ban", InitialPeer: domain.Peer{Type: domain.PeerTypeChannel, ID: x}, Visibility: domain.CommunityPeerVisible, Date: date})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Community.ID
	if _, err := communities.ToggleCommunityPeerLink(ctx, domain.CommunityTogglePeerLinkRequest{ActorUserID: owner, CommunityID: id, Peer: domain.Peer{Type: domain.PeerTypeChannel, ID: y}, Visibility: domain.CommunityPeerVisible, Date: date}); err != nil {
		t.Fatalf("link Y: %v", err)
	}
	zPeer := domain.Peer{Type: domain.PeerTypeChannel, ID: z}
	if _, err := communities.ToggleCommunityPeerLink(ctx, domain.CommunityTogglePeerLinkRequest{ActorUserID: target, CommunityID: id, Peer: zPeer, Visibility: domain.CommunityPeerVisible, Date: date}); err != nil && !errors.Is(err, domain.ErrCommunityRequestCreated) {
		t.Fatalf("request Z link: %v", err)
	}
	if _, err := communities.DecideCommunityPeerLinkRequest(ctx, owner, id, zPeer, false, date); err != nil {
		t.Fatalf("approve Z link: %v", err)
	}
	if _, _, err := communities.EditCommunityAdmin(ctx, domain.CommunityEditAdminRequest{ActorUserID: owner, CommunityID: id, UserID: a, Rights: domain.ChannelAdminRights{BanUsers: true}, Date: date}); err != nil {
		t.Fatalf("make A community admin: %v", err)
	}
	promote := func(actor, member int64, rights domain.ChannelAdminRights) {
		t.Helper()
		if _, err := channels.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: actor, ChannelID: y, MemberID: member, AdminRights: rights, Date: date}); err != nil {
			t.Fatalf("promote %d by %d: %v", member, actor, err)
		}
	}
	promote(owner, a, domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, InviteUsers: true})
	promote(owner, target, domain.ChannelAdminRights{BanUsers: true})
	promote(a, p, domain.ChannelAdminRights{BanUsers: true})

	member := func(channelID, user int64) domain.ChannelMember {
		t.Helper()
		m, err := channels.GetParticipant(ctx, owner, channelID, user)
		if err != nil {
			t.Fatalf("participant %d in %d: %v", user, channelID, err)
		}
		return m
	}
	linked := func(peer domain.Peer) bool {
		t.Helper()
		view, err := communities.GetCommunity(ctx, owner, id)
		if err != nil {
			t.Fatal(err)
		}
		for _, l := range view.Links {
			if l.Peer == peer {
				return true
			}
		}
		return false
	}

	// 非群主、非提拔人：整次封禁被拒，什么都没改。
	if _, err := communities.ToggleCommunityParticipantBanned(ctx, a, id, target, false, date); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("non-promoter community ban of channel admin = %v, want ErrUserAdminInvalid", err)
	}
	if m := member(x, target); m.Status != domain.ChannelMemberActive || m.Role != domain.ChannelRoleMember {
		t.Fatalf("rejected ban touched X membership: %+v", m)
	}
	if m := member(y, target); m.Status != domain.ChannelMemberActive || m.Role != domain.ChannelRoleAdmin || m.InviterUserID != owner {
		t.Fatalf("rejected ban touched Y admin: %+v", m)
	}
	if !linked(zPeer) {
		t.Fatalf("rejected ban unlinked T's own channel")
	}
	if _, err := communities.GetCommunity(ctx, target, id); err != nil {
		t.Fatalf("rejected ban kicked T from the community: %v", err)
	}

	kicked := func(step string, user int64, channelIDs ...int64) {
		t.Helper()
		for _, channelID := range channelIDs {
			if m := member(channelID, user); m.Status != domain.ChannelMemberKicked || m.Role != domain.ChannelRoleMember {
				t.Fatalf("%s: %d in %d = %+v, want kicked member", step, user, channelID, m)
			}
		}
	}
	// 提拔人可以封禁自己提拔的管理员。
	if res, err := communities.ToggleCommunityParticipantBanned(ctx, a, id, p, false, date); err != nil || len(res.ChannelBans) != 2 {
		t.Fatalf("promoter community ban = %+v, %v, want two channel bans", res, err)
	}
	kicked("promoter ban", p, x, y)
	// 普通成员照常封禁。
	if res, err := communities.ToggleCommunityParticipantBanned(ctx, a, id, b, false, date); err != nil || len(res.ChannelBans) != 2 {
		t.Fatalf("member community ban = %+v, %v, want two channel bans", res, err)
	}
	kicked("member ban", b, x, y)
	// 群主可以封禁任何管理员；T 自建的 Z 解除关联。
	if res, err := communities.ToggleCommunityParticipantBanned(ctx, owner, id, target, false, date); err != nil || len(res.ChannelBans) != 2 || len(res.RemovedLinks) != 1 {
		t.Fatalf("owner community ban = %+v, %v, want two channel bans and Z unlinked", res, err)
	}
	kicked("owner ban", target, x, y)
	// 解封不受管理员保护限制。
	if res, err := communities.ToggleCommunityParticipantBanned(ctx, a, id, target, true, date); err != nil || !res.Changed {
		t.Fatalf("unban by community admin = %+v, %v, want changed", res, err)
	}
	return id
}

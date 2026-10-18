package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// ChannelAdminProtection 校验管理员保护规则：群主可编辑/封禁任何管理员；
// 非群主管理员只能编辑、降级、封禁或限制由自己提拔的管理员，其余返回 ErrUserAdminInvalid。
// users 至少 4 个：群主、管理员 A（可提拔、可封禁）、群主提拔的管理员 B、A 提拔的管理员 C。
func ChannelAdminProtection(t *testing.T, s store.ChannelStore, users [4]int64) {
	t.Helper()
	ctx := context.Background()
	owner, a, b, c := users[0], users[1], users[2], users[3]
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner, Title: "Admin protection", Megagroup: true, MemberUserIDs: []int64{a, b, c}, Date: baseNow()})
	if err != nil {
		t.Fatal(err)
	}
	id := created.Channel.ID
	promote := func(actor, target int64, rights domain.ChannelAdminRights) error {
		_, err := s.EditChannelAdmin(ctx, domain.EditChannelAdminRequest{UserID: actor, ChannelID: id, MemberID: target, AdminRights: rights, Date: baseNow()})
		return err
	}
	ban := func(actor, target int64, rights domain.ChannelBannedRights) error {
		_, err := s.EditChannelBanned(ctx, domain.EditChannelBannedRequest{UserID: actor, ChannelID: id, Participant: domain.Peer{Type: domain.PeerTypeUser, ID: target}, BannedRights: rights, Date: baseNow()})
		return err
	}
	full := domain.ChannelAdminRights{AddAdmins: true, BanUsers: true, DeleteMessages: true, PinMessages: true}
	if err := promote(owner, a, full); err != nil {
		t.Fatal(err)
	}
	if err := promote(owner, b, domain.ChannelAdminRights{BanUsers: true}); err != nil {
		t.Fatal(err)
	}
	if err := promote(a, c, domain.ChannelAdminRights{BanUsers: true}); err != nil {
		t.Fatal(err)
	}
	role := func(target int64) domain.ChannelMember {
		t.Helper()
		m, err := s.GetParticipant(ctx, owner, id, target)
		if err != nil {
			t.Fatal(err)
		}
		return m
	}
	// A 不能动群主提拔的 B：限制、踢出、改权限、撤销。
	if err := ban(a, b, domain.ChannelBannedRights{SendMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("restrict foreign admin = %v", err)
	}
	if err := ban(a, b, domain.ChannelBannedRights{ViewMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("kick foreign admin = %v", err)
	}
	if err := promote(a, b, domain.ChannelAdminRights{BanUsers: true, PinMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("edit foreign admin = %v", err)
	}
	if err := promote(a, b, domain.ChannelAdminRights{}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("demote foreign admin = %v", err)
	}
	if m := role(b); m.Role != domain.ChannelRoleAdmin || m.Status != domain.ChannelMemberActive || !m.AdminRights.BanUsers {
		t.Fatalf("foreign admin changed: %+v", m)
	}
	// B 也不能动 A（A 由群主提拔）。
	if err := ban(b, a, domain.ChannelBannedRights{SendMessages: true}); !errors.Is(err, domain.ErrUserAdminInvalid) {
		t.Fatalf("restrict peer admin = %v", err)
	}
	// A 可以编辑、限制自己提拔的 C。
	if err := promote(a, c, domain.ChannelAdminRights{BanUsers: true, PinMessages: true}); err != nil {
		t.Fatalf("edit own admin = %v", err)
	}
	if err := ban(a, c, domain.ChannelBannedRights{SendMessages: true}); err != nil {
		t.Fatalf("restrict own admin = %v", err)
	}
	if m := role(c); m.Role != domain.ChannelRoleMember || !m.BannedRights.SendMessages {
		t.Fatalf("own admin not restricted: %+v", m)
	}
	// 群主可以动任何管理员。
	if err := ban(owner, b, domain.ChannelBannedRights{ViewMessages: true}); err != nil {
		t.Fatalf("owner kick admin = %v", err)
	}
	if err := promote(owner, a, domain.ChannelAdminRights{}); err != nil {
		t.Fatalf("owner demote admin = %v", err)
	}
}

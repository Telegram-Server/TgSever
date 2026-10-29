package rpc

import (
	"context"
	"strings"
	"testing"

	"github.com/iamxvbaba/td/tg"

	"telesrv/internal/domain"
)

func TestAdminCreateGroupBypassesDeniedOwnerAndReachesOnlineSessions(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(1, "15550001001", "Company")
	sessions := &captureSessions{onlineUserIDs: []int64{owner.ID}}
	f.router.deps.Sessions = sessions
	f.router.deps.Permissions = permissionGateFlags(owner.ID, func(flags *domain.AccountPermissionFlags) { flags.CreateGroup = false })
	f.router.cfg.PublicBaseURL = "https://t.example.test"

	// 后台调用的 ctx 里没有调用会话，也没有登录用户。
	created, err := f.router.AdminCreateGroup(context.Background(), owner.ID, "Sales Team")
	if err != nil {
		t.Fatalf("AdminCreateGroup: %v", err)
	}
	if !created.AccountPermissionBypassed {
		t.Fatalf("created = %+v, want account_permission_bypassed for an owner denied create_group", created)
	}
	ch, err := f.channels.GetChannelByID(f.ctx, created.Channel.ID)
	if err != nil {
		t.Fatalf("stored channel: %v", err)
	}
	if !ch.Megagroup || ch.Broadcast || ch.CreatorUserID != owner.ID || ch.Title != "Sales Team" {
		t.Fatalf("stored channel = %+v, want owner's megagroup titled Sales Team", ch)
	}
	if !strings.HasPrefix(created.InviteLink, "https://t.example.test/+") || len(created.InviteLink) <= len("https://t.example.test/+") {
		t.Fatalf("invite link = %q, want full permanent invite link", created.InviteLink)
	}

	// 没有调用会话时推给群主的全部在线会话：不排除任何会话，推送里带着新群。
	if pushed := sessions.pushedUserIDs(); len(pushed) == 0 || pushed[0] != owner.ID {
		t.Fatalf("pushed users = %v, want owner %d", pushed, owner.ID)
	}
	if snap := sessions.snapshot(); snap.sessionID != 0 || snap.authKeyID != ([8]byte{}) {
		t.Fatalf("push excluded session %d, want none excluded", snap.sessionID)
	}
	updates, ok := sessions.lastUserPush().(*tg.Updates)
	if !ok || !updatesCarryChannel(updates, created.Channel.ID) {
		t.Fatalf("owner push = %#v, want updates carrying channel %d", sessions.lastUserPush(), created.Channel.ID)
	}
}

func TestAdminCreateGroupReportsNoBypassWhenOwnerMayCreateGroups(t *testing.T) {
	f := newRPCChannelFixture(t)
	owner := f.user(1, "15550001001", "Company")
	f.router.deps.Permissions = permissionGateFlags(owner.ID, func(*domain.AccountPermissionFlags) {})

	created, err := f.router.AdminCreateGroup(context.Background(), owner.ID, "Sales Team")
	if err != nil {
		t.Fatalf("AdminCreateGroup: %v", err)
	}
	if created.AccountPermissionBypassed || created.Channel.ID == 0 {
		t.Fatalf("created = %+v, want a group without bypass", created)
	}
}

func updatesCarryChannel(updates *tg.Updates, channelID int64) bool {
	for _, chat := range updates.Chats {
		if ch, ok := chat.(*tg.Channel); ok && ch.ID == channelID {
			return true
		}
	}
	return false
}

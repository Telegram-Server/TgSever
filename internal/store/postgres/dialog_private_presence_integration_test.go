package postgres

import (
	"context"
	"slices"
	"testing"

	"telesrv/internal/domain"
)

// “已有私聊”豁免的存储契约：只看 owner 自己名下的私聊行，单查与批量一致。
func TestPrivateDialogPresencePostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)

	users := NewUserStore(pool)
	sender := createTestUser(t, ctx, users, "+1910"+suffix+"01", "Sender", "S")
	withChat := createTestUser(t, ctx, users, "+1910"+suffix+"02", "WithChat", "W")
	withoutChat := createTestUser(t, ctx, users, "+1910"+suffix+"03", "WithoutChat", "N")
	other := createTestUser(t, ctx, users, "+1910"+suffix+"04", "Other", "O")

	dialogs := NewDialogStore(pool)
	if err := dialogs.Upsert(ctx, withChat.ID, domain.Dialog{
		Peer:       domain.Peer{Type: domain.PeerTypeUser, ID: sender.ID},
		TopMessage: 1,
	}); err != nil {
		t.Fatalf("seed recipient dialog: %v", err)
	}
	// 发送者名下的对话、以及收件人与别人的对话都不能算作收件人与发送者的私聊。
	if err := dialogs.Upsert(ctx, sender.ID, domain.Dialog{
		Peer:       domain.Peer{Type: domain.PeerTypeUser, ID: withoutChat.ID},
		TopMessage: 1,
	}); err != nil {
		t.Fatalf("seed sender dialog: %v", err)
	}
	if err := dialogs.Upsert(ctx, withoutChat.ID, domain.Dialog{
		Peer:       domain.Peer{Type: domain.PeerTypeUser, ID: other.ID},
		TopMessage: 1,
	}); err != nil {
		t.Fatalf("seed unrelated dialog: %v", err)
	}

	for _, tc := range []struct {
		owner int64
		want  bool
	}{
		{owner: withChat.ID, want: true},
		{owner: withoutChat.ID, want: false},
		{owner: other.ID, want: false},
	} {
		got, err := dialogs.HasPrivateDialog(ctx, tc.owner, sender.ID)
		if err != nil {
			t.Fatalf("HasPrivateDialog(%d): %v", tc.owner, err)
		}
		if got != tc.want {
			t.Fatalf("HasPrivateDialog(%d) = %v, want %v", tc.owner, got, tc.want)
		}
	}

	owners, err := dialogs.ListPrivateDialogOwners(ctx, []int64{withChat.ID, withoutChat.ID, other.ID}, sender.ID)
	if err != nil {
		t.Fatalf("ListPrivateDialogOwners: %v", err)
	}
	if !slices.Equal(owners, []int64{withChat.ID}) {
		t.Fatalf("ListPrivateDialogOwners = %v, want [%d]", owners, withChat.ID)
	}
}

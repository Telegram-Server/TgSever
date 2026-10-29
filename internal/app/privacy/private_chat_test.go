package privacy

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// “已有私聊”豁免只看收件人自己的对话列表：收件人名下有与发送者的私聊行才算。
func TestHasPrivateChatBatchReadsRecipientDialogList(t *testing.T) {
	ctx := context.Background()
	dialogs := memory.NewDialogStore()
	svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore()).ConfigurePrivateDialogs(dialogs)

	const sender, withChat, withoutChat, reverseOnly = int64(10), int64(20), int64(30), int64(40)
	if err := dialogs.Upsert(ctx, withChat, domain.Dialog{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: sender}, TopMessage: 1}); err != nil {
		t.Fatalf("seed recipient dialog: %v", err)
	}
	// 只有发送者名下有对话、收件人没有（如收件人删了对话）不算已有私聊。
	if err := dialogs.Upsert(ctx, sender, domain.Dialog{Peer: domain.Peer{Type: domain.PeerTypeUser, ID: reverseOnly}, TopMessage: 1}); err != nil {
		t.Fatalf("seed sender-only dialog: %v", err)
	}

	single, err := svc.HasPrivateChatBatch(ctx, []int64{withChat}, sender)
	if err != nil {
		t.Fatalf("single lookup: %v", err)
	}
	if !single[withChat] {
		t.Fatalf("single lookup = %v, want recipient %d exempt", single, withChat)
	}

	batch, err := svc.HasPrivateChatBatch(ctx, []int64{withChat, withoutChat, reverseOnly, withChat}, sender)
	if err != nil {
		t.Fatalf("batch lookup: %v", err)
	}
	if !batch[withChat] || batch[withoutChat] || batch[reverseOnly] {
		t.Fatalf("batch lookup = %v, want only %d exempt", batch, withChat)
	}

	// 收件人删除整个对话后，发送者重新算作“新的非联系人”。
	if err := dialogs.SaveList(ctx, withChat, domain.DialogList{}); err != nil {
		t.Fatalf("delete recipient dialog: %v", err)
	}
	after, err := svc.HasPrivateChatBatch(ctx, []int64{withChat}, sender)
	if err != nil {
		t.Fatalf("lookup after delete: %v", err)
	}
	if after[withChat] {
		t.Fatalf("lookup after delete = %v, want not exempt", after)
	}
}

// 没有接对话存储时不豁免任何人（保守：维持原有限制）。
func TestHasPrivateChatBatchWithoutDialogStoreExemptsNobody(t *testing.T) {
	svc := NewService(memory.NewPrivacyStore(), memory.NewContactStore())
	got, err := svc.HasPrivateChatBatch(context.Background(), []int64{20}, 10)
	if err != nil {
		t.Fatalf("lookup: %v", err)
	}
	if got[20] {
		t.Fatalf("lookup = %v, want not exempt", got)
	}
}

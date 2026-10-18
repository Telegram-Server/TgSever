package admin

import (
	"context"
	"testing"

	appmessages "telesrv/internal/app/messages"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

type adminDeletionFixture struct {
	ctx       context.Context
	svc       *Service
	messages  *memory.MessageStore
	deletions *memory.MessageDeletionStore
}

func newAdminDeletionFixture(t *testing.T) adminDeletionFixture {
	t.Helper()
	dialogs := memory.NewDialogStore()
	messages := memory.NewMessageStore(dialogs)
	deletions := memory.NewMessageDeletionStore()
	messages.AttachDeletionStore(deletions)
	svc := NewService(Dependencies{
		Commands: newMemoryCommandRepo(),
		Messages: appmessages.NewService(messages, dialogs),
		Now:      fixedNow,
	})
	return adminDeletionFixture{ctx: context.Background(), svc: svc, messages: messages, deletions: deletions}
}

func (f adminDeletionFixture) send(t *testing.T, randomID int64, body string) domain.SendPrivateTextResult {
	t.Helper()
	sent, err := f.messages.SendPrivateText(f.ctx, domain.SendPrivateTextRequest{
		SenderUserID:    1002,
		RecipientUserID: 1001,
		RandomID:        randomID,
		Message:         body,
		Date:            1_700_320_000,
	})
	if err != nil {
		t.Fatalf("SendPrivateText: %v", err)
	}
	return sent
}

// assertOperatorDeletions 断言每行都记在后台操作员名下，而不是被删消息的所属账号。
func (f adminDeletionFixture) assertOperatorDeletions(t *testing.T, meta CommandMeta, want int) {
	t.Helper()
	got, err := f.deletions.ListMessageDeletions(f.ctx, domain.MessageDeletionFilter{SenderUserID: 1002, Limit: 10})
	if err != nil {
		t.Fatalf("ListMessageDeletions: %v", err)
	}
	if len(got) != want {
		t.Fatalf("deletions = %+v, want %d rows", got, want)
	}
	for _, row := range got {
		if row.Source != domain.DeletionSourceAdmin || row.ActorType != domain.DeletionActorOperator ||
			row.ActorUserID != 0 || row.OperatorID != meta.Actor ||
			row.CommandID != meta.CommandID || row.Reason != meta.Reason {
			t.Fatalf("deletion = %+v, want operator %q command %q reason %q", row, meta.Actor, meta.CommandID, meta.Reason)
		}
	}
}

func TestDeletePrivateMessagesRecordsTheOperator(t *testing.T) {
	f := newAdminDeletionFixture(t)
	sent := f.send(t, 320_001, "abusive")
	meta := CommandMeta{CommandID: "delete-log-1", Actor: "ops", Reason: "abuse report #7"}

	if _, err := f.svc.DeletePrivateMessages(f.ctx, DeletePrivateMessagesRequest{
		CommandMeta: meta,
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
		IDs:         []int{sent.RecipientMessage.ID},
		Revoke:      true,
	}); err != nil {
		t.Fatalf("DeletePrivateMessages: %v", err)
	}

	f.assertOperatorDeletions(t, meta, 2)
}

func TestDeletePrivateHistoryRecordsTheOperator(t *testing.T) {
	f := newAdminDeletionFixture(t)
	f.send(t, 320_011, "spam one")
	f.send(t, 320_012, "spam two")
	meta := CommandMeta{CommandID: "delete-log-2", Actor: "ops", Reason: "spam sweep"}

	if _, err := f.svc.DeletePrivateHistory(f.ctx, DeletePrivateHistoryRequest{
		CommandMeta: meta,
		OwnerUserID: 1001,
		Peer:        domain.Peer{Type: domain.PeerTypeUser, ID: 1002},
	}); err != nil {
		t.Fatalf("DeletePrivateHistory: %v", err)
	}

	f.assertOperatorDeletions(t, meta, 2)
}

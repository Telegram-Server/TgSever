package storetest

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// MessageDeletionFixture 是一对新建的私聊用户与对应的存储；每个子测试各要一份。
type MessageDeletionFixture struct {
	Messages          store.MessageStore
	Deletions         store.MessageDeletionStore
	Sender, Recipient int64
}

// deletionRandomBase 是删除记录契约里 random_id 的起点：每个子测试的用户都是新建的，
// 固定常数不会与其他子测试冲突，也不依赖真实时钟。
const deletionRandomBase = 7_700_000

// MessageDeletions 校验两种实现对私聊删除记录的同一契约：每个被删的消息副本
// 留一行记录，写明谁删的、何时删的、删的是哪条消息。每个子测试经 newFixture 取得
// 自己的一对用户与数据，可以单独运行。
func MessageDeletions(t *testing.T, newFixture func(t *testing.T) MessageDeletionFixture) {
	t.Helper()
	ctx := context.Background()
	now := baseNow()
	type env struct {
		MessageDeletionFixture
		send func(t *testing.T, from, to int64, body string) domain.SendPrivateTextResult
		list func(t *testing.T, filter domain.MessageDeletionFilter) []domain.MessageDeletion
	}
	setup := func(t *testing.T) env {
		t.Helper()
		f := newFixture(t)
		nextRandom := int64(deletionRandomBase)
		return env{
			MessageDeletionFixture: f,
			send: func(t *testing.T, from, to int64, body string) domain.SendPrivateTextResult {
				t.Helper()
				nextRandom++
				sent, err := f.Messages.SendPrivateText(ctx, domain.SendPrivateTextRequest{
					SenderUserID:    from,
					RecipientUserID: to,
					RandomID:        nextRandom,
					Message:         body,
					Date:            now,
				})
				if err != nil {
					t.Fatalf("SendPrivateText: %v", err)
				}
				return sent
			},
			list: func(t *testing.T, filter domain.MessageDeletionFilter) []domain.MessageDeletion {
				t.Helper()
				got, err := f.Deletions.ListMessageDeletions(ctx, filter)
				if err != nil {
					t.Fatalf("ListMessageDeletions: %v", err)
				}
				return got
			},
		}
	}
	userPeer := func(id int64) domain.Peer { return domain.Peer{Type: domain.PeerTypeUser, ID: id} }

	t.Run("delete for self records the actor's own copy and device", func(t *testing.T) {
		e := setup(t)
		sender, recipient := e.Sender, e.Recipient
		sent := e.send(t, sender, recipient, "delete me")
		authKeyID := [8]byte{0x21, 0x43, 0x65, 0x87, 0x09, 0xba, 0xdc, 0x7e}
		if _, err := e.Messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
			OwnerUserID:     sender,
			IDs:             []int{sent.SenderMessage.ID},
			Date:            now + 1,
			OriginAuthKeyID: authKeyID,
			OriginSessionID: 77,
		}); err != nil {
			t.Fatalf("DeleteMessages: %v", err)
		}

		got := e.list(t, domain.MessageDeletionFilter{ActorUserID: sender, Limit: 10})
		if len(got) != 1 {
			t.Fatalf("deletions = %+v, want exactly the sender's copy", got)
		}
		row := got[0]
		want := domain.MessageDeletion{
			ID:           row.ID,
			BatchID:      row.BatchID,
			Date:         now + 1,
			Source:       domain.DeletionSourceUser,
			Scope:        domain.DeletionScopeSelf,
			ActorType:    domain.DeletionActorUser,
			ActorUserID:  sender,
			OwnerUserID:  sender,
			Peer:         userPeer(recipient),
			MessageID:    sent.SenderMessage.ID,
			SenderUserID: sender,
			MessageDate:  now,
			Body:         "delete me",

			OriginAuthKeyID: authKeyID,
			OriginSessionID: 77,
		}
		if row != want || row.ID == 0 || row.BatchID == 0 {
			t.Fatalf("deletion = %+v, want %+v with non-zero id/batch", row, want)
		}
	})

	t.Run("revoke by the recipient records both copies in one batch", func(t *testing.T) {
		e := setup(t)
		sender, recipient := e.Sender, e.Recipient
		sent := e.send(t, sender, recipient, "take it back")
		if _, err := e.Messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
			OwnerUserID: recipient,
			IDs:         []int{sent.RecipientMessage.ID},
			Revoke:      true,
			Date:        now + 2,
		}); err != nil {
			t.Fatalf("DeleteMessages revoke: %v", err)
		}

		got := e.list(t, domain.MessageDeletionFilter{ActorUserID: recipient, Limit: 10})
		if len(got) != 2 {
			t.Fatalf("deletions = %+v, want the recipient's and the sender's copy", got)
		}
		byOwner := map[int64]domain.MessageDeletion{got[0].OwnerUserID: got[0], got[1].OwnerUserID: got[1]}
		copies := map[int64]struct {
			peer      domain.Peer
			messageID int
		}{
			recipient: {userPeer(sender), sent.RecipientMessage.ID},
			sender:    {userPeer(recipient), sent.SenderMessage.ID},
		}
		for owner, copy := range copies {
			row, ok := byOwner[owner]
			if !ok {
				t.Fatalf("deletions = %+v, missing owner %d", got, owner)
			}
			want := domain.MessageDeletion{
				ID:           row.ID,
				BatchID:      got[0].BatchID,
				Date:         now + 2,
				Source:       domain.DeletionSourceUser,
				Scope:        domain.DeletionScopeBoth,
				ActorType:    domain.DeletionActorUser,
				ActorUserID:  recipient,
				OwnerUserID:  owner,
				Peer:         copy.peer,
				MessageID:    copy.messageID,
				SenderUserID: sender,
				MessageDate:  now,
				Body:         "take it back",
			}
			if row != want {
				t.Fatalf("owner %d deletion = %+v, want %+v", owner, row, want)
			}
		}
	})

	t.Run("clearing history records every cleared copy as history_clear", func(t *testing.T) {
		e := setup(t)
		sender, recipient := e.Sender, e.Recipient
		first := e.send(t, sender, recipient, "old one")
		second := e.send(t, sender, recipient, "old two")
		if _, err := e.Messages.DeleteHistory(ctx, domain.DeleteHistoryRequest{
			OwnerUserID: sender,
			Peer:        userPeer(recipient),
			Date:        now + 3,
		}); err != nil {
			t.Fatalf("DeleteHistory: %v", err)
		}

		got := e.list(t, domain.MessageDeletionFilter{ActorUserID: sender, Limit: 10})
		if len(got) != 2 {
			t.Fatalf("deletions = %+v, want both cleared copies", got)
		}
		cleared := map[int]string{first.SenderMessage.ID: "old one", second.SenderMessage.ID: "old two"}
		for _, row := range got {
			body, ok := cleared[row.MessageID]
			if !ok {
				t.Fatalf("deletions = %+v, unexpected message %d", got, row.MessageID)
			}
			want := domain.MessageDeletion{
				ID:           row.ID,
				BatchID:      got[0].BatchID,
				Date:         now + 3,
				Source:       domain.DeletionSourceHistoryClear,
				Scope:        domain.DeletionScopeSelf,
				ActorType:    domain.DeletionActorUser,
				ActorUserID:  sender,
				OwnerUserID:  sender,
				Peer:         userPeer(recipient),
				MessageID:    row.MessageID,
				SenderUserID: sender,
				MessageDate:  now,
				Body:         body,
			}
			if row != want {
				t.Fatalf("deletion = %+v, want %+v", row, want)
			}
		}
	})

	t.Run("clearing a saved sub-chat records saved_history", func(t *testing.T) {
		e := setup(t)
		recipient := e.Recipient
		note := e.send(t, recipient, recipient, "note to self")
		if _, err := e.Messages.DeleteSavedHistory(ctx, domain.DeleteSavedHistoryRequest{
			OwnerUserID: recipient,
			SavedPeer:   userPeer(recipient),
			Date:        now + 4,
		}); err != nil {
			t.Fatalf("DeleteSavedHistory: %v", err)
		}

		got := e.list(t, domain.MessageDeletionFilter{ActorUserID: recipient, Limit: 10})
		if len(got) != 1 {
			t.Fatalf("deletions = %+v, want the note", got)
		}
		want := domain.MessageDeletion{
			ID:           got[0].ID,
			BatchID:      got[0].BatchID,
			Date:         now + 4,
			Source:       domain.DeletionSourceSavedHistory,
			Scope:        domain.DeletionScopeSelf,
			ActorType:    domain.DeletionActorUser,
			ActorUserID:  recipient,
			OwnerUserID:  recipient,
			Peer:         userPeer(recipient),
			MessageID:    note.SenderMessage.ID,
			SenderUserID: recipient,
			MessageDate:  now,
			Body:         "note to self",
		}
		if got[0] != want {
			t.Fatalf("deletion = %+v, want %+v", got[0], want)
		}
	})

	t.Run("an explicit audit overrides the owner as actor", func(t *testing.T) {
		e := setup(t)
		sender, recipient := e.Sender, e.Recipient
		sent := e.send(t, sender, recipient, "expired")
		if _, err := e.Messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{
			OwnerUserID: sender,
			IDs:         []int{sent.SenderMessage.ID},
			Revoke:      true,
			Date:        now + 5,
			Audit:       domain.SystemDeletionAudit(domain.DeletionSourceTTL),
		}); err != nil {
			t.Fatalf("DeleteMessages system: %v", err)
		}
		for _, actor := range []int64{sender, recipient} {
			if rows := e.list(t, domain.MessageDeletionFilter{ActorUserID: actor, Limit: 10}); len(rows) != 0 {
				t.Fatalf("system deletion attributed to user %d: %+v", actor, rows)
			}
		}
		rows := e.list(t, domain.MessageDeletionFilter{SenderUserID: sender, Limit: 10})
		if len(rows) != 2 {
			t.Fatalf("system deletion rows = %+v, want both copies", rows)
		}
		for _, row := range rows {
			if row.Source != domain.DeletionSourceTTL || row.ActorType != domain.DeletionActorSystem || row.ActorUserID != 0 {
				t.Fatalf("system deletion = %+v, want system ttl", row)
			}
		}
	})

	t.Run("an operator audit keeps who, which command and why", func(t *testing.T) {
		e := setup(t)
		sender, recipient := e.Sender, e.Recipient
		sent := e.send(t, sender, recipient, "moderated")
		if _, err := e.Messages.DeleteHistory(ctx, domain.DeleteHistoryRequest{
			OwnerUserID: recipient,
			Peer:        userPeer(sender),
			Date:        now + 6,
			Audit:       domain.OperatorDeletionAudit("ops", "cmd-42", "abuse report"),
		}); err != nil {
			t.Fatalf("DeleteHistory operator: %v", err)
		}
		var found bool
		for _, row := range e.list(t, domain.MessageDeletionFilter{SenderUserID: sender, Limit: 50}) {
			if row.MessageID != sent.RecipientMessage.ID || row.OwnerUserID != recipient {
				continue
			}
			found = true
			if row.Source != domain.DeletionSourceAdmin || row.ActorType != domain.DeletionActorOperator || row.ActorUserID != 0 ||
				row.OperatorID != "ops" || row.CommandID != "cmd-42" || row.Reason != "abuse report" {
				t.Fatalf("deletion = %+v, want operator ops / cmd-42 / abuse report", row)
			}
		}
		if !found {
			t.Fatal("operator deletion of the recipient's copy was not recorded")
		}
	})

	t.Run("filter by sender finds deletions of that sender's messages", func(t *testing.T) {
		e := setup(t)
		sender, recipient := e.Sender, e.Recipient
		other := e.send(t, sender, recipient, "from the sender")
		if _, err := e.Messages.DeleteMessages(ctx, domain.DeleteMessagesRequest{OwnerUserID: recipient, IDs: []int{other.RecipientMessage.ID}, Date: now + 1}); err != nil {
			t.Fatalf("DeleteMessages: %v", err)
		}
		e.send(t, recipient, recipient, "note to self")
		if _, err := e.Messages.DeleteSavedHistory(ctx, domain.DeleteSavedHistoryRequest{OwnerUserID: recipient, SavedPeer: userPeer(recipient), Date: now + 2}); err != nil {
			t.Fatalf("DeleteSavedHistory: %v", err)
		}
		got := e.list(t, domain.MessageDeletionFilter{SenderUserID: recipient, Limit: 10})
		if len(got) != 1 || got[0].SenderUserID != recipient || got[0].Body != "note to self" {
			t.Fatalf("deletions by sender %d = %+v, want only the note", recipient, got)
		}
	})
}

// latestBatch 取按 ID 倒序列表中最新一次删除操作的所有行。
func latestBatch(rows []domain.MessageDeletion) []domain.MessageDeletion {
	if len(rows) == 0 {
		return nil
	}
	out := make([]domain.MessageDeletion, 0, len(rows))
	for _, row := range rows {
		if row.BatchID == rows[0].BatchID {
			out = append(out, row)
		}
	}
	return out
}

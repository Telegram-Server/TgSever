package postgres

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
)

func TestSuggestedPostTONDeclineEscrowRefundAndSettlementPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	suffix := randomSuffix(t)
	users := NewUserStore(pool)
	owner, err := users.Create(ctx, domain.User{AccessHash: 901, Phone: "+1777" + suffix + "01", FirstName: "Owner"})
	if err != nil {
		t.Fatal(err)
	}
	payer, err := users.Create(ctx, domain.User{AccessHash: 902, Phone: "+1777" + suffix + "02", FirstName: "Payer"})
	if err != nil {
		t.Fatal(err)
	}
	s := NewChannelStore(pool)
	created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "TON suggestions", Broadcast: true, Date: 1_700_100_000})
	if err != nil {
		t.Fatal(err)
	}
	enabled, err := s.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
	if err != nil {
		t.Fatal(err)
	}
	monoID := enabled.Channel.LinkedMonoforumID
	if _, err := pool.Exec(ctx, `INSERT INTO ton_balances(user_id,balance_nanoton) VALUES($1,100000000)`, payer.ID); err != nil {
		t.Fatal(err)
	}
	balances := func(wantPayer, wantChannel int64) {
		t.Helper()
		var payerBalance, channelBalance int64
		if err := pool.QueryRow(ctx, `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`, payer.ID).Scan(&payerBalance); err != nil {
			t.Fatal(err)
		}
		if err := pool.QueryRow(ctx, `SELECT COALESCE((SELECT balance_nanoton FROM channel_ton_balances WHERE channel_id=$1),0)`, created.Channel.ID).Scan(&channelBalance); err != nil {
			t.Fatal(err)
		}
		if payerBalance != wantPayer || channelBalance != wantChannel {
			t.Fatalf("TON payer/channel=%d/%d want %d/%d", payerBalance, channelBalance, wantPayer, wantChannel)
		}
	}
	send := func(randomID int64) domain.SendChannelMessageResult {
		t.Helper()
		res, err := s.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: payer.ID, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: payer.ID}, RandomID: randomID, Message: "TON suggestion", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: domain.SuggestedPostPriceTON, Amount: 10_000_000}}, Date: 1_700_100_000})
		if err != nil {
			t.Fatal(err)
		}
		return res
	}
	declined := send(9701)
	if _, err := s.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: declined.Message.ID, Reject: true, Date: 1_700_100_001}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: declined.Message.ID, Date: 1_700_100_001}); !errors.Is(err, domain.ErrSuggestedPostAlreadyHandled) {
		t.Fatalf("approve declined=%v", err)
	}
	balances(100_000_000, 0)
	refunded := send(9702)
	command := domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: refunded.Message.ID, Date: 1_700_100_001}
	approved, err := s.ToggleSuggestedPostApproval(ctx, command)
	if err != nil || approved.Published == nil {
		t.Fatalf("TON approval=%+v err=%v", approved, err)
	}
	if _, err := s.ToggleSuggestedPostApproval(ctx, command); err != nil {
		t.Fatal(err)
	}
	balances(90_000_000, 0)
	if _, err := s.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{UserID: owner.ID, ChannelID: created.Channel.ID, IDs: []int{approved.Published.Message.ID}, Date: 1_700_100_002}); err != nil {
		t.Fatal(err)
	}
	result, err := s.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_100_003})
	if err != nil || len(result) != 1 || result[0].State != domain.SuggestedPostStateRefunded {
		t.Fatalf("TON refund=%+v err=%v", result, err)
	}
	balances(100_000_000, 0)
	settled := send(9703)
	if _, err := s.ToggleSuggestedPostApproval(ctx, domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: settled.Message.ID, Date: 1_700_100_004}); err != nil {
		t.Fatal(err)
	}
	result, err = s.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_100_004 + suggestedPostSettlementAge})
	if err != nil || len(result) != 1 || result[0].State != domain.SuggestedPostStateCompleted || result[0].ServiceMessage.Action.SuggestedPostPrice.Amount != 8_500_000 {
		t.Fatalf("TON settlement=%+v err=%v", result, err)
	}
	balances(90_000_000, 8_500_000)
	if _, err := s.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: 1_700_100_005 + suggestedPostSettlementAge}); err != nil {
		t.Fatal(err)
	}
	balances(90_000_000, 8_500_000)
}

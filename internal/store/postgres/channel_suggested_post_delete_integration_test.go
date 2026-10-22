package postgres

import (
	"context"
	"errors"
	"sync"
	"testing"

	"telesrv/internal/domain"
)

func TestSuggestedPostChannelDeletionFinishesEscrowPostgres(t *testing.T) {
	pool := testPool(t)
	for _, kind := range []domain.SuggestedPostPriceKind{domain.SuggestedPostPriceStars, domain.SuggestedPostPriceTON} {
		for _, stage := range []string{"scheduled", "published-before", "published-at", "published-after", "post-deleted-before", "concurrent-approval"} {
			t.Run(string(kind)+"/"+stage, func(t *testing.T) {
				ctx := context.Background()
				suffix := randomSuffix(t)
				users := NewUserStore(pool)
				owner, err := users.Create(ctx, domain.User{AccessHash: 981, Phone: "+1666" + suffix + "01", FirstName: "Owner"})
				if err != nil {
					t.Fatal(err)
				}
				payer, err := users.Create(ctx, domain.User{AccessHash: 982, Phone: "+1666" + suffix + "02", FirstName: "Payer"})
				if err != nil {
					t.Fatal(err)
				}
				s := NewChannelStore(pool)
				created, err := s.CreateChannel(ctx, domain.CreateChannelRequest{CreatorUserID: owner.ID, Title: "Delete with escrow", Broadcast: true, Date: 1_700_300_000})
				if err != nil {
					t.Fatal(err)
				}
				enabled, err := s.SetPaidMessagesPrice(ctx, owner.ID, created.Channel.ID, 0, true)
				if err != nil {
					t.Fatal(err)
				}
				monoID := enabled.Channel.LinkedMonoforumID
				amount, initial := int64(10), int64(100)
				if kind == domain.SuggestedPostPriceTON {
					amount, initial = 10_000_000, 100_000_000
					_, err = pool.Exec(ctx, `INSERT INTO ton_balances(user_id,balance_nanoton) VALUES($1,$2)`, payer.ID, initial)
				} else {
					_, err = pool.Exec(ctx, `INSERT INTO stars_balances(user_id,balance,granted) VALUES($1,$2,true)`, payer.ID, initial)
				}
				if err != nil {
					t.Fatal(err)
				}
				const approvedAt = 1_700_300_000
				post, err := s.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: monoID, SenderUserID: payer.ID, SavedPeer: domain.Peer{Type: domain.PeerTypeUser, ID: payer.ID}, RandomID: 58101, Message: "Delete channel with escrow", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: kind, Amount: amount}}, Date: approvedAt - 1})
				if err != nil {
					t.Fatal(err)
				}
				command := domain.ToggleSuggestedPostApprovalRequest{UserID: owner.ID, MonoforumID: monoID, MessageID: post.Message.ID, Date: approvedAt}
				if stage == "scheduled" {
					command.ScheduleDate = approvedAt + 3600
				}
				deleteAt := approvedAt + suggestedPostSettlementAge - 1
				if stage == "scheduled" || stage == "concurrent-approval" {
					deleteAt = approvedAt + 1
				}
				if stage == "published-at" {
					deleteAt++
				}
				if stage == "published-after" || stage == "post-deleted-before" {
					deleteAt += 2
				}
				deletion := domain.DeleteChannelRequest{UserID: owner.ID, ChannelID: created.Channel.ID, Date: deleteAt}
				if stage == "concurrent-approval" {
					var wg sync.WaitGroup
					var approvalErr, deleteErr error
					start := make(chan struct{})
					wg.Add(2)
					go func() { defer wg.Done(); <-start; _, approvalErr = s.ToggleSuggestedPostApproval(ctx, command) }()
					go func() { defer wg.Done(); <-start; _, deleteErr = s.DeleteChannel(ctx, deletion) }()
					close(start)
					wg.Wait()
					if deleteErr != nil || (approvalErr != nil && !errors.Is(approvalErr, domain.ErrSuggestedPostInvalid)) {
						t.Fatalf("concurrent approval/delete=%v/%v", approvalErr, deleteErr)
					}
				} else {
					approved, err := s.ToggleSuggestedPostApproval(ctx, command)
					if err != nil {
						t.Fatal(err)
					}
					if stage == "post-deleted-before" {
						if _, err := s.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{UserID: owner.ID, ChannelID: created.Channel.ID, IDs: []int{approved.Published.Message.ID}, Date: approvedAt + 1}); err != nil {
							t.Fatal(err)
						}
					}
					if _, err := s.DeleteChannel(ctx, deletion); err != nil {
						t.Fatal(err)
					}
				}
				wantState, wantPayer, wantChannel := string(domain.SuggestedPostStateRefunded), initial, int64(0)
				if stage == "published-at" || stage == "published-after" {
					wantState, wantPayer, wantChannel = string(domain.SuggestedPostStateCompleted), initial-amount, amount*850/1000
				}
				var pending int
				if err := pool.QueryRow(ctx, `SELECT count(*) FROM suggested_post_approvals WHERE monoforum_id=$1 AND state IN ('scheduled','published')`, monoID).Scan(&pending); err != nil || pending != 0 {
					t.Fatalf("pending escrow=%d err=%v", pending, err)
				}
				if stage != "concurrent-approval" {
					var state string
					if err := pool.QueryRow(ctx, `SELECT state FROM suggested_post_approvals WHERE monoforum_id=$1 AND suggestion_message_id=$2`, monoID, post.Message.ID).Scan(&state); err != nil || state != wantState {
						t.Fatalf("state=%s want %s err=%v", state, wantState, err)
					}
				}
				assertBalances := func() {
					t.Helper()
					var payerBalance, channelBalance int64
					payerQuery := `SELECT balance FROM stars_balances WHERE user_id=$1`
					channelQuery := `SELECT COALESCE((SELECT balance FROM channel_stars_balances WHERE channel_id=$1),0)`
					if kind == domain.SuggestedPostPriceTON {
						payerQuery = `SELECT balance_nanoton FROM ton_balances WHERE user_id=$1`
						channelQuery = `SELECT COALESCE((SELECT balance_nanoton FROM channel_ton_balances WHERE channel_id=$1),0)`
					}
					if err := pool.QueryRow(ctx, payerQuery, payer.ID).Scan(&payerBalance); err != nil {
						t.Fatal(err)
					}
					if err := pool.QueryRow(ctx, channelQuery, created.Channel.ID).Scan(&channelBalance); err != nil {
						t.Fatal(err)
					}
					if payerBalance != wantPayer || channelBalance != wantChannel {
						t.Fatalf("payer/channel=%d/%d want %d/%d", payerBalance, channelBalance, wantPayer, wantChannel)
					}
				}
				assertBalances()
				var beforePublished int
				if err := pool.QueryRow(ctx, `SELECT top_message_id FROM channels WHERE id=$1`, created.Channel.ID).Scan(&beforePublished); err != nil {
					t.Fatal(err)
				}
				if _, err := s.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: deleteAt + 2*suggestedPostSettlementAge}); err != nil {
					t.Fatal(err)
				}
				assertBalances()
				var afterPublished int
				if err := pool.QueryRow(ctx, `SELECT top_message_id FROM channels WHERE id=$1`, created.Channel.ID).Scan(&afterPublished); err != nil || beforePublished != afterPublished {
					t.Fatalf("worker published after channel deletion %d/%d err=%v", beforePublished, afterPublished, err)
				}
			})
		}
	}
}

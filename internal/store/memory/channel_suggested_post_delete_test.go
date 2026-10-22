package memory

import (
	"context"
	"testing"

	"telesrv/internal/domain"
)

func TestSuggestedPostChannelDeletionFinishesEscrow(t *testing.T) {
	for _, kind := range []domain.SuggestedPostPriceKind{domain.SuggestedPostPriceStars, domain.SuggestedPostPriceTON} {
		for _, stage := range []string{"scheduled", "published-before", "published-at", "published-after", "post-deleted-before"} {
			t.Run(string(kind)+"/"+stage, func(t *testing.T) {
				ctx := context.Background()
				s, parent, mono, payer := newSuggestedPostMemoryFixture(t)
				amount, initial := int64(10), int64(100)
				if kind == domain.SuggestedPostPriceTON {
					amount, initial = 10_000_000, 100_000_000
					s.tonBalances[payer.ID] = initial
				} else {
					s.starsBalances[payer.ID] = initial
				}
				const approvedAt = 1_700_300_000
				post, err := s.SendMonoforumMessage(ctx, domain.SendMonoforumMessageRequest{MonoforumID: mono.ID, SenderUserID: payer.ID, SavedPeer: payer, RandomID: 58101, Message: "Delete channel with escrow", SuggestedPost: &domain.SuggestedPost{Price: &domain.SuggestedPostPrice{Kind: kind, Amount: amount}}, Date: approvedAt - 1})
				if err != nil {
					t.Fatal(err)
				}
				command := domain.ToggleSuggestedPostApprovalRequest{UserID: 1, MonoforumID: mono.ID, MessageID: post.Message.ID, Date: approvedAt}
				if stage == "scheduled" {
					command.ScheduleDate = approvedAt + 3600
				}
				approved, err := s.ToggleSuggestedPostApproval(ctx, command)
				if err != nil {
					t.Fatal(err)
				}
				deleteAt := approvedAt + suggestedPostSettlementAge - 1
				if stage == "scheduled" {
					deleteAt = approvedAt + 1
				}
				if stage == "published-at" {
					deleteAt++
				}
				if stage == "published-after" || stage == "post-deleted-before" {
					deleteAt += 2
				}
				if stage == "post-deleted-before" {
					if _, err := s.DeleteChannelMessages(ctx, domain.DeleteChannelMessagesRequest{UserID: 1, ChannelID: parent.ID, IDs: []int{approved.Published.Message.ID}, Date: approvedAt + 1}); err != nil {
						t.Fatal(err)
					}
				}
				if _, err := s.DeleteChannel(ctx, domain.DeleteChannelRequest{UserID: 1, ChannelID: parent.ID, Date: deleteAt}); err != nil {
					t.Fatal(err)
				}
				wantState, wantPayer, wantChannel := domain.SuggestedPostStateRefunded, initial, int64(0)
				if stage == "published-at" || stage == "published-after" {
					wantState, wantPayer, wantChannel = domain.SuggestedPostStateCompleted, initial-amount, amount*850/1000
				}
				if got := s.suggestedPostApprovals[memorySuggestedPostKey{monoforumID: mono.ID, messageID: post.Message.ID}].state; got != wantState {
					t.Fatalf("state=%s want %s", got, wantState)
				}
				assertBalances := func() {
					t.Helper()
					payerBalance, channelBalance := s.starsBalances[payer.ID], s.channelStarsBalances[parent.ID]
					if kind == domain.SuggestedPostPriceTON {
						payerBalance, channelBalance = s.tonBalances[payer.ID], s.channelTONBalances[parent.ID]
					}
					if payerBalance != wantPayer || channelBalance != wantChannel {
						t.Fatalf("payer/channel=%d/%d want %d/%d", payerBalance, channelBalance, wantPayer, wantChannel)
					}
				}
				assertBalances()
				beforePublished := s.channels[parent.ID].TopMessageID
				if _, err := s.ProcessSuggestedPostLifecycle(ctx, domain.SuggestedPostLifecycleRequest{Now: deleteAt + 2*suggestedPostSettlementAge}); err != nil {
					t.Fatal(err)
				}
				assertBalances()
				if s.channels[parent.ID].TopMessageID != beforePublished {
					t.Fatal("worker published into deleted channel")
				}
			})
		}
	}
}

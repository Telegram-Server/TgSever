package memory

import (
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// TestSendMonoforumMessageAndHistory 跑频道私信(monoforum)发送+按订阅者读历史的共享契约。
// 群主删除 monoforum 消息只能走私有的 deleteChannelMessagesLocked（群主不是 monoforum 成员）。
func TestSendMonoforumMessageAndHistory(t *testing.T) {
	storetest.MonoforumSendAndHistory(t, func(t *testing.T) storetest.MonoforumFixture {
		store := NewChannelStore()
		return storetest.MonoforumFixture{
			Channels: store, Dialogs: NewDialogStore(), Owner: 1, Sub: 42, Other: 99,
			DeleteAsCreator: func(t *testing.T, monoID, creatorID int64, ids []int, date int) domain.ChannelUpdateEvent {
				t.Helper()
				store.mu.Lock()
				defer store.mu.Unlock()
				member := domain.ChannelMember{ChannelID: monoID, UserID: creatorID, Role: domain.ChannelRoleCreator, Status: domain.ChannelMemberActive}
				_, event, _, err := store.deleteChannelMessagesLocked(store.channels[monoID], member, ids, creatorID, date, domain.UserDeletionAudit(domain.DeletionSourceUser, creatorID, [8]byte{}, 0), false)
				if err != nil {
					t.Fatalf("delete monoforum message: %v", err)
				}
				return event
			},
		}
	})
}

// TestSendPaidMonoforumMessageLedger 跑付费私信账本的共享契约；余额钩子直接读写内存余额表。
func TestSendPaidMonoforumMessageLedger(t *testing.T) {
	storetest.MonoforumPaidMessages(t, func(t *testing.T) storetest.PaidMonoforumFixture {
		store := NewChannelStore()
		return storetest.PaidMonoforumFixture{
			Channels: store, Owner: 1, Sub: 42, Other: 99,
			GrantStars: func(t *testing.T, userID, stars int64) {
				store.mu.Lock()
				defer store.mu.Unlock()
				store.starsBalances[userID] += stars
			},
			StarsBalances: func(t *testing.T, userID, channelID int64) (int64, int64) {
				store.mu.RLock()
				defer store.mu.RUnlock()
				return store.starsBalances[userID], store.channelStarsBalances[channelID]
			},
		}
	})
}

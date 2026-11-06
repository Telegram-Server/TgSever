package rpc

import (
	"context"
	"slices"
	"testing"

	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appchannels "telesrv/internal/app/channels"
	appcontacts "telesrv/internal/app/contacts"
	"telesrv/internal/app/onboarding"
	appusers "telesrv/internal/app/users"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// TestOnboardingDispatcherPushesJoinAndContactUpdates 只测接线：调度器领取新账号的入驻任务并执行后，
// 群里的老成员收到"加入"的 update，双向联系人的对方收到联系人 update。入驻的规则矩阵在
// app/onboarding 测。
func TestOnboardingDispatcherPushesJoinAndContactUpdates(t *testing.T) {
	ctx := context.Background()
	userStore := memory.NewUserStore()
	owner, _ := userStore.Create(ctx, domain.User{AccessHash: 71, Phone: "15550002171", FirstName: "Owner"})
	support, _ := userStore.Create(ctx, domain.User{AccessHash: 72, Phone: "15550002172", FirstName: "Support"})
	channelSvc := appchannels.NewService(memory.NewChannelStore())
	contactSvc := appcontacts.NewService(memory.NewContactStore(), userStore)
	onboardingStore := memory.NewOnboardingStore()
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Users:      appusers.NewService(userStore),
		Channels:   channelSvc,
		Contacts:   contactSvc,
		Sessions:   sessions,
		Onboarding: onboarding.NewService(onboardingStore, userStore, contactSvc, channelSvc),
	}, zaptest.NewLogger(t), clock.System)

	created, err := r.onChannelsCreateChannel(WithUserID(ctx, owner.ID), &tg.ChannelsCreateChannelRequest{Title: "Lobby", Megagroup: true})
	if err != nil {
		t.Fatalf("create group: %v", err)
	}
	group := created.(*tg.Updates).Chats[0].(*tg.Channel).ID
	if _, _, _, err := onboardingStore.SetOnboardingDefaults(ctx, domain.OnboardingDefaults{
		Contacts:   []domain.OnboardingContact{{UserID: support.ID, Mutual: true}},
		ChannelIDs: []int64{group},
	}); err != nil {
		t.Fatal(err)
	}
	newcomer, err := memory.NewAccountCreationStore(userStore, nil, nil).WithOnboarding(onboardingStore).
		CreateAccount(ctx, domain.User{AccessHash: 73, Phone: "15550002173", FirstName: "Newcomer"}, store.AccountExtras{})
	if err != nil {
		t.Fatalf("create account: %v", err)
	}
	sessions.clearMessages()

	if !NewOnboardingDispatcher(r, zaptest.NewLogger(t)).DispatchOnce(ctx) {
		t.Fatal("dispatcher found no onboarding job")
	}
	pushed := sessions.pushedUserIDs()
	for _, want := range []struct {
		name string
		id   int64
	}{{"group owner (join)", owner.ID}, {"mutual contact", support.ID}, {"newcomer", newcomer.ID}} {
		if !slices.Contains(pushed, want.id) {
			t.Fatalf("pushed users = %v, want the %s %d", pushed, want.name, want.id)
		}
	}
	if NewOnboardingDispatcher(r, zaptest.NewLogger(t)).DispatchOnce(ctx) {
		t.Fatal("the completed job was dispatched again")
	}
}

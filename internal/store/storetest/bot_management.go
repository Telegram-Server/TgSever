package storetest

import (
	"context"
	"errors"
	"reflect"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"testing"
	"time"
)

type BotManagementFixture struct {
	Bots       store.BotStore
	Management store.BotManagementStore
	BotID      int64
}

func BotManagement(t *testing.T, newFixture func(*testing.T) BotManagementFixture) {
	t.Helper()
	ctx := context.Background()
	t.Run("manager authorization and access settings persist independently of caller slices", func(t *testing.T) {
		f := newFixture(t)
		p, found, err := f.Bots.GetBot(ctx, f.BotID)
		if err != nil || !found {
			t.Fatalf("bot: %+v %v", p, err)
		}
		if err = f.Management.SetBotManager(ctx, p.OwnerUserID+1000000, f.BotID, domain.BotFatherUserID); err == nil {
			t.Fatal("stranger assigned manager")
		}
		if err = f.Management.SetBotManager(ctx, p.OwnerUserID, f.BotID, domain.BotFatherUserID); err != nil {
			t.Fatal(err)
		}
		if err = f.Management.SetManagedBotAccess(ctx, p.OwnerUserID+1000000, f.BotID, true, []int64{p.OwnerUserID}); err == nil {
			t.Fatal("stranger changed access")
		}
		added := []int64{p.OwnerUserID}
		if err = f.Management.SetManagedBotAccess(ctx, domain.BotFatherUserID, f.BotID, true, added); err != nil {
			t.Fatal(err)
		}
		added[0] = 0
		m, err := f.Management.GetBotManagement(ctx, f.BotID)
		if err != nil || m.ManagerBotID != domain.BotFatherUserID || !m.IsAccessRestricted || !reflect.DeepEqual(m.AddedUserIDs, []int64{p.OwnerUserID}) {
			t.Fatalf("saved management=%+v %v", m, err)
		}
		m.AddedUserIDs[0] = 0
		m, err = f.Management.GetBotManagement(ctx, f.BotID)
		if err != nil || m.AddedUserIDs[0] != p.OwnerUserID {
			t.Fatalf("read alias mutated state: %+v %v", m, err)
		}
		if err = f.Management.SetManagedBotAccess(ctx, p.OwnerUserID, f.BotID, false, nil); err != nil {
			t.Fatal(err)
		}
		m, err = f.Management.GetBotManagement(ctx, f.BotID)
		if err != nil || m.IsAccessRestricted || len(m.AddedUserIDs) != 0 || m.ManagerBotID != domain.BotFatherUserID {
			t.Fatalf("clear %+v %v", m, err)
		}
	})
	t.Run("close timing restart and cloud logout cooldown", func(t *testing.T) {
		f := newFixture(t)
		now := time.Unix(1800000000, 0)
		local := domain.BotAPIInstanceKey{BotUserID: f.BotID, InstanceID: "local-old"}
		if _, err := f.Management.BeginBotAPIInstance(ctx, local, false, now); err != nil {
			t.Fatal(err)
		}
		var wait *domain.BotAPIInstanceWaitError
		if err := f.Management.StopBotAPIInstance(ctx, local, false, now.Add(599*time.Second)); !errors.As(err, &wait) || wait.Seconds != 1 {
			t.Fatalf("early close=%v", err)
		}
		if err := f.Management.StopBotAPIInstance(ctx, local, false, now.Add(600*time.Second)); err != nil {
			t.Fatal(err)
		}
		if _, err := f.Management.BeginBotAPIInstance(ctx, local, false, now.Add(601*time.Second)); !errors.Is(err, domain.ErrBotInstanceClosed) {
			t.Fatalf("closed restarted=%v", err)
		}
		restart := domain.BotAPIInstanceKey{BotUserID: f.BotID, InstanceID: "local-new"}
		if _, err := f.Management.BeginBotAPIInstance(ctx, restart, false, now.Add(601*time.Second)); err != nil {
			t.Fatal(err)
		}
		cloud := domain.BotAPIInstanceKey{BotUserID: f.BotID, InstanceID: "cloud-old"}
		if _, err := f.Management.BeginBotAPIInstance(ctx, cloud, true, now); err != nil {
			t.Fatal(err)
		}
		if err := f.Management.StopBotAPIInstance(ctx, cloud, true, now.Add(time.Second)); err != nil {
			t.Fatal(err)
		}
		cloud.InstanceID = "cloud-new"
		if _, err := f.Management.BeginBotAPIInstance(ctx, cloud, true, now.Add(2*time.Second)); !errors.As(err, &wait) || wait.Seconds != 599 {
			t.Fatalf("cloud cooldown=%v", err)
		}
		if _, err := f.Management.BeginBotAPIInstance(ctx, restart, false, now.Add(2*time.Second)); err != nil {
			t.Fatalf("local blocked after cloud logout=%v", err)
		}
		if _, err := f.Management.BeginBotAPIInstance(ctx, cloud, true, now.Add(601*time.Second)); err != nil {
			t.Fatalf("cloud after cooldown=%v", err)
		}
	})
}

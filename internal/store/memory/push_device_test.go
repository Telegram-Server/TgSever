package memory_test

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
	"telesrv/internal/store/storetest"
)

func TestPushDeviceContractMemory(t *testing.T) {
	storetest.PushDeviceContract(t, func(t *testing.T) storetest.PushDeviceFixture {
		ctx := context.Background()
		users := memory.NewUserStore()
		a, err := users.Create(ctx, domain.User{AccessHash: 1, Phone: "15550009401", FirstName: "A"})
		if err != nil {
			t.Fatal(err)
		}
		b, err := users.Create(ctx, domain.User{AccessHash: 2, Phone: "15550009402", FirstName: "B"})
		if err != nil {
			t.Fatal(err)
		}
		keys := memory.NewAuthKeyStore()
		auths := memory.NewAuthorizationStore()
		auths.LinkAuthKeyAuthority(keys)
		f := storetest.PushDeviceFixture{
			Devices: memory.NewPushDeviceStore(auths),
			Auths:   auths,
			UserA:   a.ID, UserB: b.ID,
			AuthA1: [8]byte{0x94, 1}, AuthA2: [8]byte{0x94, 2}, AuthB: [8]byte{0x94, 3},
			HashA1: 9401, HashA2: 9402,
			DeleteAuthKey: keys.Delete,
			DeleteAccount: func(ctx context.Context, userID int64) error {
				lifecycle := memory.NewAccountLifecycleStore(users, nil, memory.WithAccountLifecycleAuthorizations(auths))
				_, err := lifecycle.ExecuteAccountDeletion(ctx, userID, domain.AccountDeletionManual, "", time.Now())
				return err
			},
		}
		for _, bind := range []domain.Authorization{
			{AuthKeyID: f.AuthA1, UserID: a.ID, Hash: f.HashA1},
			{AuthKeyID: f.AuthA2, UserID: a.ID, Hash: f.HashA2},
			{AuthKeyID: f.AuthB, UserID: b.ID, Hash: 9403},
		} {
			if err := keys.Save(ctx, store.AuthKeyData{ID: bind.AuthKeyID}); err != nil {
				t.Fatal(err)
			}
			if err := auths.Bind(ctx, bind); err != nil {
				t.Fatal(err)
			}
		}
		return f
	})
}

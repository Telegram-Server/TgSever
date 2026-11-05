package storetest

import (
	"context"
	"errors"
	"fmt"
	"sync/atomic"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// GuestAccountFixture 是同一份底层数据上的建号操作、账号与游客存储。NewPhone 每次返回一个
// 还没有人占用的手机号。
type GuestAccountFixture struct {
	Accounts store.AccountCreationStore
	Users    store.UserStore
	Guests   store.GuestAccountStore
	NewPhone func(t *testing.T) string
}

var guestIPKeySeq atomic.Int64

func newGuestIPKey() string {
	return fmt.Sprintf("198.51.100.%d", guestIPKeySeq.Add(1))
}

// GuestAccountContract 是游客建号的契约：建号同时写游客标记与 IP 绑定；绑定按比较并交换
// 替换，交换失败整体回滚、不占号码；同一 IP 并发建号恰好一个成功。
func GuestAccountContract(t *testing.T, newFixture func(t *testing.T) GuestAccountFixture) {
	t.Helper()
	ctx := context.Background()

	createGuest := func(t *testing.T, f GuestAccountFixture, ip string, previous *int64) (domain.User, error) {
		t.Helper()
		return f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: f.NewPhone(t), FirstName: "Guest"},
			store.AccountExtras{Guest: &store.GuestAccountExtras{IPKey: ip, PreviousUserID: previous}})
	}

	t.Run("create writes marker and binding", func(t *testing.T) {
		f := newFixture(t)
		ip := newGuestIPKey()
		u, err := createGuest(t, f, ip, nil)
		if err != nil {
			t.Fatalf("create guest: %v", err)
		}
		account, found, err := f.Guests.GuestAccount(ctx, u.ID)
		if err != nil || !found || account.Phone != u.Phone || account.IPKey != ip || account.CreatedAt.IsZero() {
			t.Fatalf("guest account=%+v found=%v err=%v, want phone=%s ip=%s", account, found, err, u.Phone, ip)
		}
		binding, found, err := f.Guests.GuestIPBinding(ctx, ip)
		if err != nil || !found || binding.UserID != u.ID || binding.BoundAt.IsZero() {
			t.Fatalf("binding=%+v found=%v err=%v, want user %d", binding, found, err, u.ID)
		}
	})

	t.Run("ordinary account is not a guest", func(t *testing.T) {
		f := newFixture(t)
		u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: f.NewPhone(t), FirstName: "Plain"}, store.AccountExtras{})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		if _, found, err := f.Guests.GuestAccount(ctx, u.ID); err != nil || found {
			t.Fatalf("guest account found=%v err=%v, want none", found, err)
		}
		if _, found, err := f.Guests.GuestIPBinding(ctx, newGuestIPKey()); err != nil || found {
			t.Fatalf("binding of unknown ip found=%v err=%v, want none", found, err)
		}
	})

	t.Run("binding swaps only from the observed account", func(t *testing.T) {
		f := newFixture(t)
		ip := newGuestIPKey()
		first, err := createGuest(t, f, ip, nil)
		if err != nil {
			t.Fatalf("create first guest: %v", err)
		}
		// 读到"没有绑定"或读到别的账号时，交换失败并回滚：不留账号、不占号码。
		wrong := first.ID + 1
		for _, previous := range []*int64{nil, &wrong} {
			phone := f.NewPhone(t)
			_, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 8, Phone: phone, FirstName: "Late"},
				store.AccountExtras{Guest: &store.GuestAccountExtras{IPKey: ip, PreviousUserID: previous}})
			if !errors.Is(err, domain.ErrGuestIPBindingChanged) {
				t.Fatalf("stale swap err=%v, want ErrGuestIPBindingChanged", err)
			}
			if owner, found, err := f.Users.ByPhone(ctx, phone); err != nil || found {
				t.Fatalf("phone owner after failed swap=%+v found=%v err=%v, want none", owner, found, err)
			}
		}
		second, err := createGuest(t, f, ip, &first.ID)
		if err != nil {
			t.Fatalf("swap from observed account: %v", err)
		}
		binding, found, err := f.Guests.GuestIPBinding(ctx, ip)
		if err != nil || !found || binding.UserID != second.ID {
			t.Fatalf("binding after swap=%+v found=%v err=%v, want user %d", binding, found, err, second.ID)
		}
		if _, found, err := f.Guests.GuestAccount(ctx, first.ID); err != nil || !found {
			t.Fatalf("first guest marker found=%v err=%v, want kept", found, err)
		}
	})

	t.Run("concurrent create on the same ip", func(t *testing.T) {
		f := newFixture(t)
		ip := newGuestIPKey()
		const workers = 4
		phones := make([]string, workers)
		for i := range phones {
			phones[i] = f.NewPhone(t)
		}
		start := make(chan struct{})
		type result struct {
			user domain.User
			err  error
		}
		results := make(chan result, workers)
		for i := 0; i < workers; i++ {
			go func(i int) {
				<-start
				u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: int64(i + 1), Phone: phones[i], FirstName: "Racer"},
					store.AccountExtras{Guest: &store.GuestAccountExtras{IPKey: ip}})
				results <- result{user: u, err: err}
			}(i)
		}
		close(start)
		var winner domain.User
		successes := 0
		for i := 0; i < workers; i++ {
			r := <-results
			switch {
			case r.err == nil:
				successes++
				winner = r.user
			case errors.Is(r.err, domain.ErrGuestIPBindingChanged):
			default:
				t.Fatalf("concurrent guest create err=%v", r.err)
			}
		}
		if successes != 1 {
			t.Fatalf("successful guest creates=%d, want exactly 1", successes)
		}
		binding, found, err := f.Guests.GuestIPBinding(ctx, ip)
		if err != nil || !found || binding.UserID != winner.ID {
			t.Fatalf("binding=%+v found=%v err=%v, want winner %d", binding, found, err, winner.ID)
		}
	})
}

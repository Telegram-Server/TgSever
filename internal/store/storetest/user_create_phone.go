package storetest

import (
	"context"
	"errors"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// UserCreatePhoneFixture 是一个 UserStore 与一个还没有人占用的手机号。
type UserCreatePhoneFixture struct {
	Store store.UserStore
	Phone string
}

// UserCreatePhoneConflicts 校验建号时的手机号唯一性：号码已被占用时再建号返回
// domain.ErrPhoneNumberOccupied；两个请求并发用同一个号码建号时恰好一个成功，
// 另一个得到同样的错误，而不是内部错误。每个子测试使用自己的号码。
func UserCreatePhoneConflicts(t *testing.T, newFixture func(t *testing.T) UserCreatePhoneFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("sequential", func(t *testing.T) {
		f := newFixture(t)
		first, err := f.Store.Create(ctx, domain.User{AccessHash: 1, Phone: f.Phone, FirstName: "First"})
		if err != nil {
			t.Fatalf("create first owner: %v", err)
		}
		if _, err := f.Store.Create(ctx, domain.User{AccessHash: 2, Phone: f.Phone, FirstName: "Second"}); !errors.Is(err, domain.ErrPhoneNumberOccupied) {
			t.Fatalf("create on occupied phone err=%v, want ErrPhoneNumberOccupied", err)
		}
		owner, found, err := f.Store.ByPhone(ctx, f.Phone)
		if err != nil || !found || owner.ID != first.ID {
			t.Fatalf("phone owner=%+v found=%v err=%v, want first owner %d", owner, found, err, first.ID)
		}
	})

	t.Run("concurrent", func(t *testing.T) {
		f := newFixture(t)
		type result struct {
			user domain.User
			err  error
		}
		const workers = 2
		start := make(chan struct{})
		results := make(chan result, workers)
		for i := 0; i < workers; i++ {
			go func(i int) {
				<-start
				u, err := f.Store.Create(ctx, domain.User{AccessHash: int64(i + 1), Phone: f.Phone, FirstName: "Racer"})
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
			case errors.Is(r.err, domain.ErrPhoneNumberOccupied):
			default:
				t.Fatalf("concurrent create err=%v, want nil or ErrPhoneNumberOccupied", r.err)
			}
		}
		if successes != 1 {
			t.Fatalf("successful creates=%d, want exactly 1", successes)
		}
		owner, found, err := f.Store.ByPhone(ctx, f.Phone)
		if err != nil || !found || owner.ID != winner.ID {
			t.Fatalf("phone owner=%+v found=%v err=%v, want winner %d", owner, found, err, winner.ID)
		}
	})
}

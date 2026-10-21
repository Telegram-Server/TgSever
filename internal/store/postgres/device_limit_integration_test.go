package postgres

import (
	"context"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/store/storetest"
)

// 设备上限与冻结同在 account_restrictions 一行（#104），但只为上限建出来的行不是冻结记录：
// 冻结读路径照旧报告"没有冻结记录"（SetAccountFrozen 的 would_change 等据此判断），第一次
// 冻结仍是 version 1，冻结写入不动上限。迁移前就有的未冻结行（command_id 可能为空）仍是冻结记录。
func TestDeviceLimitOnlyRowIsNotAFreezeRecordPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	auths := NewAuthorizationStore(pool)
	admin := NewAdminStore(pool)

	u, err := users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Limited", Phone: "1996" + randomSuffix(t)})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := auths.SetDeviceLimit(ctx, u.ID, 2); err != nil {
		t.Fatalf("set device limit: %v", err)
	}
	if got, found, err := admin.GetAccountFreeze(ctx, u.ID); err != nil || found {
		t.Fatalf("freeze of a limit-only row = %+v found=%v err=%v, want no freeze record", got, found, err)
	}

	frozen, err := admin.SetAccountFreeze(ctx, domain.AccountFreeze{
		UserID: u.ID, Frozen: true, Since: time.Now().UTC().Add(-time.Minute), Until: time.Now().UTC().Add(time.Hour),
		AppealURL: "https://appeals.example.test/limited", Reason: "review", Actor: "ops", CommandID: "freeze-limited",
	})
	if err != nil {
		t.Fatalf("freeze a limited account: %v", err)
	}
	if frozen.Version != 1 {
		t.Fatalf("first freeze of a limited account version=%d, want 1", frozen.Version)
	}
	if _, err := admin.SetAccountFreeze(ctx, domain.AccountFreeze{UserID: u.ID, Reason: "ok", Actor: "ops", CommandID: "unfreeze-limited"}); err != nil {
		t.Fatalf("unfreeze a limited account: %v", err)
	}
	if got, found, err := admin.GetAccountFreeze(ctx, u.ID); err != nil || !found || got.Frozen || got.CommandID != "unfreeze-limited" {
		t.Fatalf("freeze after unfreeze = %+v found=%v err=%v, want the unfreeze record", got, found, err)
	}
	if limit, found, err := auths.GetDeviceLimit(ctx, u.ID); err != nil || !found || limit != 2 {
		t.Fatalf("device limit after freeze writes = %d found=%v err=%v, want 2", limit, found, err)
	}

	legacy, err := users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Legacy", Phone: "1996" + randomSuffix(t)})
	if err != nil {
		t.Fatalf("create legacy user: %v", err)
	}
	if _, err := pool.Exec(ctx, `INSERT INTO account_restrictions (user_id, frozen) VALUES ($1, false)`, legacy.ID); err != nil {
		t.Fatalf("insert legacy unfrozen row: %v", err)
	}
	if got, found, err := admin.GetAccountFreeze(ctx, legacy.ID); err != nil || !found || got.Frozen {
		t.Fatalf("legacy unfrozen row = %+v found=%v err=%v, want an unfrozen freeze record", got, found, err)
	}
}

// 设置上限与登录绑定拿同一把用户锁：正在进行的登录（持有用户锁的事务）提交之前，设置命令
// 不返回，所以命令返回之后提交的登录一定按新上限计数。
func TestSetDeviceLimitWaitsForAnInFlightLoginPostgres(t *testing.T) {
	pool := testPool(t)
	ctx := context.Background()
	users := NewUserStore(pool)
	auths := NewAuthorizationStore(pool)
	u, err := users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Racing", Phone: "1995" + randomSuffix(t)})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	if err := auths.SetDeviceLimit(ctx, u.ID, 5); err != nil {
		t.Fatalf("set initial device limit: %v", err)
	}

	login, err := pool.Begin(ctx)
	if err != nil {
		t.Fatalf("begin in-flight login: %v", err)
	}
	defer func() { _ = login.Rollback(ctx) }()
	if err := lockUsersForUpdate(ctx, login, u.ID); err != nil {
		t.Fatalf("hold the login user lock: %v", err)
	}

	done := make(chan error, 1)
	go func() { done <- auths.SetDeviceLimit(ctx, u.ID, 1) }()
	select {
	case err := <-done:
		t.Fatalf("SetDeviceLimit returned (err=%v) while a login held the user lock", err)
	case <-time.After(300 * time.Millisecond):
	}
	if err := login.Commit(ctx); err != nil {
		t.Fatalf("commit in-flight login: %v", err)
	}
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("SetDeviceLimit after the login committed: %v", err)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("SetDeviceLimit still blocked after the login committed")
	}
	if limit, found, err := auths.GetDeviceLimit(ctx, u.ID); err != nil || !found || limit != 1 {
		t.Fatalf("device limit = %d found=%v err=%v, want 1", limit, found, err)
	}
}

func TestDeviceLimitContractPostgres(t *testing.T) {
	pool := testPool(t)
	users := NewUserStore(pool)
	auths := NewAuthorizationStore(pool)
	storetest.DeviceLimitContract(t, func(t *testing.T) storetest.DeviceLimitFixture {
		return storetest.DeviceLimitFixture{
			Users:        users,
			AuthKeys:     NewAuthKeyStore(pool),
			Auths:        auths,
			DeviceLimits: auths,
			NewPhone: func(t *testing.T) string {
				return "1997" + randomSuffix(t)
			},
			NewBot: func(t *testing.T) domain.User {
				ctx := context.Background()
				u, err := users.Create(ctx, domain.User{AccessHash: 1, FirstName: "Bot"})
				if err != nil {
					t.Fatalf("create bot user: %v", err)
				}
				if _, err := pool.Exec(ctx, `UPDATE users SET is_bot = true WHERE id = $1`, u.ID); err != nil {
					t.Fatalf("mark bot: %v", err)
				}
				u.Bot = true
				return u
			},
		}
	})
}

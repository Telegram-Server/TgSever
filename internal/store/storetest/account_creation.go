package storetest

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store"
)

// AccountCreationFixture 是同一份底层数据上的建号操作、账号、登录密码与权限设置 store。
// NewPhone 每次返回一个还没有人占用的手机号；DeleteAccount 走该实现的账号注销。
type AccountCreationFixture struct {
	Accounts      store.AccountCreationStore
	Users         store.UserStore
	Passwords     store.LoginPasswordStore
	Permissions   store.AccountPermissionsStore
	NewPhone      func(t *testing.T) string
	DeleteAccount func(t *testing.T, userID int64)
}

// AccountCreationContract 是通用建号操作的契约（#159）：只建号、带登录密码、带权限快照、
// 两者都带时都能读回；权限快照排除已注销的白名单成员；任何一个附加项写入失败都不留下账号，
// 也不占住号码；号码已被占用；并发用同一个号码建号恰好一个成功。
func AccountCreationContract(t *testing.T, newFixture func(t *testing.T) AccountCreationFixture) {
	t.Helper()
	ctx := context.Background()

	t.Run("user only", func(t *testing.T) {
		f := newFixture(t)
		phone := f.NewPhone(t)
		u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: phone, FirstName: "Plain"}, store.AccountExtras{})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		if u.ID == 0 || u.Phone != phone || u.FirstName != "Plain" || u.AccessHash != 7 {
			t.Fatalf("created user = %+v, want id set phone=%s", u, phone)
		}
		assertPhoneOwner(t, f, phone, u.ID)
		if _, found, err := f.Passwords.GetLoginPassword(ctx, u.ID); err != nil || found {
			t.Fatalf("login password found=%v err=%v, want none", found, err)
		}
		if _, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID); err != nil || found {
			t.Fatalf("permissions found=%v err=%v, want none", found, err)
		}
	})

	t.Run("with login password", func(t *testing.T) {
		f := newFixture(t)
		phone := f.NewPhone(t)
		want := testLoginPassword(3)
		u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: phone, FirstName: "Pw"}, store.AccountExtras{LoginPassword: &want})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		assertPhoneOwner(t, f, phone, u.ID)
		assertLoginPassword(t, f.Passwords, u.ID, want)
		if _, found, err := f.Permissions.GetAccountPermissions(ctx, u.ID); err != nil || found {
			t.Fatalf("permissions found=%v err=%v, want none", found, err)
		}
	})

	t.Run("with permissions excludes deleted whitelist members", func(t *testing.T) {
		f := newFixture(t)
		a, b, gone := createAccountCreationUser(t, f), createAccountCreationUser(t, f), createAccountCreationUser(t, f)
		f.DeleteAccount(t, gone.ID)
		snapshot := testPermissionsSnapshot(b.ID, gone.ID, a.ID, a.ID)
		phone := f.NewPhone(t)
		u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: phone, FirstName: "Perm"}, store.AccountExtras{Permissions: &snapshot})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		assertPhoneOwner(t, f, phone, u.ID)
		want := snapshot
		want.UserID = u.ID
		want.SendWhitelist = sortedIDs(a.ID, b.ID)
		assertCreatedPermissions(t, f, want)
		if _, found, err := f.Passwords.GetLoginPassword(ctx, u.ID); err != nil || found {
			t.Fatalf("login password found=%v err=%v, want none", found, err)
		}
	})

	t.Run("with login password and permissions", func(t *testing.T) {
		f := newFixture(t)
		a := createAccountCreationUser(t, f)
		snapshot := testPermissionsSnapshot(a.ID)
		pw := testLoginPassword(5)
		phone := f.NewPhone(t)
		u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: phone, FirstName: "Both"}, store.AccountExtras{LoginPassword: &pw, Permissions: &snapshot})
		if err != nil {
			t.Fatalf("create account: %v", err)
		}
		assertPhoneOwner(t, f, phone, u.ID)
		assertLoginPassword(t, f.Passwords, u.ID, pw)
		want := snapshot
		want.UserID = u.ID
		assertCreatedPermissions(t, f, want)
	})

	t.Run("failed permissions write leaves no account", func(t *testing.T) {
		f := newFixture(t)
		const missingUserID = int64(1) << 50
		snapshot := testPermissionsSnapshot(missingUserID)
		pw := testLoginPassword(6)
		phone := f.NewPhone(t)
		if _, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 7, Phone: phone, FirstName: "Half"}, store.AccountExtras{LoginPassword: &pw, Permissions: &snapshot}); !errors.Is(err, domain.ErrUserNotFound) {
			t.Fatalf("create with a missing whitelist member err=%v, want ErrUserNotFound", err)
		}
		assertNoPhoneOwner(t, f, phone)
		// 失败的尝试不能占住号码：之后仍然可以用这个号码建号。
		if _, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 8, Phone: phone, FirstName: "Whole"}, store.AccountExtras{}); err != nil {
			t.Fatalf("create after failed attempt: %v", err)
		}
	})

	t.Run("failed password write leaves no account", func(t *testing.T) {
		f := newFixture(t)
		incomplete := testLoginPassword(4)
		incomplete.Verifier = nil
		snapshot := testPermissionsSnapshot()
		phone := f.NewPhone(t)
		if _, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 8, Phone: phone, FirstName: "Half"}, store.AccountExtras{LoginPassword: &incomplete, Permissions: &snapshot}); !errors.Is(err, store.ErrLoginPasswordInvalid) {
			t.Fatalf("create with incomplete password err=%v, want ErrLoginPasswordInvalid", err)
		}
		assertNoPhoneOwner(t, f, phone)
		whole := testLoginPassword(5)
		u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 9, Phone: phone, FirstName: "Whole"}, store.AccountExtras{LoginPassword: &whole})
		if err != nil {
			t.Fatalf("create after failed attempt: %v", err)
		}
		assertLoginPassword(t, f.Passwords, u.ID, whole)
	})

	t.Run("occupied phone", func(t *testing.T) {
		f := newFixture(t)
		phone := f.NewPhone(t)
		first, err := f.Users.Create(ctx, domain.User{AccessHash: 1, Phone: phone, FirstName: "First"})
		if err != nil {
			t.Fatalf("create first owner: %v", err)
		}
		pw := testLoginPassword(6)
		snapshot := testPermissionsSnapshot()
		if _, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: 2, Phone: phone, FirstName: "Second"}, store.AccountExtras{LoginPassword: &pw, Permissions: &snapshot}); !errors.Is(err, domain.ErrPhoneNumberOccupied) {
			t.Fatalf("create on occupied phone err=%v, want ErrPhoneNumberOccupied", err)
		}
		if _, found, err := f.Passwords.GetLoginPassword(ctx, first.ID); err != nil || found {
			t.Fatalf("first owner login password found=%v err=%v, want untouched", found, err)
		}
		if _, found, err := f.Permissions.GetAccountPermissions(ctx, first.ID); err != nil || found {
			t.Fatalf("first owner permissions found=%v err=%v, want untouched", found, err)
		}
	})

	t.Run("concurrent create on the same phone", func(t *testing.T) {
		f := newFixture(t)
		phone := f.NewPhone(t)
		type result struct {
			user     domain.User
			password domain.LoginPassword
			err      error
		}
		const workers = 4
		start := make(chan struct{})
		results := make(chan result, workers)
		for i := 0; i < workers; i++ {
			go func(i int) {
				pw := testLoginPassword(byte(10 + i))
				<-start
				u, err := f.Accounts.CreateAccount(ctx, domain.User{AccessHash: int64(i + 1), Phone: phone, FirstName: "Racer"}, store.AccountExtras{LoginPassword: &pw})
				results <- result{user: u, password: pw, err: err}
			}(i)
		}
		close(start)
		var winner result
		successes := 0
		for i := 0; i < workers; i++ {
			r := <-results
			switch {
			case r.err == nil:
				successes++
				winner = r
			case errors.Is(r.err, domain.ErrPhoneNumberOccupied):
			default:
				t.Fatalf("concurrent create err=%v, want nil or ErrPhoneNumberOccupied", r.err)
			}
		}
		if successes != 1 {
			t.Fatalf("successful creates=%d, want exactly 1", successes)
		}
		assertPhoneOwner(t, f, phone, winner.user.ID)
		assertLoginPassword(t, f.Passwords, winner.user.ID, winner.password)
	})
}

// testPermissionsSnapshot 是一份可区分的默认权限快照（不是 DefaultAccountPermissions）。
func testPermissionsSnapshot(whitelist ...int64) domain.AccountPermissions {
	return domain.AccountPermissions{
		Enabled: true,
		Flags: domain.AccountPermissionFlags{
			Send: true, Edit: true, ClearHistory: true, CreateChannel: true, JoinGroup: true, LeaveChannel: true,
		},
		SendWhitelistEnabled: true,
		SendWhitelist:        whitelist,
		CommandID:            "defaults-snapshot",
	}
}

func createAccountCreationUser(t *testing.T, f AccountCreationFixture) domain.User {
	t.Helper()
	created, err := f.Users.Create(context.Background(), domain.User{AccessHash: 1, FirstName: "Member", Phone: f.NewPhone(t)})
	if err != nil {
		t.Fatalf("create user: %v", err)
	}
	return created
}

func assertPhoneOwner(t *testing.T, f AccountCreationFixture, phone string, userID int64) {
	t.Helper()
	owner, found, err := f.Users.ByPhone(context.Background(), phone)
	if err != nil || !found || owner.ID != userID {
		t.Fatalf("phone owner=%+v found=%v err=%v, want %d", owner, found, err, userID)
	}
}

func assertNoPhoneOwner(t *testing.T, f AccountCreationFixture, phone string) {
	t.Helper()
	if owner, found, err := f.Users.ByPhone(context.Background(), phone); err != nil || found {
		t.Fatalf("phone owner after failed create=%+v found=%v err=%v, want no account", owner, found, err)
	}
}

// assertCreatedPermissions 比较除 UpdatedAt 之外的全部字段；nil 与空白名单等价。
func assertCreatedPermissions(t *testing.T, f AccountCreationFixture, want domain.AccountPermissions) {
	t.Helper()
	got, found, err := f.Permissions.GetAccountPermissions(context.Background(), want.UserID)
	if err != nil || !found {
		t.Fatalf("get permissions found=%v err=%v, want found", found, err)
	}
	if got.UpdatedAt.IsZero() {
		t.Fatal("stored permissions UpdatedAt is zero")
	}
	got.UpdatedAt = want.UpdatedAt
	if len(got.SendWhitelist) == 0 {
		got.SendWhitelist = nil
	}
	if len(want.SendWhitelist) == 0 {
		want.SendWhitelist = nil
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("permissions = %+v, want %+v", got, want)
	}
}

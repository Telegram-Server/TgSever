package guestsignup_test

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"telesrv/internal/app/auth"
	"telesrv/internal/app/guestsignup"
	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// 游客注册的功能矩阵在业务层测：开关、IP 绑定的复用与过期、转正后解绑、号码冲突重试、
// 默认名、绕过注册开关。RPC 接线只在 mtprotoedge 的端到端测试里测一条。

type guestSwitch struct {
	enabled atomic.Bool
	days    atomic.Int64
}

func (s *guestSwitch) settings(context.Context) (domain.GuestLoginSettings, error) {
	return domain.GuestLoginSettings{Enabled: s.enabled.Load(), IPBindDays: int(s.days.Load())}, nil
}

type fixture struct {
	svc            *guestsignup.Service
	auth           *auth.Service
	users          *memory.UserStore
	guests         *memory.GuestAccountStore
	loginPasswords *memory.LoginPasswordStore
	passwords      *memory.PasswordStore
	sw             *guestSwitch
	signUpOpen     atomic.Bool

	mu  sync.Mutex
	now time.Time
}

func newFixture(t *testing.T, opts ...guestsignup.Option) *fixture {
	t.Helper()
	f := &fixture{
		users:     memory.NewUserStore(),
		guests:    memory.NewGuestAccountStore(),
		passwords: memory.NewPasswordStore(),
		sw:        &guestSwitch{},
		now:       time.Unix(1_800_000_000, 0),
	}
	f.sw.enabled.Store(true)
	f.sw.days.Store(7)
	f.signUpOpen.Store(true)
	f.guests.SetClock(f.clock)
	f.loginPasswords = memory.NewLoginPasswordStore(f.users)
	accounts := memory.NewAccountCreationStore(f.users, f.loginPasswords, nil).WithGuests(f.guests)
	f.auth = auth.NewService(f.users, memory.NewAuthorizationStore(), memory.NewCodeStore(), nil, nil, "12345",
		auth.WithSignUpAllowed(func(context.Context) (bool, error) { return f.signUpOpen.Load(), nil }),
		auth.WithPasswords(f.passwords),
		auth.WithAccountCreation(accounts))
	opts = append([]guestsignup.Option{guestsignup.WithClock(f.clock)}, opts...)
	f.svc = guestsignup.NewService(guestsignup.Deps{
		Settings:       f.sw.settings,
		Users:          f.users,
		Guests:         f.guests,
		Accounts:       accounts,
		LoginPasswords: f.loginPasswords,
		Passwords:      f.passwords,
		Flows:          f.auth,
	}, opts...)
	return f
}

func (f *fixture) clock() time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.now
}

func (f *fixture) advance(d time.Duration) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.now = f.now.Add(d)
}

var authKeySeq atomic.Int64

// newAuth 每次返回一个新设备的授权（不同的 auth key）。
func newAuth() domain.Authorization {
	n := authKeySeq.Add(1)
	var key [8]byte
	for i := range key {
		key[i] = byte(n >> (8 * i))
	}
	return domain.Authorization{AuthKeyID: key}
}

func (f *fixture) signUp(t *testing.T, ip, first, last string) domain.User {
	t.Helper()
	u, err := f.svc.SignUp(context.Background(), newAuth(), ip, first, last)
	if err != nil {
		t.Fatalf("guest sign-up from %s: %v", ip, err)
	}
	return u
}

func TestGuestSignUpDisabled(t *testing.T) {
	f := newFixture(t)
	f.sw.enabled.Store(false)
	if _, err := f.svc.SignUp(context.Background(), newAuth(), "203.0.113.1", "", ""); !errors.Is(err, domain.ErrGuestLoginDisabled) {
		t.Fatalf("disabled err = %v, want ErrGuestLoginDisabled", err)
	}
	if _, found, _ := f.guests.GuestIPBinding(context.Background(), "203.0.113.1"); found {
		t.Fatal("disabled guest sign-up left an ip binding")
	}
}

func TestGuestSignUpSettingsErrorFailsClosed(t *testing.T) {
	f := newFixture(t)
	boom := errors.New("identity unreadable")
	svc := guestsignup.NewService(guestsignup.Deps{
		Settings: func(context.Context) (domain.GuestLoginSettings, error) { return domain.GuestLoginSettings{}, boom },
		Users:    f.users, Guests: f.guests, Accounts: memory.NewAccountCreationStore(f.users, nil, nil).WithGuests(f.guests), Flows: f.auth,
	})
	if _, err := svc.SignUp(context.Background(), newAuth(), "203.0.113.2", "", ""); !errors.Is(err, boom) {
		t.Fatalf("settings error err = %v, want it passed through", err)
	}
}

func TestGuestSignUpRequiresClientIP(t *testing.T) {
	f := newFixture(t)
	for _, ip := range []string{"", "not-an-ip", "unix"} {
		if _, err := f.svc.SignUp(context.Background(), newAuth(), ip, "", ""); !errors.Is(err, domain.ErrGuestIPUnavailable) {
			t.Fatalf("ip %q err = %v, want ErrGuestIPUnavailable", ip, err)
		}
	}
}

func TestGuestSignUpCreatesAccountInGuestRange(t *testing.T) {
	f := newFixture(t)
	u := f.signUp(t, "203.0.113.10", "", "")
	if !guestsignup.IsGuestNumber(u.Phone) {
		t.Fatalf("phone %q is not in the guest range", u.Phone)
	}
	if !domain.ValidPhone(u.Phone) {
		t.Fatalf("phone %q is not a valid login identity", u.Phone)
	}
	if utf8.RuneCountInString(u.FirstName) < 3 || u.LastName != "" {
		t.Fatalf("generated name = %q %q, want a random adjective-noun name", u.FirstName, u.LastName)
	}
	account, found, err := f.guests.GuestAccount(context.Background(), u.ID)
	if err != nil || !found || account.Phone != u.Phone || account.IPKey != "203.0.113.10" {
		t.Fatalf("guest marker = %+v found=%v err=%v", account, found, err)
	}
}

func TestGuestSignUpKeepsClientName(t *testing.T) {
	f := newFixture(t)
	u := f.signUp(t, "203.0.113.11", "  Alice ", "Smith")
	if u.FirstName != "Alice" || u.LastName != "Smith" {
		t.Fatalf("name = %q %q, want Alice Smith", u.FirstName, u.LastName)
	}
	// 只给 last_name 时随机生成 first_name，last_name 保留。
	v := f.signUp(t, "203.0.113.12", "", "Smith")
	if v.FirstName == "" || v.LastName != "Smith" {
		t.Fatalf("name = %q %q, want generated first name and Smith", v.FirstName, v.LastName)
	}
}

// 同一 IP 在绑定天数内再来，登录的是同一个账号，不管客户端这次带了什么名字。
func TestGuestSignUpSameIPReusesAccountWithinWindow(t *testing.T) {
	f := newFixture(t)
	first := f.signUp(t, "203.0.113.20", "Alice", "")
	f.advance(6 * 24 * time.Hour)
	again := f.signUp(t, "203.0.113.20", "Bob", "")
	if again.ID != first.ID || again.FirstName != "Alice" {
		t.Fatalf("second sign-up = %+v, want the same account %d", again, first.ID)
	}
	other := f.signUp(t, "203.0.113.21", "", "")
	if other.ID == first.ID {
		t.Fatal("a different ip got the same account")
	}
}

func TestGuestSignUpWindowExpiryAndDaysChange(t *testing.T) {
	f := newFixture(t)
	first := f.signUp(t, "203.0.113.30", "", "")
	f.advance(7 * 24 * time.Hour)
	second := f.signUp(t, "203.0.113.30", "", "")
	if second.ID == first.ID {
		t.Fatal("binding older than the window was reused")
	}
	// 新账号重新开始计时；后台把天数调大后，旧一些的绑定也继续有效。
	f.advance(10 * 24 * time.Hour)
	f.sw.days.Store(30)
	if third := f.signUp(t, "203.0.113.30", "", ""); third.ID != second.ID {
		t.Fatalf("after raising the window got %d, want %d", third.ID, second.ID)
	}
	binding, _, _ := f.guests.GuestIPBinding(context.Background(), "203.0.113.30")
	if binding.UserID != second.ID {
		t.Fatalf("binding = %+v, want user %d", binding, second.ID)
	}
}

// 转正后解绑：账号设了登录密码或 2FA、换绑了号码、注销后，同一 IP 拿到的是新账号。
func TestGuestSignUpPromotedAccountIsNotReused(t *testing.T) {
	ctx := context.Background()
	cases := []struct {
		name    string
		promote func(t *testing.T, f *fixture, u domain.User)
	}{
		{"login password", func(t *testing.T, f *fixture, u domain.User) {
			if err := f.loginPasswords.SetLoginPassword(ctx, u.ID, domain.LoginPassword{Salt1: []byte{1}, Salt2: []byte{2}, Verifier: []byte{3}}); err != nil {
				t.Fatal(err)
			}
		}},
		{"two-step password", func(t *testing.T, f *fixture, u domain.User) {
			if err := f.passwords.Save(ctx, u.ID, domain.PasswordSettings{HasPassword: true}); err != nil {
				t.Fatal(err)
			}
		}},
		{"phone changed", func(t *testing.T, f *fixture, u domain.User) {
			if _, err := f.users.UpdatePhone(ctx, u.ID, "15550377001"); err != nil {
				t.Fatal(err)
			}
		}},
		{"account deleted", func(t *testing.T, f *fixture, u domain.User) {
			lifecycle := memory.NewAccountLifecycleStore(f.users, f.loginPasswords)
			if _, err := lifecycle.ExecuteAccountDeletion(ctx, u.ID, domain.AccountDeletionManual, "guest test", time.Now()); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			f := newFixture(t)
			const ip = "203.0.113.40"
			first := f.signUp(t, ip, "", "")
			tc.promote(t, f, first)
			second := f.signUp(t, ip, "", "")
			if second.ID == first.ID {
				t.Fatal("promoted guest account was handed out again")
			}
			if third := f.signUp(t, ip, "", ""); third.ID != second.ID {
				t.Fatalf("new binding not reused: got %d, want %d", third.ID, second.ID)
			}
		})
	}
}

func TestGuestSignUpIPv6SharesSlash64(t *testing.T) {
	f := newFixture(t)
	a := f.signUp(t, "2001:db8:1:2::10", "", "")
	b := f.signUp(t, "2001:db8:1:2:ffff::1", "", "")
	if a.ID != b.ID {
		t.Fatal("two addresses of one /64 got different accounts")
	}
	if c := f.signUp(t, "2001:db8:1:3::10", "", ""); c.ID == a.ID {
		t.Fatal("a different /64 got the same account")
	}
	// IPv4 映射的 IPv6 地址与 IPv4 本身是同一个键。
	v4 := f.signUp(t, "198.51.100.7", "", "")
	if mapped := f.signUp(t, "::ffff:198.51.100.7", "", ""); mapped.ID != v4.ID {
		t.Fatal("ipv4-mapped address got a different account")
	}
}

// 游客注册绕过注册开关，但普通注册仍被拦住。
func TestGuestSignUpBypassesSignUpSwitch(t *testing.T) {
	f := newFixture(t)
	f.signUpOpen.Store(false)
	u := f.signUp(t, "203.0.113.50", "", "")
	if u.ID == 0 {
		t.Fatal("guest sign-up with sign-up closed did not create an account")
	}
	if err := f.auth.CheckSignUpAllowed(context.Background()); !errors.Is(err, domain.ErrSignUpDisabled) {
		t.Fatalf("ordinary sign-up gate err = %v, want ErrSignUpDisabled", err)
	}
}

func TestGuestSignUpRetriesOccupiedNumber(t *testing.T) {
	var calls atomic.Int64
	numbers := []string{"12015550100", "12015550100", "12015550101"}
	f := newFixture(t, guestsignup.WithGenerators(func() (string, error) {
		return numbers[int(calls.Add(1)-1)%len(numbers)], nil
	}, nil))
	first := f.signUp(t, "203.0.113.60", "", "")
	second := f.signUp(t, "203.0.113.61", "", "")
	if first.Phone != "12015550100" || second.Phone != "12015550101" {
		t.Fatalf("phones = %s, %s; want the occupied number skipped", first.Phone, second.Phone)
	}
}

func TestGuestSignUpNumberPoolExhausted(t *testing.T) {
	f := newFixture(t, guestsignup.WithGenerators(func() (string, error) { return "12015550199", nil }, nil))
	f.signUp(t, "203.0.113.70", "", "")
	if _, err := f.svc.SignUp(context.Background(), newAuth(), "203.0.113.71", "", ""); !errors.Is(err, domain.ErrGuestNumberPoolExhausted) {
		t.Fatalf("err = %v, want ErrGuestNumberPoolExhausted", err)
	}
	if _, found, _ := f.guests.GuestIPBinding(context.Background(), "203.0.113.71"); found {
		t.Fatal("failed sign-up left an ip binding")
	}
}

// 同一 IP 并发游客注册只建一个账号，其余请求登录它。
func TestGuestSignUpConcurrentSameIP(t *testing.T) {
	f := newFixture(t)
	const workers = 8
	start := make(chan struct{})
	ids := make(chan int64, workers)
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		go func() {
			<-start
			u, err := f.svc.SignUp(context.Background(), newAuth(), "203.0.113.80", "", "")
			if err != nil {
				errs <- err
				return
			}
			ids <- u.ID
		}()
	}
	close(start)
	seen := map[int64]bool{}
	for i := 0; i < workers; i++ {
		select {
		case err := <-errs:
			t.Fatalf("concurrent guest sign-up: %v", err)
		case id := <-ids:
			seen[id] = true
		}
	}
	if len(seen) != 1 {
		t.Fatalf("concurrent sign-ups created %d accounts, want 1", len(seen))
	}
}

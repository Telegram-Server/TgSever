package rpc

import (
	"context"
	"sync"
	"testing"
	"time"

	"github.com/iamxvbaba/td/bin"
	"github.com/iamxvbaba/td/clock"
	"github.com/iamxvbaba/td/tg"
	"go.uber.org/zap/zaptest"

	appauth "telesrv/internal/app/auth"
	"telesrv/internal/domain"
	"telesrv/internal/store"
	"telesrv/internal/store/memory"
)

// settableClock 是可手动推进的测试时钟。
type settableClock struct {
	mu  sync.Mutex
	now time.Time
}

func (c *settableClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}
func (c *settableClock) advance(d time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(d)
	c.mu.Unlock()
}
func (c *settableClock) Timer(d time.Duration) clock.Timer   { return clock.System.Timer(d) }
func (c *settableClock) Ticker(d time.Duration) clock.Ticker { return clock.System.Ticker(d) }

// presenceSessionContext 模拟一次 RPC 的身份：raw key（iOS 是临时 key）+ session，
// AuthKeyIDFrom 是它绑定的授权（perm key）。
func presenceSessionContext(userID int64, rawKey, permKey [8]byte, sessionID int64) context.Context {
	ctx := WithRawAuthKeyID(context.Background(), rawKey)
	ctx = WithAuthKeyID(ctx, permKey)
	ctx = WithSessionID(ctx, sessionID)
	return WithUserID(ctx, userID)
}

func newPresenceRouter(t *testing.T) (*Router, *settableClock) {
	t.Helper()
	clk := &settableClock{now: time.Unix(1_800_000_000, 0)}
	return New(Config{}, Deps{}, zaptest.NewLogger(t), clk), clk
}

func updateStatus(t *testing.T, r *Router, ctx context.Context, offline bool) {
	t.Helper()
	if ok, err := r.onAccountUpdateStatus(ctx, offline); err != nil || !ok {
		t.Fatalf("updateStatus(offline=%v) = %v, %v", offline, ok, err)
	}
}

func TestAuthorizationInForegroundMapsTempKeyToAuthorization(t *testing.T) {
	r, _ := newPresenceRouter(t)
	perm, temp := [8]byte{0xA1}, [8]byte{0x7E}
	updateStatus(t, r, presenceSessionContext(42, temp, perm, 1), false)
	if !r.AuthorizationInForeground(perm) {
		t.Fatal("authorization not in foreground after updateStatus(offline=false) on its temp key")
	}
	if r.AuthorizationInForeground(temp) {
		t.Fatal("the raw temp key must not be treated as an authorization")
	}
	updateStatus(t, r, presenceSessionContext(42, temp, perm, 1), true)
	if r.AuthorizationInForeground(perm) {
		t.Fatal("authorization still in foreground after updateStatus(offline=true)")
	}
}

func TestAuthorizationInForegroundAnySessionOfTheAuthorization(t *testing.T) {
	r, _ := newPresenceRouter(t)
	perm := [8]byte{0xA2}
	app := presenceSessionContext(42, [8]byte{0x01}, perm, 1)
	ext := presenceSessionContext(42, [8]byte{0x02}, perm, 2)
	updateStatus(t, r, app, true)
	updateStatus(t, r, ext, false)
	if !r.AuthorizationInForeground(perm) {
		t.Fatal("one online session of the authorization should make it foreground")
	}
	updateStatus(t, r, ext, true)
	if r.AuthorizationInForeground(perm) {
		t.Fatal("all sessions reported offline, want not foreground")
	}
	// 另一个授权（同一用户的另一台设备）在线不影响。
	updateStatus(t, r, presenceSessionContext(42, [8]byte{0x03}, [8]byte{0xB0}, 3), false)
	if r.AuthorizationInForeground(perm) {
		t.Fatal("another authorization of the same user leaked into this one")
	}
}

func TestAuthorizationInForegroundExpiresAndClearsOnDisconnect(t *testing.T) {
	r, clk := newPresenceRouter(t)
	perm, raw := [8]byte{0xA3}, [8]byte{0x09}
	if r.AuthorizationInForeground(perm) {
		t.Fatal("no session record, want not foreground")
	}
	updateStatus(t, r, presenceSessionContext(42, raw, perm, 1), false)
	clk.advance(userOnlineTTL + time.Second)
	if r.AuthorizationInForeground(perm) {
		t.Fatal("online report expired, want not foreground")
	}
	updateStatus(t, r, presenceSessionContext(42, raw, perm, 1), false)
	r.SessionOfflineAt(raw, 1, 42, true, int(clk.Now().Unix()))
	if r.AuthorizationInForeground(perm) {
		t.Fatal("session disconnected, want not foreground")
	}
}

func TestAuthorizationInForegroundIgnoresImplicitSessionOnline(t *testing.T) {
	r, _ := newPresenceRouter(t)
	perm := [8]byte{0xA4}
	ctx := presenceSessionContext(42, [8]byte{0x04}, perm, 1)
	// 新连接绑定用户时的隐式上线不是 updateStatus 上报：还没上报就按不在前台处理。
	r.setPresenceFromContext(ctx, 42, false, presencePersistAsync, presenceReportNone)
	if r.AuthorizationInForeground(perm) {
		t.Fatal("implicit session online counted as a foreground report")
	}
	// 用户级在线状态照旧：隐式上线仍让用户显示在线。
	if status, ok := r.presence.statusFor(42, int(r.clock.Now().Unix())); !ok || status.Kind != domain.UserStatusOnline {
		t.Fatalf("user status = %+v ok=%v, want online", status, ok)
	}
	updateStatus(t, r, ctx, false)
	// 隐式续期不会把已有的前台上报冲掉。
	r.setPresenceFromContext(ctx, 42, false, presencePersistAsync, presenceReportNone)
	if !r.AuthorizationInForeground(perm) {
		t.Fatal("implicit refresh erased the explicit foreground report")
	}
}

func TestUserPresenceAggregateUnchangedByAuthorizationTracking(t *testing.T) {
	r, _ := newPresenceRouter(t)
	now := int(r.clock.Now().Unix())
	updateStatus(t, r, presenceSessionContext(42, [8]byte{0x05}, [8]byte{0xA5}, 1), true)
	updateStatus(t, r, presenceSessionContext(42, [8]byte{0x06}, [8]byte{0xA6}, 2), false)
	status, ok := r.presence.statusFor(42, now)
	if !ok || status.Kind != domain.UserStatusOnline || status.Expires != now+int(userOnlineTTL/time.Second) {
		t.Fatalf("user status = %+v ok=%v, want online while any session is online", status, ok)
	}
	updateStatus(t, r, presenceSessionContext(42, [8]byte{0x06}, [8]byte{0xA6}, 2), true)
	status, ok = r.presence.statusFor(42, now)
	if !ok || status.Kind != domain.UserStatusOffline || status.WasOnline != now {
		t.Fatalf("user status = %+v ok=%v, want offline with was_online=now", status, ok)
	}
}

// 决策 9：updateStatus(offline=true) 之后同一会话上的后续 RPC 不会把它改回在线。
func TestUpdateStatusOfflineSurvivesLaterTrafficOnTheSession(t *testing.T) {
	ctx := context.Background()
	authKeyID := [8]byte{0x95, 0x09}
	authKeys := memory.NewAuthKeyStore()
	authorizations := memory.NewAuthorizationStore()
	authorizations.LinkAuthKeyAuthority(authKeys)
	users := memory.NewUserStore()
	user, err := users.Create(ctx, domain.User{Phone: "15550009509", FirstName: "Bg"})
	if err != nil {
		t.Fatal(err)
	}
	if err := authKeys.Save(ctx, store.AuthKeyData{ID: authKeyID}); err != nil {
		t.Fatal(err)
	}
	if err := authorizations.Bind(ctx, domain.Authorization{AuthKeyID: authKeyID, UserID: user.ID, Hash: 9509}); err != nil {
		t.Fatal(err)
	}
	sessions := &captureSessions{}
	r := New(Config{}, Deps{
		Auth:     appauth.NewService(users, authorizations, nil, authKeys, nil, "12345"),
		Users:    staticUsersService{user: user},
		Sessions: sessions,
	}, zaptest.NewLogger(t), clock.System)
	send := func(req bin.Encoder) {
		t.Helper()
		var buf bin.Buffer
		if err := req.Encode(&buf); err != nil {
			t.Fatal(err)
		}
		_, _ = r.Dispatch(ctx, authKeyID, 1, &buf)
	}
	send(&tg.AccountUpdateStatusRequest{Offline: false})
	if !r.AuthorizationInForeground(authKeyID) {
		t.Fatal("updateStatus(offline=false) did not mark the authorization foreground")
	}
	send(&tg.AccountUpdateStatusRequest{Offline: true})
	for range 3 {
		send(&tg.AccountGetAuthorizationsRequest{})
		send(&tg.UsersGetUsersRequest{ID: []tg.InputUserClass{&tg.InputUserSelf{}}})
	}
	if r.AuthorizationInForeground(authKeyID) {
		t.Fatal("later traffic flipped the backgrounded session back to foreground")
	}
	if status, ok := r.presence.statusFor(user.ID, int(time.Now().Unix())); !ok || status.Kind != domain.UserStatusOffline {
		t.Fatalf("user status after traffic = %+v ok=%v, want still offline", status, ok)
	}
}

package rpc

import (
	"testing"
	"time"
)

// App 一直在前台、只是超过在线 TTL 没有新的 updateStatus：在线过期期间不算前台，
// 之后同一会话的隐式续期（任意 RPC）让它重新算前台——最近一次显式上报仍是 offline=false。
func TestAuthorizationForegroundReturnsAfterExpiryOnImplicitRenewal(t *testing.T) {
	r, clk := newPresenceRouter(t)
	perm := [8]byte{0xA7}
	ctx := presenceSessionContext(42, [8]byte{0x07}, perm, 1)
	updateStatus(t, r, ctx, false)

	clk.advance(userOnlineTTL + time.Second)
	// 在线过期清扫把条目降级为 offline。
	r.presence.expireOnline(int(clk.Now().Unix()))
	if r.AuthorizationInForeground(perm) {
		t.Fatal("expired online entry counted as foreground")
	}
	r.setPresenceFromContext(ctx, 42, false, presencePersistAsync, presenceReportNone)
	if !r.AuthorizationInForeground(perm) {
		t.Fatal("implicit renewal after expiry lost the explicit foreground report; the app would get banners while open")
	}
}

// 最近一次显式上报是 offline=true 时，过期后的隐式续期仍不算前台。
func TestAuthorizationBackgroundStaysAfterExpiryOnImplicitRenewal(t *testing.T) {
	r, clk := newPresenceRouter(t)
	perm := [8]byte{0xA8}
	ctx := presenceSessionContext(42, [8]byte{0x08}, perm, 1)
	updateStatus(t, r, ctx, false)
	updateStatus(t, r, ctx, true)

	clk.advance(userOnlineTTL + time.Second)
	r.presence.expireOnline(int(clk.Now().Unix()))
	r.setPresenceFromContext(ctx, 42, false, presencePersistAsync, presenceReportNone)
	if r.AuthorizationInForeground(perm) {
		t.Fatal("implicit renewal turned a backgrounded session into foreground")
	}
}

package rpc

import (
	"testing"

	"telesrv/internal/app/push"
)

type callPushFunc func(push.IncomingCall) bool

func (f callPushFunc) IncomingCall(c push.IncomingCall) bool { return f(c) }

// 注入后可取出；再注入 nil 即关闭。
func TestNotifierSlotSetAndDisable(t *testing.T) {
	var slot notifierSlot[CallPushNotifier]
	if _, ok := slot.load(); ok {
		t.Fatal("empty slot loaded a notifier")
	}
	calls := 0
	slot.set(callPushFunc(func(push.IncomingCall) bool { calls++; return true }))
	n, ok := slot.load()
	if !ok {
		t.Fatal("injected notifier not loaded")
	}
	n.IncomingCall(push.IncomingCall{})
	if calls != 1 {
		t.Fatalf("calls = %d, want 1", calls)
	}
	slot.set(nil)
	if _, ok := slot.load(); ok {
		t.Fatal("nil did not disable the slot")
	}
}

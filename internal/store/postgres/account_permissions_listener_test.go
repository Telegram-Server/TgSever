package postgres

import (
	"context"
	"testing"
)

type fakeAccountPermissionsCache struct {
	invalidated []int64
	flushes     int
}

func (c *fakeAccountPermissionsCache) InvalidateAccountPermissions(userID int64) {
	c.invalidated = append(c.invalidated, userID)
}

func (c *fakeAccountPermissionsCache) FlushAccountPermissions() { c.flushes++ }

func TestReadModelChangeListenerRoutesAccountPermissions(t *testing.T) {
	cache := &fakeAccountPermissionsCache{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{AccountPermissions: cache}, nil)
	listener.handlePayload(`{"model":"account_permissions","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2,"hash":101}`)
	listener.handlePayload(`{"model":"account_permissions","owner_user_id":0,"peer_type":"channel","peer_id":888,"version":3,"hash":102}`)
	listener.handlePayload(`{"model":"account_permissions","owner_user_id":0,"peer_type":"user","peer_id":0,"version":4,"hash":103}`)
	if len(cache.invalidated) != 1 || cache.invalidated[0] != 777 {
		t.Fatalf("permission invalidations = %v, want [777]", cache.invalidated)
	}
	listener.flush("test")
	if cache.flushes != 1 {
		t.Fatalf("permission flushes = %d, want 1", cache.flushes)
	}
}

type fakeAccountPermissionsNotifier struct{ notified []int64 }

func (n *fakeAccountPermissionsNotifier) NotifyAccountPermissionsChanged(_ context.Context, userID int64) error {
	n.notified = append(n.notified, userID)
	return nil
}

// 权限是在另一个实例上改的：本实例的监听器除了失效缓存，还要向本实例上该账号的会话推送
// updateConfig，否则连在这里的客户端要等到下一次定时刷新 appConfig 才更新界面。
func TestReadModelChangeListenerPushesConfigForAccountPermissions(t *testing.T) {
	cache := &fakeAccountPermissionsCache{}
	notifier := &fakeAccountPermissionsNotifier{}
	listener := NewReadModelChangeListener("", ReadModelCacheSet{AccountPermissions: cache, AccountPermissionsNotifier: notifier}, nil)
	listener.handlePayload(`{"model":"account_permissions","owner_user_id":0,"peer_type":"user","peer_id":777,"version":2,"hash":101}`)
	listener.handlePayload(`{"model":"account_permissions","owner_user_id":0,"peer_type":"user","peer_id":0,"version":3,"hash":102}`)
	if len(notifier.notified) != 1 || notifier.notified[0] != 777 {
		t.Fatalf("updateConfig pushes = %v, want [777]", notifier.notified)
	}
	if len(cache.invalidated) != 1 || cache.invalidated[0] != 777 {
		t.Fatalf("permission invalidations = %v, want [777]", cache.invalidated)
	}
}

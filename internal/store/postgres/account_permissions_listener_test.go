package postgres

import "testing"

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

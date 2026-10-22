package help

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"telesrv/internal/domain"
)

type fakeAccountPermissionsProvider struct {
	items map[int64]domain.AccountPermissions
}

func (p *fakeAccountPermissionsProvider) EnforcedAccountPermissions(_ context.Context, userID int64) (domain.AccountPermissions, bool, error) {
	item, ok := p.items[userID]
	if !ok || !item.Enabled {
		return domain.AccountPermissions{}, false, nil
	}
	return item, true, nil
}

func TestAppConfigPermissionsOverlay(t *testing.T) {
	ctx := context.Background()
	provider := &fakeAccountPermissionsProvider{items: map[int64]domain.AccountPermissions{}}
	baseline := NewService(nil, nil)
	svc := NewService(nil, nil, WithAccountPermissionsProvider(provider))

	base, _, err := baseline.GetAppConfig(ctx, 1001, 0)
	if err != nil {
		t.Fatal(err)
	}
	unset, _, err := svc.GetAppConfig(ctx, 1001, 0)
	if err != nil {
		t.Fatal(err)
	}
	if unset.Hash != base.Hash || !bytes.Equal(unset.JSON, base.JSON) {
		t.Fatal("app config without a permissions record differs from the baseline")
	}

	flags := domain.AccountPermissionFlags{Send: true, Edit: true, JoinChannel: true, LeaveChannel: true}
	provider.items[1001] = domain.AccountPermissions{UserID: 1001, Enabled: true, Flags: flags, SendWhitelistEnabled: true, SendWhitelist: []int64{123, 456}}
	on, notModified, err := svc.GetAppConfig(ctx, 1001, base.Hash)
	if err != nil || notModified || on.Hash == base.Hash {
		t.Fatalf("enabled config hash=%d base=%d notModified=%v err=%v", on.Hash, base.Hash, notModified, err)
	}
	var values map[string]json.RawMessage
	if err := json.Unmarshal(on.JSON, &values); err != nil {
		t.Fatal(err)
	}
	var got any
	if err := json.Unmarshal(values["telesrv_permissions"], &got); err != nil {
		t.Fatalf("telesrv_permissions = %s: %v", values["telesrv_permissions"], err)
	}
	var want any
	if err := json.Unmarshal([]byte(`{
		"send": true, "delete": false, "edit": true, "clear_history": false,
		"create_group": false, "create_channel": false,
		"join_group": false, "join_channel": true,
		"leave_group": false, "leave_channel": true,
		"add_contact": false,
		"send_whitelist": {"enabled": true, "user_ids": [123, 456]}
	}`), &want); err != nil {
		t.Fatal(err)
	}
	if gotJSON, _ := json.Marshal(got); string(gotJSON) != mustJSON(t, want) {
		t.Fatalf("telesrv_permissions = %s, want %s", gotJSON, mustJSON(t, want))
	}
	if other, _, err := svc.GetAppConfig(ctx, 1002, 0); err != nil || other.Hash != base.Hash {
		t.Fatalf("other account hash=%d base=%d err=%v", other.Hash, base.Hash, err)
	}

	changed := provider.items[1001]
	changed.Flags.Edit = false
	provider.items[1001] = changed
	edited, notModified, err := svc.GetAppConfig(ctx, 1001, on.Hash)
	if err != nil || notModified || edited.Hash == on.Hash {
		t.Fatalf("changed permissions hash=%d previous=%d notModified=%v err=%v", edited.Hash, on.Hash, notModified, err)
	}

	changed.Enabled = false
	provider.items[1001] = changed
	off, notModified, err := svc.GetAppConfig(ctx, 1001, edited.Hash)
	if err != nil || notModified || off.Hash != base.Hash || !bytes.Equal(off.JSON, base.JSON) {
		t.Fatalf("switched off config hash=%d base=%d notModified=%v err=%v", off.Hash, base.Hash, notModified, err)
	}
}

func mustJSON(t *testing.T, v any) string {
	t.Helper()
	out, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(out)
}

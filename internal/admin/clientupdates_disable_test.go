package admin

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/updatecdn"
)

// linuxStableEntry 是停用/启用替身返回的条目。
func linuxStableEntry(disabled, unchanged bool) updatecdn.SetDisabledResult {
	return updatecdn.SetDisabledResult{
		Platform: "linux",
		Channel:  "stable",
		Release: updatecdn.DesktopRelease{
			Build: 6000000, Version: "6.0", File: "td-update-linux-x64-6000000", SHA256: "ab12", Size: 4096, Disabled: disabled,
		},
		Unchanged: unchanged,
	}
}

func entryRequest(id, platform, channel string) ClientUpdateEntryRequest {
	return ClientUpdateEntryRequest{CommandMeta: CommandMeta{CommandID: id, Actor: "ops", Reason: "crashes on start"}, Platform: platform, Channel: channel}
}

func TestDisableClientUpdateDisablesAndJournals(t *testing.T) {
	svc, catalog, repo := clientUpdateService(t)
	catalog.setResult = linuxStableEntry(true, false)

	result, err := svc.DisableClientUpdate(context.Background(), entryRequest("dis-1", " linux ", "stable"))
	if err != nil {
		t.Fatalf("DisableClientUpdate() error = %v", err)
	}
	if len(catalog.setDisabled) != 1 || catalog.setDisabled[0] != (fakeSetDisabledCall{platform: "linux", channel: "stable", disabled: true}) {
		t.Fatalf("catalog calls = %+v, want one real disable of linux.stable", catalog.setDisabled)
	}
	if result.Action != ActionDisableClientUpdate || result.DryRun || result.Details["build"] != uint64(6000000) {
		t.Fatalf("result = %+v", result)
	}
	cmd, journalled := journalledResult(t, repo, "dis-1")
	if cmd.Status != domain.AdminCommandCompleted || cmd.Action != ActionDisableClientUpdate {
		t.Fatalf("journalled command = %+v", cmd)
	}
	for key, want := range map[string]any{
		"platform": "linux", "channel": "stable", "build": float64(6000000), "file": "td-update-linux-x64-6000000",
		"sha256": "ab12", "disabled": true, "unchanged": false,
	} {
		if got := journalled.Details[key]; got != want {
			t.Errorf("journalled details[%s] = %#v, want %#v", key, got, want)
		}
	}
}

func TestDisableClientUpdateAlreadyDisabledIsUnchanged(t *testing.T) {
	svc, catalog, repo := clientUpdateService(t)
	catalog.setResult = linuxStableEntry(true, true)

	result, err := svc.DisableClientUpdate(context.Background(), entryRequest("dis-2", "linux", "stable"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Details["unchanged"] != true || !strings.Contains(result.Message, "nothing to do") {
		t.Fatalf("result = %+v, want an unchanged no-op", result)
	}
	if _, journalled := journalledResult(t, repo, "dis-2"); journalled.Details["unchanged"] != true {
		t.Fatalf("journalled details = %#v", journalled.Details)
	}
}

func TestEnableClientUpdateEnablesAndPreviews(t *testing.T) {
	svc, catalog, repo := clientUpdateService(t)
	catalog.setResult = linuxStableEntry(false, false)

	req := entryRequest("dry-en", "linux", "stable")
	req.DryRun = true
	preview, err := svc.EnableClientUpdate(context.Background(), req)
	if err != nil || !preview.DryRun || preview.Action != ActionEnableClientUpdate {
		t.Fatalf("dry run = %+v, %v", preview, err)
	}
	result, err := svc.EnableClientUpdate(context.Background(), entryRequest("en-1", "linux", "stable"))
	if err != nil {
		t.Fatalf("EnableClientUpdate() error = %v", err)
	}
	want := []fakeSetDisabledCall{
		{platform: "linux", channel: "stable", disabled: false, dryRun: true},
		{platform: "linux", channel: "stable", disabled: false},
	}
	if fmt.Sprint(catalog.setDisabled) != fmt.Sprint(want) {
		t.Fatalf("catalog calls = %+v, want %+v", catalog.setDisabled, want)
	}
	if result.Details["disabled"] != false {
		t.Fatalf("result = %+v", result)
	}
	if cmd, _ := journalledResult(t, repo, "en-1"); cmd.Status != domain.AdminCommandCompleted || cmd.Action != ActionEnableClientUpdate {
		t.Fatalf("journalled command = %+v", cmd)
	}
}

func TestEnableClientUpdateRequiresReason(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	for _, reason := range []string{"", "   "} {
		req := entryRequest("en-noreason", "linux", "stable")
		req.Reason = reason
		_, err := svc.EnableClientUpdate(context.Background(), req)
		if err == nil || !strings.Contains(err.Error(), "reason") {
			t.Fatalf("enable with reason %q: error = %v", reason, err)
		}
	}
	if len(catalog.setDisabled) != 0 {
		t.Fatal("enable without a reason reached the catalog")
	}
}

func TestClientUpdateEntryRequestNeedsPlatformAndChannel(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	if _, err := svc.DisableClientUpdate(context.Background(), entryRequest("dis-empty", "linux", " ")); err == nil {
		t.Fatal("disable without a channel succeeded")
	}
	if len(catalog.setDisabled) != 0 {
		t.Fatal("an empty channel reached the catalog")
	}
}

func TestClientUpdateEntryFailuresAreCodedAndJournalled(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"not found": {fmt.Errorf("desktop.win64.stable: %w", updatecdn.ErrDesktopEntryNotFound), CodeUpdateEntryNotFound},
		"locked":    {fmt.Errorf("%w (manifest.json.lock exists)", updatecdn.ErrPublishInProgress), CodeUpdatePublishInProgress},
		"invalid":   {errors.New(`new manifest is invalid: open package "td-update-linux-x64-6000000"`), CodeUpdatePackageRejected},
	} {
		t.Run(name, func(t *testing.T) {
			svc, catalog, repo := clientUpdateService(t)
			catalog.setErr = tc.err
			result, err := svc.EnableClientUpdate(context.Background(), entryRequest("en-fail", "linux", "stable"))
			if err == nil || result.Code != tc.code || ErrorCode(err) != tc.code {
				t.Fatalf("result = %+v, err = %v; want code %s", result, err, tc.code)
			}
			cmd, journalled := journalledResult(t, repo, "en-fail")
			if cmd.Status != domain.AdminCommandFailed || !strings.Contains(journalled.Error, tc.err.Error()) {
				t.Fatalf("failure not journalled: %+v / %+v", cmd, journalled)
			}
		})
	}
}

func TestClientUpdateEntryWritesRequireInProcessCatalog(t *testing.T) {
	for name, tc := range map[string]struct {
		deps *ClientUpdates
		code string
	}{
		"disabled": {nil, CodeUpdatesDisabled},
		"external": {&ClientUpdates{ServiceURL: "http://telesrv-update:2402"}, CodeUpdatesExternal},
	} {
		svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow, ClientUpdates: tc.deps})
		if _, err := svc.DisableClientUpdate(context.Background(), entryRequest("dis", "linux", "stable")); ErrorCode(err) != tc.code {
			t.Errorf("%s disable: error = %v, want code %s", name, err, tc.code)
		}
		if _, err := svc.EnableClientUpdate(context.Background(), entryRequest("en", "linux", "stable")); ErrorCode(err) != tc.code {
			t.Errorf("%s enable: error = %v, want code %s", name, err, tc.code)
		}
	}
}

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"testing"
	"time"

	"telesrv/internal/updatecdn"
)

// fakeClientUpdateCatalog 是进程内更新目录的替身：Status 返回预设的结果；暂存把读到
// 的字节记在内存里，发布按预设的 result/publishErr 应答并记录每一次调用。
type fakeClientUpdateCatalog struct {
	status updatecdn.StoreStatus
	err    error

	staged     map[string][]byte
	nextID     int
	stageErr   error
	result     updatecdn.PublishResult
	publishErr error
	calls      []fakePublishCall
	discarded  []string

	setResult   updatecdn.SetDisabledResult
	setErr      error
	setDisabled []fakeSetDisabledCall
}

type fakeSetDisabledCall struct {
	platform, channel string
	disabled, dryRun  bool
}

type fakePublishCall struct {
	id                     string
	dryRun, allowDowngrade bool
}

func (f *fakeClientUpdateCatalog) Status() (updatecdn.StoreStatus, error) { return f.status, f.err }
func (f *fakeClientUpdateCatalog) ManifestPath() string                   { return "/updates/manifest.json" }
func (f *fakeClientUpdateCatalog) FilesDir() string                       { return "/updates/files" }
func (f *fakeClientUpdateCatalog) CatalogOptions() updatecdn.CatalogOptions {
	return updatecdn.CatalogOptions{}
}

func (f *fakeClientUpdateCatalog) StagePackage(r io.Reader) (updatecdn.StagedPackage, error) {
	if f.stageErr != nil {
		return updatecdn.StagedPackage{}, f.stageErr
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return updatecdn.StagedPackage{}, err
	}
	if f.staged == nil {
		f.staged = map[string][]byte{}
	}
	f.nextID++
	id := fmt.Sprintf("%032x", f.nextID)
	f.staged[id] = data
	sum := sha256.Sum256(data)
	return updatecdn.StagedPackage{ID: id, Size: int64(len(data)), SHA256: hex.EncodeToString(sum[:])}, nil
}

func (f *fakeClientUpdateCatalog) PublishStagedPackage(id string, dryRun, allowDowngrade bool) (*updatecdn.PublishResult, error) {
	f.calls = append(f.calls, fakePublishCall{id: id, dryRun: dryRun, allowDowngrade: allowDowngrade})
	if _, ok := f.staged[id]; !ok {
		return nil, updatecdn.ErrStagingNotFound
	}
	if f.publishErr != nil {
		return nil, f.publishErr
	}
	if !dryRun {
		delete(f.staged, id)
	}
	result := f.result
	return &result, nil
}

func (f *fakeClientUpdateCatalog) DiscardStagedPackage(id string) error {
	if _, ok := f.staged[id]; !ok {
		return updatecdn.ErrStagingNotFound
	}
	delete(f.staged, id)
	f.discarded = append(f.discarded, id)
	return nil
}

func (f *fakeClientUpdateCatalog) SetDesktopReleaseDisabled(platform, channel string, disabled, dryRun bool) (*updatecdn.SetDisabledResult, error) {
	f.setDisabled = append(f.setDisabled, fakeSetDisabledCall{platform: platform, channel: channel, disabled: disabled, dryRun: dryRun})
	if f.setErr != nil {
		return nil, f.setErr
	}
	result := f.setResult
	return &result, nil
}

func TestClientUpdateStatusWithoutDependencyReportsDisabled(t *testing.T) {
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow})
	status, err := svc.ClientUpdateStatus(context.Background())
	if err != nil {
		t.Fatalf("ClientUpdateStatus() error = %v, want a disabled status", err)
	}
	if status.Mode != ClientUpdatesDisabled || status.PublicURL != "" || len(status.Entries) != 0 || status.Entries == nil {
		t.Fatalf("status = %+v, want mode disabled with an empty (non-nil) entry list", status)
	}
}

func TestClientUpdateStatusExternalMode(t *testing.T) {
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow, ClientUpdates: &ClientUpdates{
		PublicURL:  "https://updates.example/td",
		ServiceURL: "http://telesrv-update:2402",
	}})
	status, err := svc.ClientUpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != ClientUpdatesExternal || status.PublicURL != "https://updates.example/td" ||
		status.ServiceURL != "http://telesrv-update:2402" || len(status.Entries) != 0 {
		t.Fatalf("status = %+v, want external mode with both URLs and no entries", status)
	}
}

func TestClientUpdateStatusInProcessListsEntries(t *testing.T) {
	created := time.Unix(1760000000, 0)
	catalog := &fakeClientUpdateCatalog{status: updatecdn.StoreStatus{Desktop: []updatecdn.DesktopEntry{
		{Platform: "linux", Channel: "stable", Created: created, Release: updatecdn.DesktopRelease{
			Build: 6000000, Version: "6.0", File: "td-update-linux-x64-6000000", SHA256: "ab12", Size: 4096,
		}},
		{Platform: "mac", Channel: "beta", Release: updatecdn.DesktopRelease{Build: 1, File: "draft", Disabled: true}},
	}}}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow, ClientUpdates: &ClientUpdates{
		PublicURL: "https://chat.example/_updates", Catalog: catalog,
	}})
	status, err := svc.ClientUpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.Mode != ClientUpdatesInProcess || status.PublicURL != "https://chat.example/_updates" || status.ReloadError != "" {
		t.Fatalf("status = %+v, want in-process mode without a reload error", status)
	}
	// 面板在上传前按它检查文件大小：256 MiB 载荷加上包头、清单与签名的最大值。
	if status.MaxPackageSize != 268707243 {
		t.Fatalf("max package size = %d, want the v2 package limit 268707243", status.MaxPackageSize)
	}
	if len(status.Entries) != 2 {
		t.Fatalf("entries = %+v, want 2", status.Entries)
	}
	linux := status.Entries[0]
	if linux.Platform != "linux" || linux.Channel != "stable" || linux.Build != 6000000 || linux.Version != "6.0" ||
		linux.File != "td-update-linux-x64-6000000" || linux.SHA256 != "ab12" || linux.Size != 4096 || linux.Disabled ||
		linux.Created == nil || !linux.Created.Equal(created) {
		t.Fatalf("linux entry = %+v", linux)
	}
	draft := status.Entries[1]
	if draft.Platform != "mac" || !draft.Disabled || draft.Created != nil {
		t.Fatalf("draft entry = %+v, want disabled without a creation time", draft)
	}
}

func TestClientUpdateStatusReportsReloadFailure(t *testing.T) {
	catalog := &fakeClientUpdateCatalog{status: updatecdn.StoreStatus{
		Desktop:   []updatecdn.DesktopEntry{{Platform: "win64", Channel: "stable", Release: updatecdn.DesktopRelease{Build: 7}}},
		ReloadErr: errors.New("decode manifest: unexpected EOF"),
	}}
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow, ClientUpdates: &ClientUpdates{Catalog: catalog}})
	status, err := svc.ClientUpdateStatus(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if status.ReloadError != "decode manifest: unexpected EOF" || len(status.Entries) != 1 {
		t.Fatalf("status = %+v, want the previous entries and the reload error", status)
	}

	catalog.err = errors.New("no snapshot")
	if _, err := svc.ClientUpdateStatus(context.Background()); err == nil {
		t.Fatal("ClientUpdateStatus() with no snapshot succeeded")
	}
}

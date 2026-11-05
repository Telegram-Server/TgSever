package admin

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/updatecdn"
)

// 后台发布 Android 安装包的流程用真实的进程内更新目录（updatecdn.Store）和 internal/apkinfo
// 的样本 APK 测：上传即预检（解析包、核对身份），确认时带上通道与更新说明，状态里列出
// Android 条目与文档是否就绪，停用与启用走 apps 条目。

type fakeDocumentStatus map[string]bool

func (f fakeDocumentStatus) Ready(_ context.Context, sha256 string) (bool, error) {
	return f[sha256], nil
}

func androidUpdateService(t *testing.T, docs ClientUpdateDocuments) *Service {
	t.Helper()
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	if err := os.WriteFile(manifest, []byte(`{"schema_version":1}`), 0o644); err != nil {
		t.Fatal(err)
	}
	store, err := updatecdn.NewStore(manifest, filepath.Join(dir, "files"), updatecdn.CatalogOptions{})
	if err != nil {
		t.Fatal(err)
	}
	return NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow, ClientUpdates: &ClientUpdates{Catalog: store, Documents: docs}})
}

func uploadAPK(t *testing.T, svc *Service, id, fixture string) (CommandResult, error) {
	t.Helper()
	f, err := os.Open(filepath.Join("..", "apkinfo", "testdata", fixture))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	return svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta(id)}, f)
}

func androidRelease(channel string) *ClientUpdateAndroidRelease {
	return &ClientUpdateAndroidRelease{Channel: channel, Notes: map[string]string{"zh": "修复若干问题", "en": "Bug fixes", "ru": ""}}
}

func TestAndroidClientUpdateUploadPublishAndStatus(t *testing.T) {
	docs := fakeDocumentStatus{}
	svc := androidUpdateService(t, docs)
	ctx := context.Background()

	pre, err := uploadAPK(t, svc, "up-apk-1", "a-v1234-v2v3.apk")
	if err != nil {
		t.Fatalf("upload: %v", err)
	}
	d := pre.Details
	if d["kind"] != updatecdn.PackageKindAndroid || d["package_name"] != "cc.example.app" || d["version"] != "12.2.10" ||
		d["version_code"] != int64(1234) || d["min_sdk"] != 24 || d["staging_id"] == "" || d["signing"] != 3 {
		t.Fatalf("preflight details = %#v", d)
	}
	stagingID := d["staging_id"].(string)

	// 暂存的是 APK：不给发布信息就拒绝，暂存保留。
	if _, err := svc.PublishClientUpdate(ctx, PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-apk-0"), StagingID: stagingID}); ErrorCode(err) != CodeUpdateAndroidReleaseInvalid {
		t.Fatalf("publish without android fields err = %v, want %s", err, CodeUpdateAndroidReleaseInvalid)
	}
	bad := androidRelease("nightly")
	if _, err := svc.PublishClientUpdate(ctx, PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-apk-bad"), StagingID: stagingID, Android: bad}); ErrorCode(err) != CodeUpdateAndroidReleaseInvalid {
		t.Fatalf("publish with a bad channel err = %v, want %s", err, CodeUpdateAndroidReleaseInvalid)
	}

	dry := uploadMeta("pub-apk-dry")
	dry.DryRun = true
	preview, err := svc.PublishClientUpdate(ctx, PublishClientUpdateRequest{CommandMeta: dry, StagingID: stagingID, Android: androidRelease("stable")})
	if err != nil || preview.Details["downgrade"] != false || preview.Details["channel"] != "stable" {
		t.Fatalf("dry run = %+v, %v", preview, err)
	}
	published, err := svc.PublishClientUpdate(ctx, PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-apk-1"), StagingID: stagingID, Android: androidRelease("stable")})
	if err != nil || published.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("publish = %+v, %v", published, err)
	}
	if langs, _ := published.Details["notes_languages"].([]string); len(langs) != 2 {
		t.Fatalf("notes languages = %#v, want the two non-empty ones", published.Details["notes_languages"])
	}

	status, err := svc.ClientUpdateStatus(ctx)
	if err != nil || len(status.AndroidEntries) != 1 {
		t.Fatalf("status = %+v, %v", status, err)
	}
	entry := status.AndroidEntries[0]
	if entry.Channel != "stable" || entry.VersionCode != 1234 || entry.File != "android-cc.example.app-1234.apk" ||
		entry.DocumentReady == nil || *entry.DocumentReady || entry.Disabled {
		t.Fatalf("android entry = %+v", entry)
	}
	docs[entry.SHA256] = true
	status, _ = svc.ClientUpdateStatus(ctx)
	if ready := status.AndroidEntries[0].DocumentReady; ready == nil || !*ready {
		t.Fatalf("document_ready after registration = %v", ready)
	}

	// 换了签名密钥的包在上传这一步就被拦下，不留暂存。
	if _, err := uploadAPK(t, svc, "up-apk-b", "b-v1234-v2v3.apk"); ErrorCode(err) != CodeUpdateAndroidIdentityMismatch {
		t.Fatalf("upload with another signer err = %v, want %s", err, CodeUpdateAndroidIdentityMismatch)
	}

	// 回退要显式允许。
	pre2, err := uploadAPK(t, svc, "up-apk-2", "a-v1234-v2v3.apk")
	if err != nil {
		t.Fatal(err)
	}
	// 同一个包重新上传：只改说明，不算回退。
	edit := androidRelease("stable")
	edit.CanNotSkip = true
	if r, err := svc.PublishClientUpdate(ctx, PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-apk-edit"), StagingID: pre2.Details["staging_id"].(string), Android: edit}); err != nil || r.Details["downgrade"] != false || r.Details["can_not_skip"] != true {
		t.Fatalf("metadata edit = %+v, %v", r, err)
	}

	// 停用与启用 Android 条目。
	disabled, err := svc.DisableClientUpdate(ctx, ClientUpdateEntryRequest{CommandMeta: uploadMeta("dis-apk"), Platform: "android", Channel: "stable"})
	if err != nil || disabled.Details["disabled"] != true {
		t.Fatalf("disable = %+v, %v", disabled, err)
	}
	status, _ = svc.ClientUpdateStatus(ctx)
	if !status.AndroidEntries[0].Disabled || status.AndroidEntries[0].DocumentReady != nil {
		t.Fatalf("disabled entry = %+v", status.AndroidEntries[0])
	}
	if enabled, err := svc.EnableClientUpdate(ctx, ClientUpdateEntryRequest{CommandMeta: uploadMeta("en-apk"), Platform: "android", Channel: "stable"}); err != nil || enabled.Details["disabled"] != false {
		t.Fatalf("enable = %+v, %v", enabled, err)
	}
	if _, err := svc.DisableClientUpdate(ctx, ClientUpdateEntryRequest{CommandMeta: uploadMeta("dis-apk-beta"), Platform: "android", Channel: "beta"}); ErrorCode(err) != CodeUpdateEntryNotFound {
		t.Fatalf("disable a missing android entry err = %v, want %s", err, CodeUpdateEntryNotFound)
	}
}

func TestAndroidClientUpdateDowngradeNeedsAllow(t *testing.T) {
	svc := androidUpdateService(t, nil)
	ctx := context.Background()
	for i, fixture := range []string{"a-v1300-v2v3.apk", "a-v1234-v2v3.apk"} {
		pre, err := uploadAPK(t, svc, "up-order-"+fixture, fixture)
		if err != nil {
			t.Fatal(err)
		}
		req := PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-order-" + fixture), StagingID: pre.Details["staging_id"].(string), Android: androidRelease("beta")}
		_, err = svc.PublishClientUpdate(ctx, req)
		if i == 0 && err != nil {
			t.Fatalf("first publish: %v", err)
		}
		if i == 1 {
			if ErrorCode(err) != CodeUpdatePackageRejected {
				t.Fatalf("older version err = %v, want %s", err, CodeUpdatePackageRejected)
			}
			req.CommandMeta = uploadMeta("pub-order-allow")
			req.AllowDowngrade = true
			r, err := svc.PublishClientUpdate(ctx, req)
			if err != nil || r.Details["downgrade"] != true {
				t.Fatalf("allowed rollback = %+v, %v", r, err)
			}
		}
	}
}

package admin

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"telesrv/internal/domain"
	"telesrv/internal/updatecdn"
)

// newerLinuxRelease 是预检/发布替身返回的结果：linux.stable 从 6000000 升到 6000001。
func newerLinuxRelease() updatecdn.PublishResult {
	return updatecdn.PublishResult{
		Platform:        "linux",
		Channel:         "stable",
		Release:         updatecdn.DesktopRelease{Build: 6000001, File: "td-update-linux-x64-6000001", SHA256: "cd34", Size: 4096},
		Package:         updatecdn.VerifiedPackage{Created: time.Unix(1760000000, 0).UTC()},
		Previous:        &updatecdn.DesktopRelease{Build: 6000000, File: "td-update-linux-x64-6000000", SHA256: "ab12", Size: 4000},
		ManifestUpdated: true,
		FileCopied:      true,
	}
}

func clientUpdateService(t *testing.T) (*Service, *fakeClientUpdateCatalog, *memoryCommandRepo) {
	t.Helper()
	repo := newMemoryCommandRepo()
	catalog := &fakeClientUpdateCatalog{result: newerLinuxRelease()}
	svc := NewService(Dependencies{Commands: repo, Now: fixedNow, ClientUpdates: &ClientUpdates{PublicURL: "https://chat.example/_updates", Catalog: catalog}})
	return svc, catalog, repo
}

func uploadMeta(id string) CommandMeta {
	return CommandMeta{CommandID: id, Actor: "ops", Reason: "ship 6.0.1"}
}

func journalledResult(t *testing.T, repo *memoryCommandRepo, id string) (domain.AdminCommand, CommandResult) {
	t.Helper()
	cmd, ok := repo.items[id]
	if !ok {
		t.Fatalf("command %s was not journalled", id)
	}
	var result CommandResult
	if err := json.Unmarshal(cmd.ResultJSON, &result); err != nil {
		t.Fatalf("journalled result of %s: %v", id, err)
	}
	return cmd, result
}

func TestUploadClientUpdateStagesAndPreflights(t *testing.T) {
	svc, catalog, repo := clientUpdateService(t)
	result, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-1")}, strings.NewReader("package-bytes"))
	if err != nil {
		t.Fatalf("UploadClientUpdate() error = %v", err)
	}
	if len(catalog.calls) != 1 || !catalog.calls[0].dryRun || catalog.calls[0].allowDowngrade {
		t.Fatalf("publish calls = %+v, want one dry run", catalog.calls)
	}
	stagingID := catalog.calls[0].id
	if string(catalog.staged[stagingID]) != "package-bytes" {
		t.Fatalf("staged bytes = %q, want the uploaded body kept for the confirm", catalog.staged[stagingID])
	}
	if !result.DryRun || result.Action != ActionPreflightClientUpdate || result.Status != string(domain.AdminCommandCompleted) {
		t.Fatalf("result = %+v, want a completed dry-run preflight", result)
	}
	d := result.Details
	if d["staging_id"] != stagingID || d["platform"] != "linux" || d["channel"] != "stable" || d["build"] != uint64(6000001) ||
		d["file"] != "td-update-linux-x64-6000001" || d["sha256"] != "cd34" || d["size"] != int64(4096) ||
		d["created"] != "2025-10-09T08:53:20Z" || d["unchanged"] != false || d["downgrade"] != false {
		t.Fatalf("details = %#v", d)
	}
	previous, _ := d["previous"].(map[string]any)
	if previous["build"] != uint64(6000000) || previous["file"] != "td-update-linux-x64-6000000" || previous["disabled"] != false {
		t.Fatalf("previous = %#v", d["previous"])
	}

	cmd, journalled := journalledResult(t, repo, "up-1")
	if !cmd.DryRun || cmd.Action != ActionPreflightClientUpdate || cmd.Status != domain.AdminCommandCompleted {
		t.Fatalf("journalled command = %+v, want a completed dry run", cmd)
	}
	if journalled.Details["platform"] != "linux" || journalled.Details["staging_id"] != stagingID {
		t.Fatalf("journalled details = %#v", journalled.Details)
	}
	var request map[string]any
	_ = json.Unmarshal(cmd.RequestJSON, &request)
	sum := sha256.Sum256([]byte("package-bytes"))
	if request["sha256"] != hex.EncodeToString(sum[:]) {
		t.Fatalf("request sha256 missing: %s", cmd.RequestJSON)
	}
	if request["size"] != float64(len("package-bytes")) {
		t.Fatalf("request = %s, want the staged size bound into the command", cmd.RequestJSON)
	}
}

func TestUploadClientUpdatePassesAllowDowngradeAndFlagsRollback(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	rollback := newerLinuxRelease()
	rollback.Release.Build = 5999999
	catalog.result = rollback
	result, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-rb"), AllowDowngrade: true}, strings.NewReader("old"))
	if err != nil {
		t.Fatal(err)
	}
	if !catalog.calls[0].allowDowngrade || result.Details["downgrade"] != true {
		t.Fatalf("calls = %+v details = %#v, want allow_downgrade passed and the rollback flagged", catalog.calls, result.Details)
	}
}

func TestUploadClientUpdateFailedPreflightIsJournalledAndDiscarded(t *testing.T) {
	svc, catalog, repo := clientUpdateService(t)
	catalog.publishErr = errors.New("package \"x\": signature does not verify")
	result, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-bad")}, strings.NewReader("forged"))
	if err == nil || !strings.Contains(err.Error(), "signature does not verify") {
		t.Fatalf("UploadClientUpdate() error = %v, want the preflight failure", err)
	}
	if result.Code != CodeUpdatePackageRejected {
		t.Fatalf("code = %q, want %s", result.Code, CodeUpdatePackageRejected)
	}
	if len(catalog.staged) != 0 || len(catalog.discarded) != 1 {
		t.Fatalf("staged = %v discarded = %v, want the rejected upload removed", catalog.staged, catalog.discarded)
	}
	cmd, journalled := journalledResult(t, repo, "up-bad")
	if cmd.Status != domain.AdminCommandFailed || !strings.Contains(cmd.Error, "signature does not verify") || journalled.Code != CodeUpdatePackageRejected {
		t.Fatalf("journalled = %+v / %+v, want the failure recorded", cmd, journalled)
	}
}

func TestUploadClientUpdateAlreadyPublishedDiscardsStaging(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	catalog.result = updatecdn.PublishResult{Platform: "linux", Channel: "stable", Release: newerLinuxRelease().Release, Unchanged: true}
	result, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-same")}, strings.NewReader("same"))
	if err != nil {
		t.Fatal(err)
	}
	if result.Details["unchanged"] != true || result.Details["staging_id"] != nil {
		t.Fatalf("details = %#v, want unchanged without a staging id", result.Details)
	}
	if len(catalog.staged) != 0 {
		t.Fatalf("staged = %v, want the duplicate upload discarded", catalog.staged)
	}
}

func TestUploadClientUpdateReplayAndConflict(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	first, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-twice")}, strings.NewReader("bytes-a"))
	if err != nil {
		t.Fatal(err)
	}
	replay, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-twice")}, strings.NewReader("bytes-a"))
	if err != nil {
		t.Fatalf("replay error = %v", err)
	}
	if !replay.AlreadyExecuted || replay.Details["staging_id"] != first.Details["staging_id"] {
		t.Fatalf("replay = %+v, want the first result with its staging id", replay)
	}
	if len(catalog.calls) != 1 || len(catalog.staged) != 1 {
		t.Fatalf("calls = %+v staged = %d, want no second preflight and the replayed upload discarded", catalog.calls, len(catalog.staged))
	}

	conflict, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-twice")}, strings.NewReader("bytes-b"))
	if err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("different bytes under the same command id = %+v, %v; want COMMAND_ID_CONFLICT", conflict, err)
	}
	if len(catalog.staged) != 1 {
		t.Fatalf("staged = %d, want the conflicting upload discarded", len(catalog.staged))
	}
}

func TestUploadClientUpdateValidatesMetaBeforeReadingTheBody(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	for name, meta := range map[string]CommandMeta{
		"reason":     {CommandID: "up-x", Actor: "ops"},
		"actor":      {CommandID: "up-x", Reason: "r"},
		"command_id": {Actor: "ops", Reason: "r"},
	} {
		if _, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: meta}, strings.NewReader("bytes")); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("missing %s: error = %v", name, err)
		}
	}
	if catalog.nextID != 0 {
		t.Fatalf("staged %d uploads with invalid metadata", catalog.nextID)
	}
}

// 暂存失败按原因给出不同的码：包太大、暂存已满、读取上传失败（客户端一侧）与本机
// 存储失败（服务器一侧）。暂存失败发生在命令记录之前，不记日志。
func TestUploadClientUpdateCodesStagingFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"too large": {updatecdn.ErrStagedPackageTooLarge, CodeUpdatePackageTooLarge},
		"full":      {updatecdn.ErrStagingFull, CodeUpdateStagingFull},
		"read":      {fmt.Errorf("%w: %w", updatecdn.ErrStagingReadFailed, errors.New("i/o timeout")), CodeUpdateUploadFailed},
		"storage":   {fmt.Errorf("%w: write staging file: %w", updatecdn.ErrStagingStorage, errors.New("no space left on device")), CodeUpdateStagingStorage},
	} {
		t.Run(name, func(t *testing.T) {
			svc, catalog, repo := clientUpdateService(t)
			catalog.stageErr = tc.err
			_, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-" + name)}, strings.NewReader("bytes"))
			if ErrorCode(err) != tc.code {
				t.Fatalf("error = %v (code %q), want code %s", err, ErrorCode(err), tc.code)
			}
			if len(repo.items) != 0 {
				t.Fatalf("a staging failure was journalled: %+v", repo.items)
			}
		})
	}
}

// 命令存储未配置时，在读取请求体之前就失败：不会先把 256 MiB 收进暂存再拒绝。
func TestUploadClientUpdateChecksTheCommandStoreBeforeReadingTheBody(t *testing.T) {
	catalog := &fakeClientUpdateCatalog{result: newerLinuxRelease()}
	svc := NewService(Dependencies{Now: fixedNow, ClientUpdates: &ClientUpdates{Catalog: catalog}})
	_, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up-nostore")}, strings.NewReader("bytes"))
	if err == nil || !strings.Contains(err.Error(), "command store") {
		t.Fatalf("error = %v, want the command store requirement", err)
	}
	if catalog.nextID != 0 {
		t.Fatal("the body was staged although no command can be recorded")
	}
}

func TestClientUpdateWritesRequireInProcessCatalog(t *testing.T) {
	for name, tc := range map[string]struct {
		deps *ClientUpdates
		code string
	}{
		"disabled": {nil, CodeUpdatesDisabled},
		"external": {&ClientUpdates{ServiceURL: "http://telesrv-update:2402"}, CodeUpdatesExternal},
	} {
		svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), Now: fixedNow, ClientUpdates: tc.deps})
		_, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta("up")}, strings.NewReader("x"))
		if ErrorCode(err) != tc.code {
			t.Errorf("%s upload: error = %v, want code %s", name, err, tc.code)
		}
		_, err = svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: uploadMeta("pub"), StagingID: "x"})
		if ErrorCode(err) != tc.code {
			t.Errorf("%s publish: error = %v, want code %s", name, err, tc.code)
		}
	}
}

// stageForPublish 预检一次上传并返回暂存 id。
func stageForPublish(t *testing.T, svc *Service, catalog *fakeClientUpdateCatalog) string {
	t.Helper()
	result, err := svc.UploadClientUpdate(context.Background(), UploadClientUpdateRequest{CommandMeta: uploadMeta(fmt.Sprintf("up-%d", catalog.nextID+1))}, strings.NewReader("pkg"))
	if err != nil {
		t.Fatal(err)
	}
	id, _ := result.Details["staging_id"].(string)
	if id == "" {
		t.Fatalf("preflight returned no staging id: %#v", result.Details)
	}
	return id
}

func TestPublishClientUpdatePublishesTheStagedPackageAndJournals(t *testing.T) {
	svc, catalog, repo := clientUpdateService(t)
	stagingID := stageForPublish(t, svc, catalog)

	result, err := svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-1"), StagingID: stagingID})
	if err != nil {
		t.Fatalf("PublishClientUpdate() error = %v", err)
	}
	last := catalog.calls[len(catalog.calls)-1]
	if last != (fakePublishCall{id: stagingID}) {
		t.Fatalf("publish call = %+v, want a real publish of %s", last, stagingID)
	}
	if result.DryRun || result.Action != ActionPublishClientUpdate || result.Details["build"] != uint64(6000001) || result.Details["staging_id"] != nil {
		t.Fatalf("result = %+v", result)
	}
	cmd, journalled := journalledResult(t, repo, "pub-1")
	if cmd.Status != domain.AdminCommandCompleted || cmd.DryRun || journalled.Details["file"] != "td-update-linux-x64-6000001" ||
		journalled.Details["sha256"] != "cd34" || journalled.Details["manifest_updated"] != true {
		t.Fatalf("journalled = %+v / %#v", cmd, journalled.Details)
	}

	// 同一 command_id 重放：返回第一次的结果，不再发布。
	calls := len(catalog.calls)
	replay, err := svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-1"), StagingID: stagingID})
	if err != nil || !replay.AlreadyExecuted || len(catalog.calls) != calls {
		t.Fatalf("replay = %+v, %v (calls %d -> %d)", replay, err, calls, len(catalog.calls))
	}
	conflict, err := svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-1"), StagingID: "0123456789abcdef0123456789abcdef"})
	if err == nil || conflict.Error != "COMMAND_ID_CONFLICT" {
		t.Fatalf("other staging under the same command id = %+v, %v; want COMMAND_ID_CONFLICT", conflict, err)
	}
}

func TestPublishClientUpdateDryRunReverifiesAndKeepsStaging(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	stagingID := stageForPublish(t, svc, catalog)
	meta := uploadMeta("dry-pub")
	meta.DryRun = true
	result, err := svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: meta, StagingID: stagingID, AllowDowngrade: true})
	if err != nil || !result.DryRun {
		t.Fatalf("dry run = %+v, %v", result, err)
	}
	last := catalog.calls[len(catalog.calls)-1]
	if last != (fakePublishCall{id: stagingID, dryRun: true, allowDowngrade: true}) {
		t.Fatalf("publish call = %+v, want a dry run with allow_downgrade", last)
	}
	if _, ok := catalog.staged[stagingID]; !ok {
		t.Fatal("dry run removed the staged package")
	}
}

func TestPublishClientUpdateRollbackRequiresReason(t *testing.T) {
	svc, catalog, _ := clientUpdateService(t)
	stagingID := stageForPublish(t, svc, catalog)
	calls := len(catalog.calls)
	meta := uploadMeta("pub-rb")
	meta.Reason = "   "
	_, err := svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: meta, StagingID: stagingID, AllowDowngrade: true})
	if err == nil || !strings.Contains(err.Error(), "reason") {
		t.Fatalf("rollback without a reason: error = %v", err)
	}
	if len(catalog.calls) != calls {
		t.Fatal("rollback without a reason reached the catalog")
	}
}

func TestPublishClientUpdateCodesStagingAndLockFailures(t *testing.T) {
	for name, tc := range map[string]struct {
		err  error
		code string
	}{
		"missing": {updatecdn.ErrStagingNotFound, CodeUpdateStagingNotFound},
		"expired": {fmt.Errorf("x: %w", updatecdn.ErrStagingExpired), CodeUpdateStagingExpired},
		"locked":  {fmt.Errorf("%w (manifest.json.lock exists)", updatecdn.ErrPublishInProgress), CodeUpdatePublishInProgress},
		"invalid": {errors.New("build 5 is not newer than the published build 6"), CodeUpdatePackageRejected},
	} {
		t.Run(name, func(t *testing.T) {
			svc, catalog, repo := clientUpdateService(t)
			stagingID := stageForPublish(t, svc, catalog)
			catalog.publishErr = tc.err
			result, err := svc.PublishClientUpdate(context.Background(), PublishClientUpdateRequest{CommandMeta: uploadMeta("pub-" + name), StagingID: stagingID})
			if err == nil || result.Code != tc.code || ErrorCode(err) != tc.code {
				t.Fatalf("result = %+v, err = %v; want code %s", result, err, tc.code)
			}
			if cmd, _ := journalledResult(t, repo, "pub-"+name); cmd.Status != domain.AdminCommandFailed {
				t.Fatalf("failure not journalled: %+v", cmd)
			}
		})
	}
}

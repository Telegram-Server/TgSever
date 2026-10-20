package admin

import (
	"bytes"
	"context"
	"testing"
	"time"

	"telesrv/internal/app/systemidentity"
	"telesrv/internal/domain"
	"telesrv/internal/identity"
)

// 改成服务端写入后，systemidentity.Watcher 仍然被触发：它轮询的就是这些命令写入的
// 目录（cmd/telesrv 里两者用同一个 identity.Store），名称变了就改 777000 的展示名，
// 图标变了就重新设置头像——与以前后台直接写文件时一样。
func TestServerIdentityCommandsTriggerTheSystemIdentityWatcher(t *testing.T) {
	t.Cleanup(func() { domain.SetOfficialSystemUserDisplayName("") })
	dir := t.TempDir()
	store := identity.NewStore(dir)
	svc := NewService(Dependencies{Commands: newMemoryCommandRepo(), ServerIdentity: store, Now: fixedNow})

	avatars := make(chan []byte, 8)
	seeder := func(_ context.Context, icon []byte, _ int64) (bool, error) {
		avatars <- append([]byte(nil), icon...)
		return true, nil
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go (&systemidentity.Watcher{Store: store, SeedAvatar: seeder, Interval: 10 * time.Millisecond}).Run(ctx)
	// 让 Run 先拍下初始快照，后面的写入才一定被当作变化。
	time.Sleep(30 * time.Millisecond)

	if _, err := svc.SetServerIdentity(ctx, SetServerIdentityRequest{CommandMeta: serverIdentityMeta("watch-name", false), Name: "Acme Support"}); err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().Add(2 * time.Second)
	for domain.OfficialSystemUser().FirstName != "Acme Support" {
		if time.Now().After(deadline) {
			t.Fatalf("777000 name = %q, want the name the command wrote", domain.OfficialSystemUser().FirstName)
		}
		time.Sleep(5 * time.Millisecond)
	}

	if _, err := svc.UploadServerIcon(ctx, UploadServerIconRequest{CommandMeta: serverIdentityMeta("watch-icon", false), FileName: "logo.png", Data: pngIcon}); err != nil {
		t.Fatal(err)
	}
	select {
	case icon := <-avatars:
		if !bytes.Equal(icon, pngIcon) {
			t.Fatalf("watcher seeded avatar %q, want the uploaded icon", icon)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("watcher did not pick up the uploaded icon")
	}
}

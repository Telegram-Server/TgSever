package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"telesrv/internal/updatecdn"
)

// telesrv-update publish 按内容识别 APK：需要 -channel 与至少一种说明，发布后清单里有
// apps.android 条目；桌面包不接受 Android 专用参数。

func apkFixturePath(t *testing.T, name string) string {
	t.Helper()
	path := filepath.Join("..", "..", "internal", "apkinfo", "testdata", name)
	if _, err := os.Stat(path); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestPublishAndroidAPK(t *testing.T) {
	dir := t.TempDir()
	manifest := filepath.Join(dir, "manifest.json")
	t.Setenv("TELESRV_UPDATE_ROOT_PUBLIC_KEY", "")
	notes := filepath.Join(dir, "notes.json")
	if err := os.WriteFile(notes, []byte(`{"ru":"Исправления","en":"from file"}`), 0o644); err != nil {
		t.Fatal(err)
	}
	apk := apkFixturePath(t, "a-v1234-v2v3.apk")

	if err := runPublish([]string{"-manifest", manifest, "-notes-en", "Fixes", apk}); err == nil || !strings.Contains(err.Error(), "channel") {
		t.Fatalf("publish without -channel err = %v", err)
	}
	if err := runPublish([]string{"-manifest", manifest, "-channel", "stable", apk}); err == nil || !strings.Contains(err.Error(), "release notes") {
		t.Fatalf("publish without notes err = %v", err)
	}
	if err := runPublish([]string{"-manifest", manifest, "-channel", "stable", "-dry-run", "-notes-zh", "修复", apk}); err != nil {
		t.Fatalf("dry run: %v", err)
	}
	if _, err := os.Stat(manifest); !os.IsNotExist(err) {
		t.Fatalf("dry run wrote the manifest: %v", err)
	}
	args := []string{"-manifest", manifest, "-channel", "beta", "-notes-zh", "修复", "-notes-en", "Fixes", "-notes-file", notes, "-can-not-skip", apk}
	if err := runPublish(args); err != nil {
		t.Fatalf("publish: %v", err)
	}
	catalog, err := updatecdn.LoadCatalog(manifest, updatecdn.DefaultFilesDir(manifest, ""), updatecdn.CatalogOptions{})
	if err != nil {
		t.Fatalf("load: %v", err)
	}
	resolved, err := catalog.Resolve(updatecdn.ResolveRequest{Platform: "android", Channel: "beta", Version: "1.0", LangCode: "ru"})
	if err != nil || resolved == nil || resolved.File == nil || !resolved.CanNotSkip || resolved.Text != "Исправления" {
		t.Fatalf("resolved = %+v, %v", resolved, err)
	}
	// -notes-en 覆盖说明文件里的同一语言。
	en, _ := catalog.Resolve(updatecdn.ResolveRequest{Platform: "android", Channel: "beta", Version: "1.0", LangCode: "en"})
	if en == nil || en.Text != "Fixes" {
		t.Fatalf("en notes = %+v", en)
	}
}

func TestPublishDesktopRejectsAndroidFlags(t *testing.T) {
	dir := t.TempDir()
	pkg := filepath.Join(dir, "td-update-linux-x64-1")
	if err := os.WriteFile(pkg, []byte("not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	err := runPublish([]string{"-manifest", filepath.Join(dir, "manifest.json"), "-channel", "stable", pkg})
	if err == nil || !strings.Contains(err.Error(), "only apply to an Android APK") {
		t.Fatalf("desktop publish with -channel err = %v", err)
	}
}

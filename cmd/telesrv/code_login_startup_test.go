package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"telesrv/internal/identity"
)

// 启动时验证码登录状态的提示：密码登录开启而验证码登录实际关闭时提示"已关闭"；密码登录
// 关闭时持久化的"关闭"覆盖值被忽略（不变量），提示它被忽略；正常开启时不提示。
func TestCodeLoginStartupWarning(t *testing.T) {
	off, on := false, true
	for _, tc := range []struct {
		name          string
		override      *bool
		env, password bool
		want          string
	}{
		{"default on", nil, true, false, ""},
		{"override on", &on, true, true, ""},
		{"override off with password login", &off, true, true, "验证码登录已关闭"},
		{"env off with password login", nil, false, true, "验证码登录已关闭"},
		{"override off ignored without password login", &off, true, false, "被忽略"},
	} {
		store := identity.NewStore(t.TempDir())
		if tc.override != nil {
			if err := store.SetCodeLoginOverride(tc.override); err != nil {
				t.Fatal(err)
			}
		}
		got := codeLoginStartupWarning(store, tc.env, tc.password)
		if tc.want == "" && got != "" || tc.want != "" && !strings.Contains(got, tc.want) {
			t.Fatalf("%s: warning %q, want containing %q", tc.name, got, tc.want)
		}
	}
}

func TestCodeLoginStartupWarningUnreadableIdentity(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	store := identity.NewStore(dir)
	if got := codeLoginStartupWarning(store, true, true); !strings.Contains(got, "验证码登录已关闭") {
		t.Fatalf("unreadable identity with password login: %q, want a code-login-off warning", got)
	}
	if got := codeLoginStartupWarning(store, true, false); got != "" {
		t.Fatalf("unreadable identity without password login: %q, want no warning (code login stays on)", got)
	}
}

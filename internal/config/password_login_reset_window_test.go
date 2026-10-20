package config

import (
	"testing"
	"time"
)

func TestLoadPasswordLoginResetWindow(t *testing.T) {
	disableDefaultConfigFile(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	// 默认关闭：窗口从任何一次新授权（含扫码批准的新设备）算起，运营方接受这个风险才开启。
	if cfg.PasswordLoginResetWindow != 0 {
		t.Fatalf("PasswordLoginResetWindow = %v by default, want 0 (disabled)", cfg.PasswordLoginResetWindow)
	}

	t.Setenv("TELESRV_PASSWORD_LOGIN_RESET_WINDOW", "3m")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PasswordLoginResetWindow != 3*time.Minute {
		t.Fatalf("PasswordLoginResetWindow = %v, want 3m", cfg.PasswordLoginResetWindow)
	}

	// 0 关闭免旧密码重设，加载时原样保留，不报错也不改回默认值。
	t.Setenv("TELESRV_PASSWORD_LOGIN_RESET_WINDOW", "0")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load with the reset window disabled: %v", err)
	}
	if cfg.PasswordLoginResetWindow != 0 {
		t.Fatalf("PasswordLoginResetWindow = %v, want 0 (disabled)", cfg.PasswordLoginResetWindow)
	}
}

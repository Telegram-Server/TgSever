package config

import (
	"testing"
	"time"
)

func TestLoadPasswordLoginUserRateLimit(t *testing.T) {
	disableDefaultConfigFile(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PasswordLoginUserRateLimit != 10 || cfg.PasswordLoginUserRateWindow != time.Hour {
		t.Fatalf("password login user rate limit = %d / %v by default, want 10 / 1h",
			cfg.PasswordLoginUserRateLimit, cfg.PasswordLoginUserRateWindow)
	}

	t.Setenv("TELESRV_PASSWORD_LOGIN_USER_RATE_LIMIT", "3")
	t.Setenv("TELESRV_PASSWORD_LOGIN_USER_RATE_WINDOW", "15m")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.PasswordLoginUserRateLimit != 3 || cfg.PasswordLoginUserRateWindow != 15*time.Minute {
		t.Fatalf("password login user rate limit = %d / %v, want 3 / 15m",
			cfg.PasswordLoginUserRateLimit, cfg.PasswordLoginUserRateWindow)
	}

	// <=0 关闭这个维度，加载时原样保留，不报错也不改回默认值。
	t.Setenv("TELESRV_PASSWORD_LOGIN_USER_RATE_LIMIT", "0")
	cfg, err = Load()
	if err != nil {
		t.Fatalf("Load with the limit disabled: %v", err)
	}
	if cfg.PasswordLoginUserRateLimit != 0 {
		t.Fatalf("PasswordLoginUserRateLimit = %d, want 0 (disabled)", cfg.PasswordLoginUserRateLimit)
	}
}

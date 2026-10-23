package config

import (
	"testing"
	"time"
)

func TestLoadBotUpdateRetention(t *testing.T) {
	t.Setenv("TELESRV_CONFIG", "")
	t.Setenv("TELESRV_BOT_UPDATE_RETENTION", "")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.BotUpdateRetention != 24*time.Hour {
		t.Fatalf("default bot update retention = %v, want 24h", cfg.BotUpdateRetention)
	}
	t.Setenv("TELESRV_BOT_UPDATE_RETENTION", "6h")
	if cfg, err = Load(); err != nil || cfg.BotUpdateRetention != 6*time.Hour {
		t.Fatalf("bot update retention = %v (%v), want 6h", cfg.BotUpdateRetention, err)
	}
}

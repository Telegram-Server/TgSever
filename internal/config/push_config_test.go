package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
	"time"
)

// pushKeys 是推送相关的全部环境变量；三处示例（根目录 .env.example、Docker 的 .env.example、
// compose）都要列出它们。
var pushKeys = []string{
	"TELESRV_APNS_ENABLED",
	"TELESRV_APNS_TOPIC",
	"TELESRV_APNS_CERT_FILE",
	"TELESRV_APNS_KEY_FILE",
	"TELESRV_APNS_KEY_ID",
	"TELESRV_APNS_TEAM_ID",
	"TELESRV_PUSH_QUEUE_SIZE",
	"TELESRV_PUSH_WORKERS",
	"TELESRV_PUSH_SEND_TIMEOUT",
	"TELESRV_PUSH_MAX_RETRIES",
}

func TestLoadPushConfigDefaults(t *testing.T) {
	disableDefaultConfigFile(t)
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APNsEnabled || cfg.APNsKeyFile != "" || cfg.APNsKeyID != "" || cfg.APNsTeamID != "" {
		t.Fatalf("APNs defaults = %+v", cfg)
	}
	if cfg.PushQueueSize != 4096 || cfg.PushWorkers != 4 || cfg.PushSendTimeout != 10*time.Second || cfg.PushMaxRetries != 3 {
		t.Fatalf("push defaults = queue %d workers %d timeout %v retries %d", cfg.PushQueueSize, cfg.PushWorkers, cfg.PushSendTimeout, cfg.PushMaxRetries)
	}
}

func TestLoadPushConfigOverrides(t *testing.T) {
	disableDefaultConfigFile(t)
	t.Setenv("TELESRV_APNS_ENABLED", "true")
	t.Setenv("TELESRV_APNS_TOPIC", "co.kaka.tgpro")
	t.Setenv("TELESRV_APNS_KEY_FILE", " /etc/telesrv-apns/AuthKey_ABC123DEFG.p8 ")
	t.Setenv("TELESRV_APNS_KEY_ID", " ABC123DEFG ")
	t.Setenv("TELESRV_APNS_TEAM_ID", "TEAM123456")
	t.Setenv("TELESRV_PUSH_QUEUE_SIZE", "100")
	t.Setenv("TELESRV_PUSH_WORKERS", "16")
	t.Setenv("TELESRV_PUSH_SEND_TIMEOUT", "3s")
	t.Setenv("TELESRV_PUSH_MAX_RETRIES", "0")
	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.APNsKeyFile != "/etc/telesrv-apns/AuthKey_ABC123DEFG.p8" || cfg.APNsKeyID != "ABC123DEFG" || cfg.APNsTeamID != "TEAM123456" {
		t.Fatalf(".p8 config = %q %q %q", cfg.APNsKeyFile, cfg.APNsKeyID, cfg.APNsTeamID)
	}
	if cfg.PushQueueSize != 100 || cfg.PushWorkers != 16 || cfg.PushSendTimeout != 3*time.Second || cfg.PushMaxRetries != 0 {
		t.Fatalf("push tunables = queue %d workers %d timeout %v retries %d", cfg.PushQueueSize, cfg.PushWorkers, cfg.PushSendTimeout, cfg.PushMaxRetries)
	}
}

func TestLoadRejectsOutOfRangePushTunables(t *testing.T) {
	for _, item := range []struct{ key, value string }{
		{"TELESRV_PUSH_QUEUE_SIZE", "0"},
		{"TELESRV_PUSH_QUEUE_SIZE", "1000001"},
		{"TELESRV_PUSH_WORKERS", "0"},
		{"TELESRV_PUSH_WORKERS", "257"},
		{"TELESRV_PUSH_SEND_TIMEOUT", "500ms"},
		{"TELESRV_PUSH_SEND_TIMEOUT", "3m"},
		{"TELESRV_PUSH_MAX_RETRIES", "-1"},
		{"TELESRV_PUSH_MAX_RETRIES", "11"},
	} {
		t.Run(item.key+"="+item.value, func(t *testing.T) {
			disableDefaultConfigFile(t)
			t.Setenv(item.key, item.value)
			_, err := Load()
			if err == nil {
				t.Fatalf("Load accepted invalid %s=%s", item.key, item.value)
			}
			if !strings.Contains(err.Error(), item.key) {
				t.Fatalf("error %q does not name %s", err, item.key)
			}
		})
	}
}

// TestExampleConfigurationsDocumentPush 保证三处示例同步列出推送变量，且照示例复制出的配置不开启推送。
func TestExampleConfigurationsDocumentPush(t *testing.T) {
	for _, path := range []string{"../../.env.example", "../../deploy/docker/.env.example"} {
		t.Run(path, func(t *testing.T) {
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			defer func() { _ = f.Close() }()
			active, listed := map[string]string{}, map[string]bool{}
			scanner := bufio.NewScanner(f)
			for scanner.Scan() {
				line := strings.TrimSpace(scanner.Text())
				commented := strings.HasPrefix(line, "#")
				line = strings.TrimSpace(strings.TrimLeft(line, "#"))
				key, value, ok := strings.Cut(line, "=")
				if !ok || strings.ContainsAny(key, " \t") {
					continue
				}
				listed[key] = true
				if !commented {
					active[key] = strings.TrimSpace(value)
				}
			}
			if err := scanner.Err(); err != nil {
				t.Fatal(err)
			}
			for _, key := range pushKeys {
				if !listed[key] {
					t.Errorf("%s does not document %s", path, key)
				}
			}
			if v := active["TELESRV_APNS_ENABLED"]; v != "" && v != "false" {
				t.Errorf("TELESRV_APNS_ENABLED=%q: copying the example must keep push off", v)
			}
		})
	}
	compose, err := os.ReadFile("../../deploy/docker/compose.yaml")
	if err != nil {
		t.Fatal(err)
	}
	for _, key := range pushKeys {
		if !strings.Contains(string(compose), key+": ${"+key) {
			t.Errorf("compose.yaml does not pass %s through to the server", key)
		}
	}
}

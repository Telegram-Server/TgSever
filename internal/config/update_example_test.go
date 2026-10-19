package config

import (
	"bufio"
	"os"
	"strings"
	"testing"
)

// desktopUpdateKeys 是进程内桌面更新服务读取的变量；两份 env 示例都要列出它们。
var desktopUpdateKeys = []string{
	"TELESRV_UPDATE_MANIFEST",
	"TELESRV_UPDATE_FILES_DIR",
	"TELESRV_UPDATE_ROOT_PUBLIC_KEY",
	"TELESRV_UPDATE_PUBLIC_URL",
}

// TestExampleConfigurationsKeepDesktopUpdatesOff 保证照示例复制出的 .env 不会启用进程内
// 更新服务：TELESRV_UPDATE_MANIFEST 非空即启用，清单不存在时 telesrv 直接启动失败。
func TestExampleConfigurationsKeepDesktopUpdatesOff(t *testing.T) {
	for _, path := range []string{"../../.env.example", "../../deploy/docker/.env.example"} {
		t.Run(path, func(t *testing.T) {
			active, listed := exampleUpdateKeys(t, path)
			if value := active["TELESRV_UPDATE_MANIFEST"]; value != "" {
				t.Fatalf("TELESRV_UPDATE_MANIFEST=%q: copying the example must keep in-process desktop updates off", value)
			}
			for _, key := range desktopUpdateKeys {
				if !listed[key] {
					t.Errorf("%s does not document %s", path, key)
				}
			}
		})
	}
}

// exampleUpdateKeys 返回示例里生效的 KEY=VALUE，以及出现过（生效或 `# KEY=` 注释）的键。
func exampleUpdateKeys(t *testing.T, path string) (active map[string]string, listed map[string]bool) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = f.Close() }()
	active, listed = map[string]string{}, map[string]bool{}
	scanner := bufio.NewScanner(f)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		commented := strings.HasPrefix(line, "#")
		line = strings.TrimSpace(strings.TrimLeft(line, "#"))
		key, value, ok := strings.Cut(line, "=")
		if !ok || !strings.HasPrefix(key, "TELESRV_UPDATE_") || strings.ContainsAny(key, " \t") {
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
	return active, listed
}

// TestDocsDescribeDesktopUpdateKeys 保证中英文配置文档与更新服务文档列出进程内更新服务读取的
// 全部变量，免得文档落后于实现。
func TestDocsDescribeDesktopUpdateKeys(t *testing.T) {
	for _, path := range []string{
		"../../docs/configuration.en.md",
		"../../docs/configuration.zh-CN.md",
		"../../docs/update-service.md",
	} {
		t.Run(path, func(t *testing.T) {
			data, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			for _, key := range desktopUpdateKeys {
				if !strings.Contains(string(data), "`"+key+"`") {
					t.Errorf("%s does not document `%s`", path, key)
				}
			}
		})
	}
}

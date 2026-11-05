package domain

import "testing"

func TestSuggestLangCode(t *testing.T) {
	android := []string{"ar", "de", "en", "es", "id", "nb", "pt-br", "ru", "zh-hans", "zh-hant"}
	tdesktop := []string{"de", "en", "ru", "zh-hans", "zh-hant"}

	tests := []struct {
		name      string
		system    string
		available []string
		want      string
	}{
		{"简体：中国大陆", "zh-cn", android, "zh-hans"},
		{"简体：新加坡", "zh-SG", android, "zh-hans"},
		{"简体：只有语言码", "zh", android, "zh-hans"},
		{"简体：显式脚本", "zh-Hans-CN", android, "zh-hans"},
		{"繁体：台湾", "zh-tw", android, "zh-hant"},
		{"繁体：香港", "zh_HK", android, "zh-hant"},
		{"繁体：澳门", "zh-mo", android, "zh-hant"},
		{"繁体：显式脚本", "zh-Hant", android, "zh-hant"},
		{"繁体：Android 带变体", "zh-tw_#Hant", android, "zh-hant"},
		{"完整码精确匹配", "pt-BR", android, "pt-br"},
		{"唯一地区变体", "pt-pt", android, "pt-br"},
		{"主语言码", "de-DE", android, "de"},
		{"旧码别名 in", "in-ID", android, "id"},
		{"旧码别名 no", "no", android, "nb"},
		{"平台没有该语言时回落英文", "ar-SA", tdesktop, "en"},
		{"没有的语言回落英文", "ja-jp", android, "en"},
		{"中文平台缺简体时回落英文", "zh-cn", []string{"en", "zh-hant"}, "en"},
		{"空系统语言不下发", "", android, ""},
		{"没有英文且不匹配不下发", "ja", []string{"de"}, ""},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := SuggestLangCode(tt.system, tt.available); got != tt.want {
				t.Fatalf("SuggestLangCode(%q) = %q, want %q", tt.system, got, tt.want)
			}
		})
	}
}

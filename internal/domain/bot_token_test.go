package domain

import (
	"strings"
	"testing"
)

// 用例对照 telegram-bot-api ClientManager::send 与 Client::start_up 的 token 校验，
// secret 部分对照 tdutils base64url_decode。
func TestValidBotAPIToken(t *testing.T) {
	// 26 字节编码出的 35 位随机段：末字符 'A' 的低 2 位为 0。
	const good = "AAHdqTcvCH1vGWJxfSeofSAs0K5PALDsawA"
	cases := []struct {
		name  string
		token string
		want  bool
	}{
		{"generated shape", "1780243200:" + good, true},
		{"minimum secret length 24", "1:" + strings.Repeat("A", 24), true},
		{"padded secret", "1:" + strings.Repeat("A", 22) + "==", true},
		{"secret with url chars", "1:" + strings.Repeat("-_", 12), true},
		{"id just below 2^54", "18014398509481983:" + good, true},

		{"trailing bits set (len%4==3)", "1:" + good[:34] + "B", false},
		{"trailing bits set (len%4==2)", "1:" + strings.Repeat("A", 24) + "AB", false},
		{"length mod 4 is 1", "1:" + strings.Repeat("A", 25), false},
		{"secret shorter than 24", "1:" + strings.Repeat("A", 20), false},
		{"standard base64 char", "1:" + strings.Repeat("A", 23) + "+", false},
		{"newline in secret", "1:" + strings.Repeat("A", 12) + "\n" + strings.Repeat("A", 11), false},
		{"three padding chars", "1:" + strings.Repeat("A", 25) + "===", false},
		{"padding with wrong total length", "1:" + strings.Repeat("A", 22) + "=", false},
		{"missing colon", "1780243200" + good, false},
		{"leading zero", "01780243200:" + good, false},
		{"zero id", "0:" + good, false},
		{"negative id", "-5:" + good, false},
		{"non-decimal id", "12a:" + good, false},
		{"id at 2^54", "18014398509481984:" + good, false},
		{"contains slash", "1:" + good + "/test", false},
		{"longer than 80", "1:" + strings.Repeat("A", 80), false},
		{"empty", "", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidBotAPIToken(tc.token); got != tc.want {
				t.Fatalf("ValidBotAPIToken(%q) = %v, want %v", tc.token, got, tc.want)
			}
		})
	}
}

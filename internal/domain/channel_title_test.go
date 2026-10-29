package domain

import (
	"strings"
	"testing"
)

func TestValidChannelTitle(t *testing.T) {
	cases := []struct {
		name  string
		title string
		want  bool
	}{
		{"empty", "", false},
		{"single char", "a", true},
		{"128 ascii", strings.Repeat("a", 128), true},
		{"129 ascii", strings.Repeat("a", 129), false},
		// 按字符计数：128 个汉字有 384 字节，仍然合法。
		{"128 cjk", strings.Repeat("群", 128), true},
		{"129 cjk", strings.Repeat("群", 129), false},
	}
	for _, tc := range cases {
		if got := ValidChannelTitle(tc.title); got != tc.want {
			t.Errorf("%s: ValidChannelTitle(len=%d) = %v, want %v", tc.name, len(tc.title), got, tc.want)
		}
	}
}

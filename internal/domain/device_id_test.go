package domain

import (
	"strings"
	"testing"
)

func TestValidDeviceID(t *testing.T) {
	for _, tc := range []struct {
		name string
		in   string
		want bool
	}{
		{"uuid", "0f8fad5b-d9cb-469f-a165-70867728950e", true},
		{"single byte", "a", true},
		{"max length", strings.Repeat("x", MaxDeviceIDLength), true},
		{"printable punctuation and space", "PC #1 ~ {id}", true},
		{"empty", "", false},
		{"too long", strings.Repeat("x", MaxDeviceIDLength+1), false},
		{"control character", "abc\n", false},
		{"nul", "a\x00b", false},
		{"delete", "a\x7fb", false},
		{"non-ascii", "设备", false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := ValidDeviceID(tc.in); got != tc.want {
				t.Fatalf("ValidDeviceID(%q) = %v, want %v", tc.in, got, tc.want)
			}
		})
	}
}

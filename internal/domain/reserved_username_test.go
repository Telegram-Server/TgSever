package domain

import "testing"

func TestReservedUsername(t *testing.T) {
	tests := []struct {
		username string
		want     bool
	}{
		{username: "tdhbc", want: true},
		{username: "tdhbcfeed", want: true},
		{username: "tdhbcfeed4", want: true},
		{username: "TdhbcFeed4", want: true},
		{username: "TDHBC_beta", want: true},
		{username: "@tdhbcfeed", want: true},
		{username: "t.me/TdhbcFeed4", want: true},
		{username: "", want: false},
		{username: "tdhb", want: false},
		{username: "durov", want: false},
		{username: "my_tdhbc", want: false},
		{username: "xtdhbcfeed", want: false},
		{username: "feed_tdhbc4", want: false},
	}
	for _, tt := range tests {
		if got := ReservedUsername(tt.username); got != tt.want {
			t.Errorf("ReservedUsername(%q) = %v, want %v", tt.username, got, tt.want)
		}
	}
}

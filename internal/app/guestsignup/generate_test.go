package guestsignup

import (
	"strings"
	"testing"
	"unicode/utf8"

	"telesrv/internal/app/auth"
	"telesrv/internal/domain"
)

func TestWordListsHave500UniqueWords(t *testing.T) {
	for name, words := range map[string][]string{"adjectives": adjectives, "nouns": nouns} {
		if len(words) != 500 {
			t.Errorf("%s has %d words, want 500", name, len(words))
		}
		seen := map[string]bool{}
		for _, w := range words {
			if seen[w] {
				t.Errorf("%s: duplicate %q", name, w)
			}
			seen[w] = true
			if strings.ContainsAny(w, " \t\r") || utf8.RuneCountInString(w) > 8 {
				t.Errorf("%s: bad word %q", name, w)
			}
		}
	}
}

func TestRandomNameShape(t *testing.T) {
	for i := 0; i < 200; i++ {
		name, err := RandomName()
		if err != nil {
			t.Fatal(err)
		}
		adj, noun, ok := strings.Cut(name, "的")
		if !ok || adj == "" || noun == "" {
			t.Fatalf("name %q is not adjective的noun", name)
		}
		if _, _, err := auth.NormalizeSignUpName(name, ""); err != nil {
			t.Fatalf("name %q rejected by sign-up rules: %v", name, err)
		}
	}
}

func TestRandomNumberIsValidGuestNumber(t *testing.T) {
	if size := GuestNumberPoolSize(); size < 10_000 {
		t.Fatalf("guest number pool = %d, want at least 10000", size)
	}
	for _, strict := range []bool{false, true} {
		domain.ConfigurePhoneFormatCheck(strict)
		for i := 0; i < 500; i++ {
			phone, err := RandomNumber()
			if err != nil {
				t.Fatal(err)
			}
			if !IsGuestNumber(phone) || !domain.ValidPhone(phone) {
				t.Fatalf("strict=%v: %q is not a valid guest number", strict, phone)
			}
		}
	}
	domain.ConfigurePhoneFormatCheck(false)
}

func TestIPKey(t *testing.T) {
	cases := map[string]string{
		"203.0.113.9":             "203.0.113.9",
		" 203.0.113.9 ":           "203.0.113.9",
		"::ffff:203.0.113.9":      "203.0.113.9",
		"2001:db8:a:b:c:d:e:f":    "2001:db8:a:b::/64",
		"2001:db8:a:b::1%eth0":    "2001:db8:a:b::/64",
		"fe80::1":                 "fe80::/64",
		"2001:0db8:000a:000b::99": "2001:db8:a:b::/64",
	}
	for in, want := range cases {
		if got, ok := IPKey(in); !ok || got != want {
			t.Errorf("IPKey(%q) = %q, %v; want %q", in, got, ok, want)
		}
	}
	for _, bad := range []string{"", "localhost", "203.0.113.9:443"} {
		if _, ok := IPKey(bad); ok {
			t.Errorf("IPKey(%q) ok, want rejected", bad)
		}
	}
}

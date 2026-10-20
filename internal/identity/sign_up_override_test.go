package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func boolPtr(v bool) *bool { return &v }

func TestStoreSignUpOverrideTriState(t *testing.T) {
	s := NewStore(t.TempDir())

	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.SignUpEnabled != nil {
		t.Fatalf("expected no override before any write, got %v", *info.SignUpEnabled)
	}

	for _, want := range []bool{false, true} {
		if err := s.SetSignUpOverride(boolPtr(want)); err != nil {
			t.Fatal(err)
		}
		info, err = s.Get()
		if err != nil {
			t.Fatal(err)
		}
		if info.SignUpEnabled == nil || *info.SignUpEnabled != want {
			t.Fatalf("override = %v, want %v", info.SignUpEnabled, want)
		}
	}

	if err := s.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.SignUpEnabled != nil {
		t.Fatalf("expected override cleared, got %v", *info.SignUpEnabled)
	}
}

func TestStoreSignUpOverrideAbsentFromLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"name":"Old","description":"d","login_code_message_template":"tpl {{code}}"}`
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := NewStore(dir).Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.SignUpEnabled != nil {
		t.Fatalf("legacy identity.json must read as no override, got %v", *info.SignUpEnabled)
	}
}

func TestStoreSignUpOverridePreservedAcrossOtherSetters(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SetSignUpOverride(boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetText("Name", "desc"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetWelcomeMessageTemplates("p", "e"); err != nil {
		t.Fatal(err)
	}
	if err := s.SetLoginCodeMessageTemplate("c {{code}}"); err != nil {
		t.Fatal(err)
	}
	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.SignUpEnabled == nil || *info.SignUpEnabled {
		t.Fatalf("sign-up override lost after unrelated setters: %+v", info)
	}
	if err := s.SetSignUpOverride(nil); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.Name != "Name" || info.LoginCodeMessageTemplate != "c {{code}}" || info.WelcomeMessagePhoneTemplate != "p" {
		t.Fatalf("clearing the sign-up override touched other fields: %+v", info)
	}
}

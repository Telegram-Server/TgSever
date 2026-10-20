package identity

import (
	"os"
	"path/filepath"
	"testing"
)

func TestStoreCodeLoginOverrideTriState(t *testing.T) {
	s := NewStore(t.TempDir())

	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.CodeLoginEnabled != nil {
		t.Fatalf("expected no override before any write, got %v", *info.CodeLoginEnabled)
	}

	for _, want := range []bool{false, true} {
		if err := s.SetCodeLoginOverride(boolPtr(want)); err != nil {
			t.Fatal(err)
		}
		info, err = s.Get()
		if err != nil {
			t.Fatal(err)
		}
		if info.CodeLoginEnabled == nil || *info.CodeLoginEnabled != want {
			t.Fatalf("override = %v, want %v", info.CodeLoginEnabled, want)
		}
	}

	if err := s.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.CodeLoginEnabled != nil {
		t.Fatalf("expected override cleared, got %v", *info.CodeLoginEnabled)
	}
}

func TestStoreCodeLoginOverrideAbsentFromLegacyFile(t *testing.T) {
	dir := t.TempDir()
	legacy := `{"name":"Old","sign_up_enabled":false}`
	if err := os.WriteFile(filepath.Join(dir, "identity.json"), []byte(legacy), 0o644); err != nil {
		t.Fatal(err)
	}
	info, err := NewStore(dir).Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.CodeLoginEnabled != nil {
		t.Fatalf("legacy identity.json must read as no override, got %v", *info.CodeLoginEnabled)
	}
}

func TestStoreCodeLoginOverrideIndependentOfSignUpOverride(t *testing.T) {
	s := NewStore(t.TempDir())
	if err := s.SetCodeLoginOverride(boolPtr(false)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetSignUpOverride(boolPtr(true)); err != nil {
		t.Fatal(err)
	}
	if err := s.SetText("Name", "desc"); err != nil {
		t.Fatal(err)
	}
	info, err := s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.CodeLoginEnabled == nil || *info.CodeLoginEnabled {
		t.Fatalf("code login override lost after unrelated setters: %+v", info)
	}
	if err := s.SetCodeLoginOverride(nil); err != nil {
		t.Fatal(err)
	}
	info, err = s.Get()
	if err != nil {
		t.Fatal(err)
	}
	if info.SignUpEnabled == nil || !*info.SignUpEnabled || info.Name != "Name" {
		t.Fatalf("clearing the code login override touched other fields: %+v", info)
	}
}

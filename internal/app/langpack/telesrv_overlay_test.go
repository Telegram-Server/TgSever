package langpack

import (
	"context"
	"testing"

	"telesrv/internal/domain"
	"telesrv/internal/store/memory"
)

// telesrv 叠加包：自有键补进各平台同语言包，同键覆盖官方值；版本号为两者之和，
// 全量、增量、按键查询都带叠加结果；没有对应语言的叠加包时原样返回。
func TestTelesrvOverlayMergesIntoPlatformPacks(t *testing.T) {
	ctx := context.Background()
	packs := memory.NewLangPackStore()
	for _, pack := range []domain.LangPack{
		{LangPack: "android", LangCode: "zh-hans", Version: 100, Strings: []domain.LangPackString{
			{Key: "Official", Value: "官方"},
			{Key: "Shared", Value: "官方值"},
		}},
		{LangPack: "android", LangCode: "de", Version: 50, Strings: []domain.LangPackString{
			{Key: "Official", Value: "Offiziell"},
		}},
		{LangPack: TelesrvOverlayPack, LangCode: "zh-hans", Version: 3, Strings: []domain.LangPackString{
			{Key: "TelesrvGuestContinue", Value: "以游客身份继续"},
			{Key: "Shared", Value: "自有值"},
		}},
	} {
		if err := packs.UpsertPack(ctx, pack); err != nil {
			t.Fatalf("seed %s/%s: %v", pack.LangPack, pack.LangCode, err)
		}
	}
	svc := NewService(packs)

	full, err := svc.GetLangPack(ctx, "android", "zh-hans")
	if err != nil {
		t.Fatalf("full: %v", err)
	}
	if full.Version != 103 {
		t.Fatalf("full version = %d, want 100+3", full.Version)
	}
	for key, want := range map[string]string{"Official": "官方", "Shared": "自有值", "TelesrvGuestContinue": "以游客身份继续"} {
		if got := stringValue(full.Strings, key); got != want {
			t.Fatalf("full %s = %q, want %q", key, got, want)
		}
	}
	if len(full.Strings) != 3 {
		t.Fatalf("full has %d strings, want 3 (overlay must replace, not duplicate)", len(full.Strings))
	}

	// 客户端缓存了叠加前的官方版本 100：增量必须带上叠加键。
	diff, err := svc.GetDifference(ctx, "android", "zh-hans", 100)
	if err != nil {
		t.Fatalf("difference: %v", err)
	}
	if diff.Version != 103 || stringValue(diff.Strings, "TelesrvGuestContinue") != "以游客身份继续" {
		t.Fatalf("difference = v%d %+v, want v103 with overlay key", diff.Version, diff.Strings)
	}
	upToDate, err := svc.GetDifference(ctx, "android", "zh-hans", 103)
	if err != nil {
		t.Fatalf("up-to-date difference: %v", err)
	}
	if len(upToDate.Strings) != 0 {
		t.Fatalf("up-to-date difference has %d strings, want 0", len(upToDate.Strings))
	}

	selected, err := svc.GetStrings(ctx, "android", "zh-hans", []string{"TelesrvGuestContinue"})
	if err != nil {
		t.Fatalf("strings: %v", err)
	}
	if stringValue(selected.Strings, "TelesrvGuestContinue") != "以游客身份继续" {
		t.Fatalf("strings = %+v, want overlay key", selected.Strings)
	}

	de, err := svc.GetLangPack(ctx, "android", "de")
	if err != nil {
		t.Fatalf("de: %v", err)
	}
	if de.Version != 50 || len(de.Strings) != 1 {
		t.Fatalf("de = v%d %d strings, want untouched v50 with 1 string", de.Version, len(de.Strings))
	}
}

package push

import (
	"context"
	"errors"
	"strings"
	"testing"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// fakeLangPack 按 langCode → key → value 返回 iOS 语言包字符串；未知语言报错（同 LangPackService）。
type fakeLangPack map[string]map[string]string

func (f fakeLangPack) GetStrings(_ context.Context, langPack, langCode string, keys []string) (domain.LangPack, error) {
	if langPack != "ios" {
		return domain.LangPack{}, errors.New("unexpected pack " + langPack)
	}
	table, ok := f[langCode]
	if !ok {
		return domain.LangPack{}, errors.New("LANG_CODE_NOT_SUPPORTED")
	}
	out := domain.LangPack{LangPack: langPack, LangCode: langCode}
	for _, key := range keys {
		if v, ok := table[key]; ok {
			out.Strings = append(out.Strings, domain.LangPackString{Key: key, Value: v})
		}
	}
	return out, nil
}

func testLangPack() fakeLangPack {
	return fakeLangPack{
		"en": {
			"PUSH_MESSAGE_TEXT":      "%1$@|%2$@",
			"PUSH_MESSAGE_NOTEXT":    "%1$@|New message",
			"PUSH_ENCRYPTED_MESSAGE": "You have a new message%1$@",
		},
		"ru": {
			"PUSH_MESSAGE_NOTEXT": "%1$@|Новое сообщение",
		},
	}
}

func TestRendererSplitsTitleAndBodyFromLangPack(t *testing.T) {
	r := NewRenderer(testLangPack())
	got := r.Render(context.Background(), "en", KeyMessageText, "Alice", "a|b 100%")
	if got.Title != "Alice" || got.Body != "a|b 100%" {
		t.Fatalf("alert = %+v, want title Alice and the raw text (args are not re-split or re-formatted)", got)
	}
	if got := r.Render(context.Background(), "ru", KeyMessageNoText, "Боб"); got.Title != "Боб" || got.Body != "Новое сообщение" {
		t.Fatalf("ru alert = %+v, want the ru string", got)
	}
}

func TestRendererFallsBackToEnglishThenBuiltin(t *testing.T) {
	r := NewRenderer(testLangPack())
	// ru 语言包没有 PUSH_MESSAGE_TEXT，未知语言 xx 整体报错：都回落到 en。
	for _, lang := range []string{"ru", "xx", ""} {
		if got := r.Render(context.Background(), lang, KeyMessageText, "Alice", "hi"); got.Title != "Alice" || got.Body != "hi" {
			t.Fatalf("lang %q alert = %+v, want the en string", lang, got)
		}
	}
	// 没有语言包（未 seed）时用内置英文模板，推送照样能发。
	none := NewRenderer(nil)
	if got := none.Render(context.Background(), "en", KeyMessageNoText, "Alice"); got.Title != "Alice" || got.Body == "" {
		t.Fatalf("builtin alert = %+v, want title Alice and a non-empty body", got)
	}
	if got := none.Render(context.Background(), "en", KeyEncryptedMessage, ""); got.Body != "You have a new message" || got.Title != "" {
		t.Fatalf("builtin generic alert = %+v", got)
	}
}

func TestRendererTruncatesLongText(t *testing.T) {
	r := NewRenderer(testLangPack())
	long := strings.Repeat("я", 5000)
	got := r.Render(context.Background(), "en", KeyMessageText, "Alice", long)
	if n := utf8.RuneCountInString(got.Body); n > maxAlertArgRunes+1 || !utf8.ValidString(got.Body) {
		t.Fatalf("body has %d runes (valid=%v), want truncated to <= %d", n, utf8.ValidString(got.Body), maxAlertArgRunes+1)
	}
}

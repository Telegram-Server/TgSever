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
// "KEY#one"、"KEY#other" 等复数形式像真实语言包一样合并成 KEY 的一条复数字符串返回。
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
			continue
		}
		plural := domain.LangPackString{Key: key, Pluralized: true}
		found := false
		for suffix, form := range map[string]*string{
			"#zero": &plural.ZeroValue, "#one": &plural.OneValue, "#two": &plural.TwoValue,
			"#few": &plural.FewValue, "#many": &plural.ManyValue, "#other": &plural.OtherValue,
		} {
			if v, ok := table[key+suffix]; ok {
				*form, found = v, true
			}
		}
		if found {
			out.Strings = append(out.Strings, plural)
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

// 复数字符串按数量选形式（en：one/other；ru：one/few/many），%n$d 与 %d 都替换成数量参数。
func TestRendererPicksPluralFormByCount(t *testing.T) {
	r := NewRenderer(fakeLangPack{
		"en": {
			"PUSH_MESSAGE_PHOTOS#one":   "%1$@|📷 Photo",
			"PUSH_MESSAGE_PHOTOS#other": "%1$@|📷 %2$d photos",
			"PUSH_COUNTED#one":          "one message",
			"PUSH_COUNTED#other":        "%d messages",
		},
		"ru": {
			"PUSH_MESSAGE_PHOTOS#one":   "%1$@|📷 %2$d фотография",
			"PUSH_MESSAGE_PHOTOS#few":   "%1$@|📷 %2$d фотографии",
			"PUSH_MESSAGE_PHOTOS#many":  "%1$@|📷 %2$d фотографий",
			"PUSH_MESSAGE_PHOTOS#other": "%1$@|📷 %2$d фотографий",
		},
	})
	ctx := context.Background()
	cases := []struct {
		lang  string
		key   Key
		count int
		args  []string
		want  Alert
	}{
		{"en", "MESSAGE_PHOTOS", 1, []string{"Alice", "1"}, Alert{Title: "Alice", Body: "📷 Photo"}},
		{"en", "MESSAGE_PHOTOS", 3, []string{"Alice", "3"}, Alert{Title: "Alice", Body: "📷 3 photos"}},
		{"ru", "MESSAGE_PHOTOS", 2, []string{"Боб", "2"}, Alert{Title: "Боб", Body: "📷 2 фотографии"}},
		{"ru", "MESSAGE_PHOTOS", 5, []string{"Боб", "5"}, Alert{Title: "Боб", Body: "📷 5 фотографий"}},
		{"ru", "MESSAGE_PHOTOS", 21, []string{"Боб", "21"}, Alert{Title: "Боб", Body: "📷 21 фотография"}},
		// ru 没有这个键：回落到 en，按 en 的形式选。
		{"ru", "COUNTED", 4, []string{"4"}, Alert{Body: "4 messages"}},
		{"en", "COUNTED", 1, []string{"1"}, Alert{Body: "one message"}},
	}
	for _, tc := range cases {
		if got := r.RenderCount(ctx, tc.lang, tc.key, tc.count, tc.args...); got != tc.want {
			t.Fatalf("%s %s count %d = %+v, want %+v", tc.lang, tc.key, tc.count, got, tc.want)
		}
	}
	// 只有 other 形式（如 ja 只用 other）时任何数量都取 other；不是复数字符串的键照常渲染。
	only := NewRenderer(fakeLangPack{"en": {"PUSH_X#other": "%1$d items", "PUSH_MESSAGE_DOCS": "%1$@|sent you %2$d files"}})
	if got := only.RenderCount(ctx, "en", "X", 1, "1"); got.Body != "1 items" {
		t.Fatalf("other-only plural = %+v, want the other form", got)
	}
	if got := only.RenderCount(ctx, "en", "MESSAGE_DOCS", 2, "Alice", "2"); got != (Alert{Title: "Alice", Body: "sent you 2 files"}) {
		t.Fatalf("plain key with count = %+v", got)
	}
	// 没有语言包时内置英文模板同样带复数形式。
	none := NewRenderer(nil)
	if got := none.RenderCount(ctx, "en", "CHAT_MESSAGE_VIDEOS", 4, "Alice", "Team", "4"); got != (Alert{Title: "Team", Body: "Alice: 📹 4 videos"}) {
		t.Fatalf("builtin plural = %+v", got)
	}
	if got := none.RenderCount(ctx, "en", "CHAT_MESSAGE_VIDEOS", 1, "Alice", "Team", "1"); got != (Alert{Title: "Team", Body: "Alice: 📹 Video"}) {
		t.Fatalf("builtin plural one = %+v", got)
	}
}

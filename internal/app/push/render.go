package push

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/feature/plural"
	"golang.org/x/text/language"

	"telesrv/internal/domain"
)

// LangPackSource 读取语言包字符串（rpc.LangPackService / app/langpack.Service 满足它）。
type LangPackSource interface {
	GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error)
}

const (
	iosLangPack     = "ios"
	defaultLangCode = "en"
	// maxAlertArgRunes 限制单个文案参数（消息正文、名字）的长度；多字节字符仍可能超出
	// APNs 4KB 上限，最终由 buildMessagePayload 按字节截短 body/title。
	maxAlertArgRunes = 512
)

// builtinTemplates 是语言包缺失时的英文兜底（与 iOS en 语言包逐字一致，keys_test 核对），保证未 seed 语言包时推送照常可用。
var builtinTemplates = map[Key]string{
	"MESSAGE_TEXT":            "%1$@|%2$@",
	"MESSAGE_NOTEXT":          "%1$@|New message",
	"MESSAGE_PHOTO":           "%1$@|📷 Photo",
	"MESSAGE_VIDEO":           "%1$@|📹 Video",
	"MESSAGE_DOC":             "%1$@|📎 Document",
	"MESSAGE_STICKER":         "%1$@|%2$@ Sticker",
	"MESSAGE_AUDIO":           "%1$@|🎤 Voice message",
	"MESSAGE_ROUND":           "%1$@|📺 Video message",
	"MESSAGE_GIF":             "%1$@|📹 GIF",
	"MESSAGE_CONTACT":         "%1$@|👤 %2$@",
	"MESSAGE_GEO":             "%1$@|📍 Location",
	"MESSAGE_GEOLIVE":         "%1$@|Live location",
	"MESSAGE_POLL":            "%1$@|📊 Poll \"%2$@\"",
	"MESSAGE_QUIZ":            "%1$@|sent you the quiz \"%2$@\"",
	"CHAT_MESSAGE_TEXT":       "%2$@|%1$@: %3$@",
	"CHAT_MESSAGE_NOTEXT":     " %2$@|%1$@: New message",
	"CHAT_MESSAGE_PHOTO":      "%2$@|%1$@: 📷 Photo",
	"CHAT_MESSAGE_VIDEO":      "%2$@|%1$@: 📹 Video",
	"CHAT_MESSAGE_DOC":        "%2$@|%1$@: 📎 Document",
	"CHAT_MESSAGE_STICKER":    "%2$@|%1$@: %3$@ Sticker",
	"CHAT_MESSAGE_AUDIO":      "%2$@|%1$@: 🎤 Voice message",
	"CHAT_MESSAGE_ROUND":      "%2$@|%1$@: 📺 Video message",
	"CHAT_MESSAGE_GIF":        "%2$@|%1$@: 📹 GIF",
	"CHAT_MESSAGE_CONTACT":    "%2$@|%1$@: 👤 %3$@",
	"CHAT_MESSAGE_GEO":        "%2$@|%1$@: 📍 Location",
	"CHAT_MESSAGE_GEOLIVE":    "%2$@|%1$@: Live location",
	"CHAT_MESSAGE_POLL":       "%2$@|%1$@: 📊 Poll \"%3$@\"",
	"CHAT_MESSAGE_QUIZ":       "%2$@|%1$@ sent the quiz \"%3$@\" to the group",
	"CHANNEL_MESSAGE_TEXT":    "%1$@|%2$@",
	"CHANNEL_MESSAGE_NOTEXT":  "%1$@|New message",
	"CHANNEL_MESSAGE_PHOTO":   "%1$@|📷 Photo",
	"CHANNEL_MESSAGE_VIDEO":   "%1$@|📹 Video",
	"CHANNEL_MESSAGE_DOC":     "%1$@|📎 Document",
	"CHANNEL_MESSAGE_STICKER": "%1$@|%2$@ Sticker",
	"CHANNEL_MESSAGE_AUDIO":   "%1$@|🎤 Voice message",
	"CHANNEL_MESSAGE_ROUND":   "%1$@|📺 Video message",
	"CHANNEL_MESSAGE_GIF":     "%1$@|📹 GIF",
	"CHANNEL_MESSAGE_CONTACT": "%1$@|👤 %2$@",
	"CHANNEL_MESSAGE_GEO":     "%1$@|📍 Location",
	"CHANNEL_MESSAGE_GEOLIVE": "%1$@|Live location",
	"CHANNEL_MESSAGE_POLL":    "%1$@|📊 Poll \"%2$@\"",
	"CHANNEL_MESSAGE_QUIZ":    "%1$@|posted the quiz \"%2$@\"",
	KeyEncryptedMessage:       "You have a new message%1$@",
	// 相册（albumAlert）：复数键按语言包写成 KEY#one / KEY#other。
	"MESSAGE_PHOTOS#one":           "%1$@|📷 Photo",
	"MESSAGE_PHOTOS#other":         "%1$@|📷 %2$d photos",
	"MESSAGE_VIDEOS#one":           "%1$@|📹 Video",
	"MESSAGE_VIDEOS#other":         "%1$@|📹 %2$d videos",
	"MESSAGE_DOCS":                 "%1$@|sent you %2$d files",
	"MESSAGES#one":                 "%1$@|📷📹 Album",
	"MESSAGES#other":               "%1$@|📷📹 Albums",
	"CHAT_MESSAGE_PHOTOS#one":      "%2$@|%1$@: 📷 Photo",
	"CHAT_MESSAGE_PHOTOS#other":    "%2$@|%1$@: 📷 %3$d photos",
	"CHAT_MESSAGE_VIDEOS#one":      "%2$@|%1$@: 📹 Video",
	"CHAT_MESSAGE_VIDEOS#other":    "%2$@|%1$@: 📹 %3$d videos",
	"CHAT_MESSAGE_DOCS":            "%2$@|%1$@ sent %3$d files",
	"CHAT_MESSAGES#one":            "%2$@|%1$@: 📷 Album",
	"CHAT_MESSAGES#other":          "%2$@|%1$@: 📷 Albums",
	"CHANNEL_MESSAGE_PHOTOS#one":   "%1$@|📷 Photo",
	"CHANNEL_MESSAGE_PHOTOS#other": "%1$@|📷 %2$d photos",
	"CHANNEL_MESSAGE_VIDEOS#one":   "%1$@|📹 Video",
	"CHANNEL_MESSAGE_VIDEOS#other": "%1$@|📹 %2$d videos",
	"CHANNEL_MESSAGE_DOCS":         "%1$@|posted %2$d files",
	"CHANNEL_MESSAGES#one":         "%1$@|📷 Album",
	"CHANNEL_MESSAGES#other":       "%1$@|📷 Albums",
}

// Renderer 用 iOS 语言包的 PUSH_<KEY> 字符串渲染通知文案。
// 字符串格式是 "title|body"（按第一个 | 拆分），参数是 %n$@ / %@ / %n$d / %d 占位符。
// 复数字符串（语言包里的 KEY#one、KEY#other……）由 RenderCount 按数量与语言的 CLDR 规则选形式。
type Renderer struct {
	strings LangPackSource
}

// NewRenderer 创建渲染器；strings 为 nil 时只用内置英文模板。
func NewRenderer(strings LangPackSource) *Renderer {
	return &Renderer{strings: strings}
}

// Render 按 langCode 渲染 key；该语言缺这个键时回落到 en，再回落到内置模板。
func (r *Renderer) Render(ctx context.Context, langCode string, key Key, args ...string) Alert {
	return renderTemplate(r.lookup(ctx, langCode, key, noCount), args)
}

// RenderCount 同 Render，key 是复数字符串时按 count 选形式（该语言的 CLDR 规则；缺这个形式取 other，
// 再取不分复数的值）。数量本身仍要作为参数传入（%n$d）。key 不是复数字符串时与 Render 相同。
func (r *Renderer) RenderCount(ctx context.Context, langCode string, key Key, count int, args ...string) Alert {
	return renderTemplate(r.lookup(ctx, langCode, key, max(count, 0)), args)
}

// noCount 表示不按数量渲染：复数字符串取 other 形式。
const noCount = -1

// jobRenderer 是一个推送任务内的渲染器：按 (语言, 键) 缓存查到的模板，同一任务里给多台设备、
// 多个收件人渲染时语言包只查一次（尤其是每台设备都要的外层通用文案）。只在一个 worker 内使用。
type jobRenderer struct {
	r         *Renderer
	templates map[jobTemplateKey]string
}

type jobTemplateKey struct {
	lang  string
	key   Key
	count int
}

func newJobRenderer(r *Renderer) *jobRenderer {
	return &jobRenderer{r: r, templates: make(map[jobTemplateKey]string)}
}

// Render 同 Renderer.Render，模板在本任务内缓存。
func (j *jobRenderer) Render(ctx context.Context, langCode string, key Key, args ...string) Alert {
	return j.render(ctx, langCode, key, noCount, args)
}

// RenderCount 同 Renderer.RenderCount，模板在本任务内缓存。
func (j *jobRenderer) RenderCount(ctx context.Context, langCode string, key Key, count int, args ...string) Alert {
	return j.render(ctx, langCode, key, max(count, 0), args)
}

func (j *jobRenderer) render(ctx context.Context, langCode string, key Key, count int, args []string) Alert {
	k := jobTemplateKey{lang: langCode, key: key, count: count}
	template, ok := j.templates[k]
	if !ok {
		template = j.r.lookup(ctx, langCode, key, count)
		j.templates[k] = template
	}
	return renderTemplate(template, args)
}

func renderTemplate(template string, args []string) Alert {
	title, body, hasTitle := strings.Cut(template, "|")
	if !hasTitle {
		body, title = title, ""
	}
	args = append([]string(nil), args...)
	for i := range args {
		args[i] = truncateRunes(args[i], maxAlertArgRunes)
	}
	// 去掉首尾空白：语言包个别字符串带多余空格（如 en 的 PUSH_CHAT_MESSAGE_NOTEXT），
	// 空参数（无 emoji 的贴纸）也会留下空格。
	alert := Alert{Body: strings.TrimSpace(formatIOS(body, args))}
	if hasTitle {
		alert.Title = strings.TrimSpace(formatIOS(title, args))
	}
	return alert
}

// lookup 取 key 在 langCode（缺失时 en）下的模板，复数字符串按 count 选形式（noCount 取 other）；
// 语言包都没有时用内置英文模板。
func (r *Renderer) lookup(ctx context.Context, langCode string, key Key, count int) string {
	name := "PUSH_" + string(key)
	if r != nil && r.strings != nil {
		langCode = strings.TrimSpace(langCode)
		codes := []string{defaultLangCode}
		if langCode != "" && langCode != defaultLangCode {
			codes = []string{langCode, defaultLangCode}
		}
		for _, code := range codes {
			pack, err := r.strings.GetStrings(ctx, iosLangPack, code, []string{name})
			if err != nil {
				continue
			}
			for _, s := range pack.Strings {
				if s.Key != name || s.Deleted {
					continue
				}
				if v := pluralValue(s, code, count); v != "" {
					return v
				}
			}
		}
	}
	return builtinTemplate(key, count)
}

// pluralValue 取语言包字符串在 count 下的值：不是复数字符串时就是 Value；否则按 langCode 的
// CLDR 基数规则选形式，该形式为空时取 other，再取不分复数的 Value。
func pluralValue(s domain.LangPackString, langCode string, count int) string {
	if !s.Pluralized {
		return s.Value
	}
	var v string
	switch pluralForm(langCode, count) {
	case plural.Zero:
		v = s.ZeroValue
	case plural.One:
		v = s.OneValue
	case plural.Two:
		v = s.TwoValue
	case plural.Few:
		v = s.FewValue
	case plural.Many:
		v = s.ManyValue
	}
	if v == "" {
		v = s.OtherValue
	}
	if v == "" {
		v = s.Value
	}
	return v
}

// pluralForm 返回 count 在 langCode 下的 CLDR 基数复数形式；noCount 或无法识别的语言取 other / en 规则。
func pluralForm(langCode string, count int) plural.Form {
	if count < 0 {
		return plural.Other
	}
	tag, err := language.Parse(langCode)
	if err != nil {
		tag = language.English
	}
	return plural.Cardinal.MatchPlural(tag, count%10_000_000, 0, 0, 0, 0)
}

// builtinTemplate 取内置英文模板；复数键在 builtinTemplates 里按 "KEY#one"、"KEY#other" 存放（同语言包）。
func builtinTemplate(key Key, count int) string {
	if v, ok := builtinTemplates[key]; ok {
		return v
	}
	if pluralForm(defaultLangCode, count) == plural.One {
		if v, ok := builtinTemplates[key+"#one"]; ok {
			return v
		}
	}
	return builtinTemplates[key+"#other"]
}

// formatIOS 替换 iOS 格式串里的 %n$@、%n$d、%@ 与 %d（后两者按顺序）以及 %%；参数本身原样插入，
// 不再解释（数量参数由调用方格式化成十进制字符串）。
// 越界的占位符替换为空串。
func formatIOS(format string, args []string) string {
	if !strings.Contains(format, "%") {
		return format
	}
	var b strings.Builder
	next := 0
	for i := 0; i < len(format); i++ {
		c := format[i]
		if c != '%' || i+1 >= len(format) {
			b.WriteByte(c)
			continue
		}
		rest := format[i+1:]
		if rest[0] == '%' {
			b.WriteByte('%')
			i++
			continue
		}
		if rest[0] == '@' || rest[0] == 'd' {
			if next < len(args) {
				b.WriteString(args[next])
			}
			next++
			i++
			continue
		}
		digits := 0
		for digits < len(rest) && rest[digits] >= '0' && rest[digits] <= '9' {
			digits++
		}
		if digits > 0 && digits+1 < len(rest) && rest[digits] == '$' && (rest[digits+1] == '@' || rest[digits+1] == 'd') {
			n, _ := strconv.Atoi(rest[:digits])
			if n >= 1 && n <= len(args) {
				b.WriteString(args[n-1])
			}
			i += digits + 2
			continue
		}
		b.WriteByte(c)
	}
	return b.String()
}

func truncateRunes(s string, limit int) string {
	if utf8.RuneCountInString(s) <= limit {
		return s
	}
	n := 0
	for i := range s {
		if n == limit {
			return s[:i] + "…"
		}
		n++
	}
	return s
}

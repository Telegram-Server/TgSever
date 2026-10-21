package push

import (
	"context"
	"strconv"
	"strings"
	"unicode/utf8"

	"telesrv/internal/domain"
)

// LangPackSource 读取语言包字符串（rpc.LangPackService / app/langpack.Service 满足它）。
type LangPackSource interface {
	GetStrings(ctx context.Context, langPack, langCode string, keys []string) (domain.LangPack, error)
}

const (
	iosLangPack     = "ios"
	defaultLangCode = "en"
	// maxAlertArgRunes 限制单个文案参数（消息正文、名字）的长度，保证加密、base64 之后
	// 仍在 APNs 4KB 上限内。
	maxAlertArgRunes = 512
)

// builtinTemplates 是语言包缺失时的英文兜底（与 iOS en 语言包一致），保证未 seed 语言包时推送照常可用。
var builtinTemplates = map[Key]string{
	KeyMessageText:      "%1$@|%2$@",
	KeyMessageNoText:    "%1$@|New message",
	KeyEncryptedMessage: "You have a new message%1$@",
}

// Renderer 用 iOS 语言包的 PUSH_<KEY> 字符串渲染通知文案。
// 字符串格式是 "title|body"（按第一个 | 拆分），参数是 %n$@ / %@ 占位符。
type Renderer struct {
	strings LangPackSource
}

// NewRenderer 创建渲染器；strings 为 nil 时只用内置英文模板。
func NewRenderer(strings LangPackSource) *Renderer {
	return &Renderer{strings: strings}
}

// Render 按 langCode 渲染 key；该语言缺这个键时回落到 en，再回落到内置模板。
func (r *Renderer) Render(ctx context.Context, langCode string, key Key, args ...string) Alert {
	template := r.lookup(ctx, langCode, key)
	title, body, hasTitle := strings.Cut(template, "|")
	if !hasTitle {
		body, title = title, ""
	}
	args = append([]string(nil), args...)
	for i := range args {
		args[i] = truncateRunes(args[i], maxAlertArgRunes)
	}
	alert := Alert{Body: formatIOS(body, args)}
	if hasTitle {
		alert.Title = formatIOS(title, args)
	}
	return alert
}

func (r *Renderer) lookup(ctx context.Context, langCode string, key Key) string {
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
				if s.Key == name && !s.Deleted && s.Value != "" {
					return s.Value
				}
			}
		}
	}
	return builtinTemplates[key]
}

// formatIOS 替换 iOS 格式串里的 %n$@、%@（按顺序）与 %%；参数本身原样插入，不再解释。
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
		if rest[0] == '@' {
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

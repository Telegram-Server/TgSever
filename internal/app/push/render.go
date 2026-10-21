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
	return renderTemplate(r.lookup(ctx, langCode, key), args)
}

// jobRenderer 是一个推送任务内的渲染器：按 (语言, 键) 缓存查到的模板，同一任务里给多台设备、
// 多个收件人渲染时语言包只查一次（尤其是每台设备都要的外层通用文案）。只在一个 worker 内使用。
type jobRenderer struct {
	r         *Renderer
	templates map[jobTemplateKey]string
}

type jobTemplateKey struct {
	lang string
	key  Key
}

func newJobRenderer(r *Renderer) *jobRenderer {
	return &jobRenderer{r: r, templates: make(map[jobTemplateKey]string)}
}

// Render 同 Renderer.Render，模板在本任务内缓存。
func (j *jobRenderer) Render(ctx context.Context, langCode string, key Key, args ...string) Alert {
	k := jobTemplateKey{lang: langCode, key: key}
	template, ok := j.templates[k]
	if !ok {
		template = j.r.lookup(ctx, langCode, key)
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

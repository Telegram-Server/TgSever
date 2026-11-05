package domain

import "strings"

// SuggestLangCode 按客户端 initConnection 上报的 system_lang_code（设备系统语言，
// 如 Android 的 "zh-cn"、"pt-br"、"de-de"）在该平台已有的语言码里挑出推荐语言，
// 用于 help.getConfig 的 suggested_lang_code。客户端拿它去匹配
// langpack.getLanguages 的 lang_code，在欢迎/登录页显示「继续使用 XX 语言」。
//
// 依次尝试：中文按简繁地区归到 zh-hans / zh-hant；完整码精确匹配；旧语言码别名
// （in→id、iw→he、jw→jv、no→nb，与客户端 LocaleController.getLocaleAlias 一致）；
// 主语言码匹配；主语言相同且唯一的地区变体（pt-pt→pt-br）。都不中时回落 "en"
// （与官方一致，客户端收到 en 而系统不是英文时会自行改用系统语言）。
//
// available 里没有 "en" 且其余都不匹配，或 systemLangCode 为空时返回 ""，
// 调用方此时不下发该字段。
func SuggestLangCode(systemLangCode string, available []string) string {
	codes := make(map[string]struct{}, len(available))
	for _, code := range available {
		code = normalizeLangTag(code)
		if code != "" {
			codes[code] = struct{}{}
		}
	}
	has := func(code string) bool {
		_, ok := codes[code]
		return ok
	}

	tag := normalizeLangTag(systemLangCode)
	if tag == "" {
		return ""
	}
	primary, rest, _ := strings.Cut(tag, "-")

	if primary == "zh" {
		if code := chineseVariant(rest); has(code) {
			return code
		}
	}
	if has(tag) {
		return tag
	}
	if alias, ok := legacyLangAliases[primary]; ok {
		primary = alias
		if rest != "" && has(primary+"-"+rest) {
			return primary + "-" + rest
		}
	}
	if has(primary) {
		return primary
	}
	if primary != "zh" {
		match := ""
		for code := range codes {
			if strings.HasPrefix(code, primary+"-") {
				if match != "" {
					match = ""
					break
				}
				match = code
			}
		}
		if match != "" {
			return match
		}
	}
	if has("en") {
		return "en"
	}
	return ""
}

var legacyLangAliases = map[string]string{
	"in": "id",
	"iw": "he",
	"jw": "jv",
	"no": "nb",
}

// chineseVariant 把 zh 之后的子标签（脚本或地区）归到 zh-hans / zh-hant。
// 繁体：显式 hant 脚本，或台湾、香港、澳门地区；其余（含只有 zh）按简体。
func chineseVariant(rest string) string {
	for _, part := range strings.Split(rest, "-") {
		switch part {
		case "hant", "tw", "hk", "mo":
			return "zh-hant"
		case "hans":
			return "zh-hans"
		}
	}
	return "zh-hans"
}

func normalizeLangTag(code string) string {
	code = strings.ToLower(strings.TrimSpace(code))
	code = strings.ReplaceAll(code, "_", "-")
	return strings.Trim(code, "-")
}

package langpack

import (
	"fmt"
	"math"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"telesrv/internal/domain"
)

var langPackNameRE = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,31}$`)
var langCodeRE = regexp.MustCompile(`^[a-z0-9]{1,16}(?:-[a-z0-9]{1,16})*$`)

// ParseTDesktopFile 解析客户端 .strings 文件为 domain 语言包。
func ParseTDesktopFile(path string) (domain.LangPack, error) {
	pack, err := packFromFilename(path)
	if err != nil {
		return domain.LangPack{}, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return domain.LangPack{}, fmt.Errorf("read langpack %q: %w", path, err)
	}

	plain := make([]domain.LangPackString, 0)
	plurals := make(map[string]*domain.LangPackString)
	pluralOrder := make([]string, 0)
	for _, match := range scanTDesktopStrings(string(data)) {
		key := unquoteTDesktop(match[0])
		value := unquoteTDesktop(match[1])
		base, plural := splitPluralKey(key)
		if plural == "" {
			plain = append(plain, domain.LangPackString{Key: key, Value: value})
			continue
		}
		item, ok := plurals[base]
		if !ok {
			plurals[base] = &domain.LangPackString{Key: base, Pluralized: true}
			item = plurals[base]
			pluralOrder = append(pluralOrder, base)
		}
		setPluralValue(item, plural, value)
	}

	pack.Strings = make([]domain.LangPackString, 0, len(plain)+len(pluralOrder))
	pack.Strings = append(pack.Strings, plain...)
	for _, key := range pluralOrder {
		pack.Strings = append(pack.Strings, *plurals[key])
	}
	return pack, nil
}

// scanTDesktopStrings 找出所有 "key" = "value"; 条目，返回未反转义的 key/value 原文。
//
// 语义与正则 (?s)"((?:\\.|[^"\\])*)"\s*=\s*"((?:\\.|[^"\\])*)"; 的 FindAll 完全一致：
// 在每个 " 处尝试匹配，失败就从下一个字节继续找，成功则从匹配结尾继续。手写是因为 Go 的
// regexp 扫一遍内置语言包（113 MB）要十几秒，种子流程和测试都卡在这里。
func scanTDesktopStrings(data string) [][2]string {
	var out [][2]string
	for i := 0; i < len(data); {
		j := strings.IndexByte(data[i:], '"')
		if j < 0 {
			break
		}
		start := i + j
		key, value, end, ok := matchTDesktopString(data, start)
		if !ok {
			i = start + 1
			continue
		}
		out = append(out, [2]string{key, value})
		i = end
	}
	return out
}

// matchTDesktopString 尝试在 data[start]（一个 "）处匹配一条完整条目。
func matchTDesktopString(data string, start int) (key, value string, end int, ok bool) {
	keyEnd, ok := scanTDesktopQuoted(data, start)
	if !ok {
		return "", "", 0, false
	}
	pos := skipTDesktopSpace(data, keyEnd)
	if pos >= len(data) || data[pos] != '=' {
		return "", "", 0, false
	}
	pos = skipTDesktopSpace(data, pos+1)
	if pos >= len(data) || data[pos] != '"' {
		return "", "", 0, false
	}
	valueEnd, ok := scanTDesktopQuoted(data, pos)
	if !ok || valueEnd >= len(data) || data[valueEnd] != ';' {
		return "", "", 0, false
	}
	return data[start+1 : keyEnd-1], data[pos+1 : valueEnd-1], valueEnd + 1, true
}

// scanTDesktopQuoted 从 data[start] 的 " 开始，返回闭合 " 之后的位置。反斜杠吞掉紧随的一个
// 字节即可：多字节字符的后续字节既不是 " 也不是 \，照常按普通字符走。
func scanTDesktopQuoted(data string, start int) (int, bool) {
	for i := start + 1; i < len(data); i++ {
		switch data[i] {
		case '\\':
			i++
		case '"':
			return i + 1, true
		}
	}
	return 0, false
}

// skipTDesktopSpace 跳过 RE2 \s 的字符集 [\t\n\f\r ]。
func skipTDesktopSpace(data string, i int) int {
	for i < len(data) {
		switch data[i] {
		case '\t', '\n', '\f', '\r', ' ':
			i++
		default:
			return i
		}
	}
	return i
}

func packFromFilename(path string) (domain.LangPack, error) {
	name := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	idx := strings.LastIndex(name, "_v")
	if idx <= 0 || idx+2 >= len(name) {
		return domain.LangPack{}, fmt.Errorf("invalid langpack filename %q", filepath.Base(path))
	}
	version, err := strconv.Atoi(name[idx+2:])
	if err != nil {
		return domain.LangPack{}, fmt.Errorf("parse langpack version %q: %w", name[idx+2:], err)
	}

	head := name[:idx]
	dirPack := filepath.Base(filepath.Dir(path))
	prefix := dirPack + "_"
	langPack := ""
	langCode := ""
	if dirPack != "." && dirPack != "" && strings.HasPrefix(head, prefix) {
		langPack = dirPack
		langCode = strings.TrimPrefix(head, prefix)
	} else {
		sep := strings.Index(head, "_")
		if sep <= 0 || sep+1 >= len(head) {
			return domain.LangPack{}, fmt.Errorf("invalid langpack filename %q", filepath.Base(path))
		}
		langPack = head[:sep]
		langCode = head[sep+1:]
	}
	if langPack == "" || langCode == "" {
		return domain.LangPack{}, fmt.Errorf("invalid langpack filename %q", filepath.Base(path))
	}
	langPack = normalizePack(langPack)
	langCode = normalizeCode(langCode)
	if !langPackNameRE.MatchString(langPack) {
		return domain.LangPack{}, fmt.Errorf("invalid langpack name %q in %q", langPack, filepath.Base(path))
	}
	if len(langCode) > 64 || !langCodeRE.MatchString(langCode) {
		return domain.LangPack{}, fmt.Errorf("invalid language code %q in %q", langCode, filepath.Base(path))
	}
	if version <= 0 || version > math.MaxInt32 {
		return domain.LangPack{}, fmt.Errorf("invalid langpack version %d in %q", version, filepath.Base(path))
	}
	return domain.LangPack{
		LangPack: langPack,
		LangCode: langCode,
		Version:  version,
	}, nil
}

func splitPluralKey(key string) (base, plural string) {
	for _, suffix := range []string{"#zero", "#one", "#two", "#few", "#many", "#other"} {
		if strings.HasSuffix(key, suffix) {
			return strings.TrimSuffix(key, suffix), strings.TrimPrefix(suffix, "#")
		}
	}
	return key, ""
}

func setPluralValue(item *domain.LangPackString, plural, value string) {
	switch plural {
	case "zero":
		item.ZeroValue = value
	case "one":
		item.OneValue = value
	case "two":
		item.TwoValue = value
	case "few":
		item.FewValue = value
	case "many":
		item.ManyValue = value
	case "other":
		item.OtherValue = value
	}
}

func unquoteTDesktop(s string) string {
	v, err := strconv.Unquote(`"` + s + `"`)
	if err != nil {
		return s
	}
	return v
}

package langpack

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"sort"
	"testing"
)

// tdesktopStringOracle 是手写扫描器替换掉的原正则，留作对照：两者对任何输入都必须给出
// 同样的条目。
var tdesktopStringOracle = regexp.MustCompile(`(?s)"((?:\\.|[^"\\])*)"\s*=\s*"((?:\\.|[^"\\])*)";`)

func oracleTDesktopStrings(data string) [][2]string {
	var out [][2]string
	for _, m := range tdesktopStringOracle.FindAllStringSubmatch(data, -1) {
		out = append(out, [2]string{m[1], m[2]})
	}
	return out
}

func assertScanMatchesOracle(t *testing.T, name, data string) {
	t.Helper()
	got, want := scanTDesktopStrings(data), oracleTDesktopStrings(data)
	if !reflect.DeepEqual(got, want) {
		n := min(len(got), len(want))
		for i := 0; i < n; i++ {
			if got[i] != want[i] {
				t.Fatalf("%s: entry %d = %q, regexp gives %q", name, i, got[i], want[i])
			}
		}
		t.Fatalf("%s: %d entries, regexp gives %d", name, len(got), len(want))
	}
}

func TestScanTDesktopStringsMatchesRegexp(t *testing.T) {
	cases := map[string]string{
		"empty":            ``,
		"plain":            `"a" = "b";`,
		"spaces":           "\"a\"\t\n\f\r =\r\n \"b\";",
		"vertical tab":     "\"a\"\v= \"b\";",
		"escaped quote":    `"a\"b" = "c\"d";`,
		"escaped slash":    `"a\\" = "b\\";`,
		"escaped newline":  "\"a\\\nb\" = \"c\";",
		"missing semi":     `"a" = "b" "c" = "d";`,
		"retry inside key": `"x "a" = "b";`,
		"dangling escape":  `"a" = "b\`,
		"unterminated":     `"a" = "b`,
		"utf8 escape":      `"ключ\я" = "значение";`,
		"invalid utf8":     "\"a\\\xff\" = \"\xfe\";",
		"comments":         "/* \"a\" = */ \"b\" = \"c\"; // \"d\" = \"e\";",
		"adjacent":         `"a"="b";"c"="d";`,
	}
	for name, data := range cases {
		assertScanMatchesOracle(t, name, data)
	}
}

// 每个客户端目录抽一个文件和正则对照：全量对照要扫 113 MB，正则太慢，放不进常驻测试。
func TestScanTDesktopStringsMatchesRegexpOnBundledSamples(t *testing.T) {
	root := filepath.Join("..", "..", "..", "data", "langpack")
	dirs, err := os.ReadDir(root)
	if err != nil {
		t.Fatalf("read bundled langpacks: %v", err)
	}
	for _, dir := range dirs {
		if !dir.IsDir() {
			continue
		}
		files, err := filepath.Glob(filepath.Join(root, dir.Name(), "*.strings"))
		if err != nil || len(files) == 0 {
			continue
		}
		sort.Strings(files)
		data, err := os.ReadFile(files[0])
		if err != nil {
			t.Fatalf("read %s: %v", files[0], err)
		}
		assertScanMatchesOracle(t, files[0], string(data))
	}
}

func FuzzScanTDesktopStrings(f *testing.F) {
	for _, seed := range []string{`"a" = "b";`, `"a\"" = "\\";`, "\"a\"\n=\t\"b\";\"c", `"x "a"="b";`, "\"\\\xff\"=\"\";"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, data string) {
		assertScanMatchesOracle(t, "fuzz", data)
	})
}

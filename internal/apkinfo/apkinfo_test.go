package apkinfo

import (
	"archive/zip"
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf16"
)

// 以下证书指纹均来自 `apksigner verify --print-certs`（见 testdata/README.md），
// 用官方工具交叉验证本包的解析结果。
const (
	certA = "036c7da6ef8e90cb5731f0d9270ff204b33347b840425e24e9b7d34a30076b2e"
	certB = "1f1421ffbd6984e9faae9e6074e188fb221f21eda378c3a06584e699cc1c1a91"
)

func TestInspectSamples(t *testing.T) {
	v1234 := func(cert string, scheme int) Info {
		return Info{PackageName: "cc.example.app", VersionCode: 1234, VersionName: "12.2.10", MinSDK: 24, SignerCertSHA256: cert, SigningScheme: scheme}
	}
	cases := []struct {
		file string
		want Info
		err  error
	}{
		{file: "a-v1234-v2v3.apk", want: v1234(certA, 3)},
		{file: "a-v1300-v2v3.apk", want: Info{PackageName: "cc.example.app", VersionCode: 1300, VersionName: "12.3.0", MinSDK: 24, SignerCertSHA256: certA, SigningScheme: 3}},
		{file: "b-v1234-v2v3.apk", want: v1234(certB, 3)},
		{file: "a-v1234-v2only.apk", want: v1234(certA, 2)},
		{file: "a-major-v3only.apk", want: Info{PackageName: "cc.example.app", VersionCode: 2<<32 | 7, VersionName: "20.0.7", MinSDK: 28, SignerCertSHA256: certA, SigningScheme: 3}},
		{file: "a-v1234-v1only.apk", err: ErrUnsigned},
		{file: "unsigned-v1234.apk", err: ErrUnsigned},
	}
	for _, tc := range cases {
		t.Run(tc.file, func(t *testing.T) {
			got, err := InspectFile(filepath.Join("testdata", tc.file))
			if tc.err != nil {
				if !errors.Is(err, tc.err) {
					t.Fatalf("err = %v, want %v", err, tc.err)
				}
				return
			}
			if err != nil {
				t.Fatalf("InspectFile: %v", err)
			}
			if got != tc.want {
				t.Fatalf("got  %+v\nwant %+v", got, tc.want)
			}
		})
	}
}

func TestInspectFileMissing(t *testing.T) {
	if _, err := InspectFile(filepath.Join(t.TempDir(), "nope.apk")); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("err = %v, want not exist", err)
	}
}

// ---- 合成 AXML ----

const androidNS = "http://schemas.android.com/apk/res/android"

type axmlBuilder struct {
	utf8   bool
	strs   []string
	resIDs []uint32
	body   []byte
}

type tattr struct {
	ns, name, raw uint32
	typ           uint8
	data          uint32
}

func (b *axmlBuilder) idx(s string) uint32 {
	for i, v := range b.strs {
		if v == s {
			return uint32(i)
		}
	}
	b.strs = append(b.strs, s)
	return uint32(len(b.strs) - 1)
}

// res 登记一个带资源 ID 的属性名；必须在任何 idx 之前调用，使其下标与资源 ID 表对齐。
func (b *axmlBuilder) res(name string, id uint32) uint32 {
	if len(b.strs) != len(b.resIDs) {
		panic("res after idx")
	}
	b.strs = append(b.strs, name)
	b.resIDs = append(b.resIDs, id)
	return uint32(len(b.strs) - 1)
}

func (b *axmlBuilder) str(ns, name uint32, v string) tattr {
	i := b.idx(v)
	return tattr{ns: ns, name: name, raw: i, typ: typeString, data: i}
}

func intAttr(ns, name uint32, v int32) tattr {
	return tattr{ns: ns, name: name, raw: noIndex, typ: typeIntDec, data: uint32(v)}
}

func refAttr(ns, name uint32) tattr {
	return tattr{ns: ns, name: name, raw: noIndex, typ: typeReference, data: 0x7f010000}
}

func le16(b []byte, v uint16) []byte { return binary.LittleEndian.AppendUint16(b, v) }
func le32(b []byte, v uint32) []byte { return binary.LittleEndian.AppendUint32(b, v) }

func chunk(typ uint16, header, payload []byte) []byte {
	out := le16(nil, typ)
	out = le16(out, uint16(8+len(header)))
	out = le32(out, uint32(8+len(header)+len(payload)))
	out = append(out, header...)
	return append(out, payload...)
}

func (b *axmlBuilder) start(name string, attrs ...tattr) {
	ext := le32(nil, noIndex)
	ext = le32(ext, b.idx(name))
	ext = le16(ext, 20)
	ext = le16(ext, 20)
	ext = le16(ext, uint16(len(attrs)))
	ext = le16(ext, 0)
	ext = le16(ext, 0)
	ext = le16(ext, 0)
	for _, a := range attrs {
		ext = le32(ext, a.ns)
		ext = le32(ext, a.name)
		ext = le32(ext, a.raw)
		ext = le16(ext, 8)
		ext = append(ext, 0, a.typ)
		ext = le32(ext, a.data)
	}
	b.body = append(b.body, chunk(resXMLStartElement, le32(le32(nil, 1), noIndex), ext)...)
}

func (b *axmlBuilder) end(name string) {
	ext := le32(le32(nil, noIndex), b.idx(name))
	b.body = append(b.body, chunk(resXMLEndElement, le32(le32(nil, 1), noIndex), ext)...)
}

func (b *axmlBuilder) bytes() []byte {
	var offsets, data []byte
	for _, s := range b.strs {
		offsets = le32(offsets, uint32(len(data)))
		if b.utf8 {
			data = appendUTF8Len(data, len([]rune(s)))
			data = appendUTF8Len(data, len(s))
			data = append(data, s...)
			data = append(data, 0)
		} else {
			u := utf16.Encode([]rune(s))
			if len(u) > 0x7fff {
				data = le16(data, uint16(len(u)>>16)|0x8000)
			}
			data = le16(data, uint16(len(u)))
			for _, c := range u {
				data = le16(data, c)
			}
			data = le16(data, 0)
		}
	}
	for len(data)%4 != 0 {
		data = append(data, 0)
	}
	var flags uint32
	if b.utf8 {
		flags = stringPoolUTF8Flag
	}
	hdr := le32(nil, uint32(len(b.strs)))
	hdr = le32(hdr, 0)
	hdr = le32(hdr, flags)
	hdr = le32(hdr, uint32(28+len(offsets)))
	hdr = le32(hdr, 0)
	body := chunk(resStringPoolType, hdr, append(offsets, data...))
	if len(b.resIDs) > 0 {
		var ids []byte
		for _, id := range b.resIDs {
			ids = le32(ids, id)
		}
		body = append(body, chunk(resXMLResourceMap, nil, ids)...)
	}
	body = append(body, b.body...)
	return chunk(resXMLType, nil, body)
}

func appendUTF8Len(b []byte, n int) []byte {
	if n > 0x7f {
		return append(b, byte(n>>8)|0x80, byte(n))
	}
	return append(b, byte(n))
}

// manifestOpts 描述一个合成清单；零值字段表示不写该属性。
type manifestOpts struct {
	utf8        bool
	noResMap    bool
	pkg         string
	code        int32
	major       int32
	name        string
	nameRef     bool
	codeRef     bool
	minSDK      int32
	root        string
	extraString string
}

func buildManifest(o manifestOpts) []byte {
	b := &axmlBuilder{utf8: o.utf8}
	var codeN, nameN, minN, majorN uint32
	if o.noResMap {
		codeN, nameN, minN, majorN = b.idx("versionCode"), b.idx("versionName"), b.idx("minSdkVersion"), b.idx("versionCodeMajor")
	} else {
		codeN = b.res("versionCode", attrVersionCode)
		nameN = b.res("versionName", attrVersionName)
		minN = b.res("minSdkVersion", attrMinSDKVersion)
		majorN = b.res("versionCodeMajor", attrVersionCodeMajor)
	}
	ns := b.idx(androidNS)
	if o.extraString != "" {
		b.idx(o.extraString)
	}
	var attrs []tattr
	if o.pkg != "" {
		attrs = append(attrs, b.str(noIndex, b.idx("package"), o.pkg))
	}
	if o.major != 0 {
		attrs = append(attrs, intAttr(ns, majorN, o.major))
	}
	switch {
	case o.codeRef:
		attrs = append(attrs, refAttr(ns, codeN))
	case o.code != 0:
		attrs = append(attrs, intAttr(ns, codeN, o.code))
	}
	switch {
	case o.nameRef:
		attrs = append(attrs, refAttr(ns, nameN))
	case o.name != "":
		attrs = append(attrs, b.str(ns, nameN, o.name))
	}
	root := o.root
	if root == "" {
		root = "manifest"
	}
	b.start(root, attrs...)
	if o.minSDK != 0 {
		b.start("uses-sdk", intAttr(ns, minN, o.minSDK))
		b.end("uses-sdk")
	}
	b.start("application")
	b.end("application")
	b.end(root)
	return b.bytes()
}

func TestParseManifestSynthetic(t *testing.T) {
	ok := manifestOpts{pkg: "org.example.демо", code: 42, name: "1.0 β", minSDK: 21}
	cases := []struct {
		name      string
		opts      func(o *manifestOpts)
		utf16Only bool
		want      Info
		wantErr   string
	}{
		{name: "plain", opts: func(o *manifestOpts) {}, want: Info{PackageName: "org.example.демо", VersionCode: 42, VersionName: "1.0 β", MinSDK: 21}},
		// 超过 0x7f 字节时 UTF-8 长度是两字节编码。
		{name: "two-byte lengths", opts: func(o *manifestOpts) { o.name = strings.Repeat("я", 1000) }, want: Info{PackageName: "org.example.демо", VersionCode: 42, VersionName: strings.Repeat("я", 1000), MinSDK: 21}},
		// 超过 0x7fff 个字符时 UTF-16 长度是两个 u16；UTF-8 池放不下这么长的字符串。
		{name: "utf16 extended length", utf16Only: true, opts: func(o *manifestOpts) { o.name = strings.Repeat("x", 40000) }, want: Info{PackageName: "org.example.демо", VersionCode: 42, VersionName: strings.Repeat("x", 40000), MinSDK: 21}},
		{name: "name fallback without resource map", opts: func(o *manifestOpts) { o.noResMap = true }, want: Info{PackageName: "org.example.демо", VersionCode: 42, VersionName: "1.0 β", MinSDK: 21}},
		{name: "versionCodeMajor", opts: func(o *manifestOpts) { o.major = 3; o.code = -1 }, want: Info{PackageName: "org.example.демо", VersionCode: 3<<32 | 0xffffffff, VersionName: "1.0 β", MinSDK: 21}},
		{name: "no uses-sdk", opts: func(o *manifestOpts) { o.minSDK = 0 }, want: Info{PackageName: "org.example.демо", VersionCode: 42, VersionName: "1.0 β"}},
		{name: "missing package", opts: func(o *manifestOpts) { o.pkg = "" }, wantErr: "missing package"},
		{name: "missing versionCode", opts: func(o *manifestOpts) { o.code = 0 }, wantErr: "missing android:versionCode"},
		{name: "negative versionCode", opts: func(o *manifestOpts) { o.code = -5 }, wantErr: "must be positive"},
		{name: "versionCode reference", opts: func(o *manifestOpts) { o.codeRef = true }, wantErr: "versionCode is a resource reference"},
		{name: "missing versionName", opts: func(o *manifestOpts) { o.name = "" }, wantErr: "missing android:versionName"},
		{name: "versionName reference", opts: func(o *manifestOpts) { o.nameRef = true }, wantErr: "not a literal string"},
		{name: "wrong root", opts: func(o *manifestOpts) { o.root = "resources" }, wantErr: "not <manifest>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			o := ok
			tc.opts(&o)
			for _, utf8 := range []bool{false, true} {
				if tc.utf16Only && utf8 {
					continue
				}
				o.utf8 = utf8
				got, err := parseManifest(buildManifest(o))
				if tc.wantErr != "" {
					if !errors.Is(err, ErrNotAPK) || !strings.Contains(err.Error(), tc.wantErr) {
						t.Fatalf("utf8=%v err = %v, want ErrNotAPK containing %q", o.utf8, err, tc.wantErr)
					}
					continue
				}
				if err != nil {
					t.Fatalf("utf8=%v parseManifest: %v", o.utf8, err)
				}
				if got != tc.want {
					t.Fatalf("utf8=%v got %.200v, want %.200v", o.utf8, got, tc.want)
				}
			}
		})
	}
}

func TestParseManifestTruncated(t *testing.T) {
	full := buildManifest(manifestOpts{pkg: "a.b", code: 1, name: "1", minSDK: 21})
	if _, err := parseManifest(full); err != nil {
		t.Fatalf("full manifest: %v", err)
	}
	for n := 0; n < len(full); n++ {
		if _, err := parseManifest(full[:n]); !errors.Is(err, ErrNotAPK) {
			t.Fatalf("prefix %d: err = %v, want ErrNotAPK", n, err)
		}
	}
	// 声明的 chunk 大小超出实际数据。
	bad := bytes.Clone(full)
	binary.LittleEndian.PutUint32(bad[4:], uint32(len(full)+1))
	if _, err := parseManifest(bad); !errors.Is(err, ErrNotAPK) {
		t.Fatalf("oversized root chunk: err = %v", err)
	}
	// 字符串池声明了海量字符串。
	bad = bytes.Clone(full)
	binary.LittleEndian.PutUint32(bad[8+8:], 0x7fffffff)
	if _, err := parseManifest(bad); !errors.Is(err, ErrNotAPK) {
		t.Fatalf("huge string count: err = %v", err)
	}
}

// ---- 合成 zip 与签名块 ----

func buildZip(t testing.TB, files map[string][]byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, data := range files {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Deflate})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write(data); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func lp(parts ...[]byte) []byte {
	var body []byte
	for _, p := range parts {
		body = append(body, p...)
	}
	return append(le32(nil, uint32(len(body))), body...)
}

// signerValue 构造一个 v2/v3 value：一个签名者、一张证书。
func signerValue(cert []byte, v3 bool) []byte {
	signedData := lp(lp(), lp(lp(cert)))
	signer := lp(signedData)
	if v3 {
		signer = lp(signedData, le32(nil, 24), le32(nil, 0x7fffffff), lp(), lp())
	}
	return lp(signer)
}

type pair struct {
	id  uint32
	val []byte
}

func sigBlock(pairs ...pair) []byte {
	var body []byte
	for _, p := range pairs {
		body = binary.LittleEndian.AppendUint64(body, uint64(4+len(p.val)))
		body = le32(body, p.id)
		body = append(body, p.val...)
	}
	n := uint64(len(body) + 24)
	out := binary.LittleEndian.AppendUint64(nil, n)
	out = append(out, body...)
	out = binary.LittleEndian.AppendUint64(out, n)
	return append(out, sigBlockMagic...)
}

// withSigBlock 把签名块插到中央目录之前并修正 EOCD 里的中央目录偏移。
func withSigBlock(t testing.TB, z, block []byte) []byte {
	t.Helper()
	eocd := len(z) - eocdMinSize
	if binary.LittleEndian.Uint32(z[eocd:]) != eocdSignature {
		t.Fatal("zip has a comment; EOCD not at end")
	}
	cd := binary.LittleEndian.Uint32(z[eocd+16:])
	out := append(append(append([]byte(nil), z[:cd]...), block...), z[cd:]...)
	binary.LittleEndian.PutUint32(out[len(out)-eocdMinSize+16:], cd+uint32(len(block)))
	return out
}

func inspectBytes(b []byte) (Info, error) { return Inspect(bytes.NewReader(b), int64(len(b))) }

func TestInspectSyntheticSignatures(t *testing.T) {
	z := buildZip(t, map[string][]byte{"AndroidManifest.xml": buildManifest(manifestOpts{pkg: "a.b", code: 7, name: "0.7"})})
	cert2, cert3 := []byte("v2-certificate-der"), []byte("v3-certificate-der")
	sum := func(b []byte) string { s := sha256.Sum256(b); return hex.EncodeToString(s[:]) }

	cases := []struct {
		name   string
		block  []byte
		scheme int
		cert   string
		errMsg string
	}{
		{name: "v3 preferred over v2", block: sigBlock(pair{0x42726577, make([]byte, 100)}, pair{blockIDV2, signerValue(cert2, false)}, pair{blockIDV3, signerValue(cert3, true)}), scheme: 3, cert: sum(cert3)},
		{name: "v2 only", block: sigBlock(pair{blockIDV2, signerValue(cert2, false)}), scheme: 2, cert: sum(cert2)},
		{name: "empty v3 falls back to v2", block: sigBlock(pair{blockIDV3, lp()}, pair{blockIDV2, signerValue(cert2, false)}), scheme: 2, cert: sum(cert2)},
		{name: "no v2 or v3", block: sigBlock(pair{0x42726577, make([]byte, 8)}), errMsg: "no v3 or v2 signer"},
		{name: "empty block", block: sigBlock(), errMsg: "no v3 or v2 signer"},
		{name: "truncated signer", block: sigBlock(pair{blockIDV2, signerValue(cert2, false)[:9]}), errMsg: "v2 signature"},
		{name: "certificate length overflow", block: sigBlock(pair{blockIDV2, lp(lp(lp(lp(), le32(nil, 0xffffffff))))}), errMsg: "certificates"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, err := inspectBytes(withSigBlock(t, z, tc.block))
			if tc.errMsg != "" {
				if !errors.Is(err, ErrUnsigned) || !strings.Contains(err.Error(), tc.errMsg) {
					t.Fatalf("err = %v, want ErrUnsigned containing %q", err, tc.errMsg)
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if got.SigningScheme != tc.scheme || got.SignerCertSHA256 != tc.cert || got.PackageName != "a.b" || got.VersionCode != 7 {
				t.Fatalf("got %+v, want scheme %d cert %s", got, tc.scheme, tc.cert)
			}
		})
	}
}

func readSample(t testing.TB, name string) []byte {
	t.Helper()
	b, err := os.ReadFile(filepath.Join("testdata", name))
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestInspectMalformed(t *testing.T) {
	good := readSample(t, "a-v1234-v2v3.apk")
	magic := bytes.Index(good, []byte(sigBlockMagic))
	if magic < 0 {
		t.Fatal("sample has no signing block magic")
	}
	footerSize := magic - 8
	blockStart := magic + len(sigBlockMagic) - int(binary.LittleEndian.Uint64(good[footerSize:])) - 8

	// 找到 v3 对的 value 起点（apksigner 把 v2 放在前面，v3 在后）。
	v3Value := -1
	for off := blockStart + 8; off+12 <= magic-8; {
		n := int(binary.LittleEndian.Uint64(good[off:]))
		if binary.LittleEndian.Uint32(good[off+8:]) == blockIDV3 {
			v3Value = off + 12
			break
		}
		off += 8 + n
	}
	if v3Value < 0 {
		t.Fatal("sample has no v3 pair")
	}

	mutate := func(f func(b []byte) []byte) []byte { return f(bytes.Clone(good)) }
	put64 := func(off int, v uint64) func([]byte) []byte {
		return func(b []byte) []byte { binary.LittleEndian.PutUint64(b[off:], v); return b }
	}
	cases := []struct {
		name string
		data []byte
		err  error
	}{
		{name: "empty", data: nil, err: ErrNotAPK},
		{name: "not zip", data: []byte("definitely not a zip file, just some text"), err: ErrNotAPK},
		{name: "truncated zip", data: good[:len(good)/2], err: ErrNotAPK},
		{name: "zip without manifest", data: buildZip(t, map[string][]byte{"classes.dex": []byte("dex")}), err: ErrNotAPK},
		{name: "text manifest", data: buildZip(t, map[string][]byte{"AndroidManifest.xml": []byte("<manifest package=\"a\"/>")}), err: ErrNotAPK},
		{name: "manifest over limit", data: buildZip(t, map[string][]byte{"AndroidManifest.xml": make([]byte, maxManifestSize+1)}), err: ErrNotAPK},
		{name: "valid manifest without signing block", data: buildZip(t, map[string][]byte{"AndroidManifest.xml": buildManifest(manifestOpts{pkg: "a", code: 1, name: "1"})}), err: ErrUnsigned},
		{name: "bad magic", data: mutate(func(b []byte) []byte { b[magic] ^= 0xff; return b }), err: ErrUnsigned},
		{name: "footer size huge", data: mutate(put64(footerSize, 1<<62)), err: ErrUnsigned},
		{name: "footer size beyond file start", data: mutate(put64(footerSize, uint64(magic)+1000)), err: ErrUnsigned},
		{name: "footer size too small", data: mutate(put64(footerSize, 3)), err: ErrUnsigned},
		{name: "header size mismatch", data: mutate(put64(blockStart, 1)), err: ErrUnsigned},
		{name: "pair length overflow", data: mutate(put64(blockStart+8, 1<<40)), err: ErrUnsigned},
		{name: "pair length too small", data: mutate(put64(blockStart+8, 2)), err: ErrUnsigned},
		{name: "signers length overflow", data: mutate(func(b []byte) []byte { binary.LittleEndian.PutUint32(b[v3Value:], 0xfffffff0); return b }), err: ErrUnsigned},
		{name: "certificate length overflow", data: mutate(func(b []byte) []byte {
			// signers | signer | signed data | digests 各一个 u32 长度，再跳过 digests 内容就是 certificates 长度。
			digests := v3Value + 12
			certs := digests + 4 + int(binary.LittleEndian.Uint32(b[digests:]))
			binary.LittleEndian.PutUint32(b[certs+4:], 0x7ffffff0) // 第一张证书的长度
			return b
		}), err: ErrUnsigned},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := inspectBytes(tc.data); !errors.Is(err, tc.err) {
				t.Fatalf("err = %v, want %v", err, tc.err)
			}
		})
	}
}

// 样本 APK 的每个前缀与逐字节翻转都只能返回错误或成功，不能 panic。
func TestInspectSampleCorruptionNoPanic(t *testing.T) {
	good := readSample(t, "a-v1234-v2v3.apk")
	for n := 0; n < len(good); n += 7 {
		_, _ = inspectBytes(good[:n])
	}
	b := bytes.Clone(good)
	for i := range b {
		b[i] ^= 0xa5
		_, _ = inspectBytes(b)
		b[i] ^= 0xa5
	}
}

func FuzzInspect(f *testing.F) {
	for _, name := range []string{"a-v1234-v2v3.apk", "a-v1234-v2only.apk", "a-major-v3only.apk", "a-v1234-v1only.apk", "unsigned-v1234.apk"} {
		f.Add(readSample(f, name))
	}
	f.Add([]byte{})
	f.Add([]byte("PK\x05\x06" + strings.Repeat("\x00", 18)))
	f.Add(buildZip(f, map[string][]byte{"AndroidManifest.xml": buildManifest(manifestOpts{utf8: true, pkg: "a", code: 1, name: "1", minSDK: 1})}))
	f.Fuzz(func(t *testing.T, data []byte) {
		info, err := inspectBytes(data)
		if err == nil {
			if info.PackageName == "" || info.VersionCode <= 0 || info.VersionName == "" || (info.SigningScheme != 2 && info.SigningScheme != 3) || len(info.SignerCertSHA256) != 64 {
				t.Fatalf("success with incomplete info: %+v", info)
			}
		} else if !errors.Is(err, ErrNotAPK) && !errors.Is(err, ErrUnsigned) {
			t.Fatalf("error without sentinel: %v", err)
		}
	})
}

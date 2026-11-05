package apkinfo

import (
	"encoding/binary"
	"fmt"
	"strconv"
	"unicode/utf16"
)

// 本文件是只够读 AndroidManifest.xml 的最小 Android 二进制 XML（AXML）解析器，格式见
// AOSP frameworks/base/libs/androidfw/include/androidfw/ResourceTypes.h。整数均为小端。
//
//	ResXMLTree_header（RES_XML_TYPE）
//	  ResStringPool（RES_STRING_POOL_TYPE）
//	  RES_XML_RESOURCE_MAP_TYPE：u32 数组，第 i 项是字符串 i 对应的属性资源 ID
//	  START_NAMESPACE / START_ELEMENT / END_ELEMENT / END_NAMESPACE / CDATA ...
//
// 每个 chunk 以 ResChunk_header 开头：u16 type | u16 headerSize | u32 size。

const (
	resStringPoolType  = 0x0001
	resXMLType         = 0x0003
	resXMLStartElement = 0x0102
	resXMLEndElement   = 0x0103
	resXMLResourceMap  = 0x0180

	stringPoolUTF8Flag = 1 << 8
	noIndex            = 0xffffffff

	typeReference = 0x01
	typeString    = 0x03
	typeIntDec    = 0x10
	typeIntHex    = 0x11

	attrMinSDKVersion    = 0x0101020c
	attrVersionCode      = 0x0101021b
	attrVersionName      = 0x0101021c
	attrVersionCodeMajor = 0x01010576

	// maxNameLen 是按名字匹配元素与属性时比较的最长字符串；更长的字符串不可能是
	// 我们要找的名字，直接跳过，避免对大字符串反复解码。
	maxNameLen = 64
)

// stringPool 保存字符串池的原始字节，按需解码单个字符串。
type stringPool struct {
	utf8    bool
	offsets []byte // stringCount 个 u32
	strings []byte // 从 stringsStart 到 chunk 末尾
}

func (p *stringPool) count() int { return len(p.offsets) / 4 }

// raw 返回第 idx 个字符串的编码字节（UTF-8 字节或 UTF-16LE 字节）与字符数。
func (p *stringPool) raw(idx uint32) ([]byte, error) {
	if p == nil || uint64(idx) >= uint64(p.count()) {
		return nil, fmt.Errorf("string index %d out of range", idx)
	}
	off := uint64(binary.LittleEndian.Uint32(p.offsets[idx*4:]))
	if off >= uint64(len(p.strings)) {
		return nil, fmt.Errorf("string %d offset %d out of range", idx, off)
	}
	b := p.strings[off:]
	if p.utf8 {
		// UTF-8：字符数与字节数各一个变长长度（1 或 2 字节，首字节高位为续标志）。
		_, n, ok := utf8Len(b)
		if !ok {
			return nil, fmt.Errorf("string %d: truncated length", idx)
		}
		b = b[n:]
		blen, n, ok := utf8Len(b)
		if !ok {
			return nil, fmt.Errorf("string %d: truncated length", idx)
		}
		b = b[n:]
		if uint64(blen) > uint64(len(b)) {
			return nil, fmt.Errorf("string %d: length %d out of range", idx, blen)
		}
		return b[:blen], nil
	}
	// UTF-16：u16 字符数，最高位为续标志时再接一个 u16 组成 31 位长度。
	if len(b) < 2 {
		return nil, fmt.Errorf("string %d: truncated length", idx)
	}
	clen := uint64(binary.LittleEndian.Uint16(b))
	b = b[2:]
	if clen&0x8000 != 0 {
		if len(b) < 2 {
			return nil, fmt.Errorf("string %d: truncated length", idx)
		}
		clen = (clen&0x7fff)<<16 | uint64(binary.LittleEndian.Uint16(b))
		b = b[2:]
	}
	if clen*2 > uint64(len(b)) {
		return nil, fmt.Errorf("string %d: length %d out of range", idx, clen)
	}
	return b[:clen*2], nil
}

func utf8Len(b []byte) (int, int, bool) {
	if len(b) < 1 {
		return 0, 0, false
	}
	if b[0]&0x80 == 0 {
		return int(b[0]), 1, true
	}
	if len(b) < 2 {
		return 0, 0, false
	}
	return int(b[0]&0x7f)<<8 | int(b[1]), 2, true
}

// get 解码第 idx 个字符串。
func (p *stringPool) get(idx uint32) (string, error) {
	b, err := p.raw(idx)
	if err != nil {
		return "", err
	}
	if p.utf8 {
		return string(b), nil
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		u[i] = binary.LittleEndian.Uint16(b[i*2:])
	}
	return string(utf16.Decode(u)), nil
}

// is 判断第 idx 个字符串是否等于 s（s 为 ASCII 短名字）；越界或过长的字符串视为不等。
func (p *stringPool) is(idx uint32, s string) bool {
	b, err := p.raw(idx)
	if err != nil {
		return false
	}
	if p.utf8 {
		return len(b) == len(s) && string(b) == s
	}
	if len(b) != len(s)*2 || len(s) > maxNameLen {
		return false
	}
	for i := 0; i < len(s); i++ {
		if binary.LittleEndian.Uint16(b[i*2:]) != uint16(s[i]) {
			return false
		}
	}
	return true
}

// attr 是 START_ELEMENT 里的一个属性（ResXMLTree_attribute）。
type attr struct {
	ns, name, rawValue uint32
	dataType           uint8
	data               uint32
}

// chunk 读出 data[off:] 处的 chunk 头并检查边界，返回 type、headerSize 与 chunk 字节。
func chunkAt(data []byte, off int) (uint16, int, []byte, error) {
	if off < 0 || len(data)-off < 8 {
		return 0, 0, nil, fmt.Errorf("truncated chunk header at %d", off)
	}
	typ := binary.LittleEndian.Uint16(data[off:])
	hsize := int(binary.LittleEndian.Uint16(data[off+2:]))
	size := uint64(binary.LittleEndian.Uint32(data[off+4:]))
	if hsize < 8 || uint64(hsize) > size || size > uint64(len(data)-off) {
		return 0, 0, nil, fmt.Errorf("chunk 0x%04x at %d: bad sizes header=%d size=%d", typ, off, hsize, size)
	}
	return typ, hsize, data[off : off+int(size)], nil
}

func parseStringPool(c []byte, hsize int) (*stringPool, error) {
	if hsize < 28 {
		return nil, fmt.Errorf("string pool header too short: %d", hsize)
	}
	count := uint64(binary.LittleEndian.Uint32(c[8:]))
	flags := binary.LittleEndian.Uint32(c[16:])
	start := uint64(binary.LittleEndian.Uint32(c[20:]))
	if count*4 > uint64(len(c)-hsize) {
		return nil, fmt.Errorf("string pool: %d strings do not fit", count)
	}
	offEnd := uint64(hsize) + count*4
	if count > 0 && (start < offEnd || start > uint64(len(c))) {
		return nil, fmt.Errorf("string pool: strings start %d out of range", start)
	}
	p := &stringPool{utf8: flags&stringPoolUTF8Flag != 0, offsets: c[hsize:offEnd]}
	if count > 0 {
		p.strings = c[start:]
	}
	return p, nil
}

func parseAttrs(c []byte, hsize int) (name uint32, attrs []attr, err error) {
	// ResXMLTree_attrExt 紧跟在 header 之后：ns, name u32 | attributeStart, attributeSize,
	// attributeCount, idIndex, classIndex, styleIndex u16。
	if len(c)-hsize < 20 {
		return 0, nil, fmt.Errorf("start element truncated")
	}
	ext := c[hsize:]
	name = binary.LittleEndian.Uint32(ext[4:])
	start := int(binary.LittleEndian.Uint16(ext[8:]))
	asize := int(binary.LittleEndian.Uint16(ext[10:]))
	count := int(binary.LittleEndian.Uint16(ext[12:]))
	if count == 0 {
		return name, nil, nil
	}
	if asize < 20 {
		return 0, nil, fmt.Errorf("attribute size %d too small", asize)
	}
	if uint64(start)+uint64(asize)*uint64(count) > uint64(len(ext)) {
		return 0, nil, fmt.Errorf("%d attributes do not fit", count)
	}
	attrs = make([]attr, count)
	for i := range attrs {
		a := ext[start+i*asize:]
		attrs[i] = attr{
			ns:       binary.LittleEndian.Uint32(a[0:]),
			name:     binary.LittleEndian.Uint32(a[4:]),
			rawValue: binary.LittleEndian.Uint32(a[8:]),
			dataType: a[15],
			data:     binary.LittleEndian.Uint32(a[16:]),
		}
	}
	return name, attrs, nil
}

// manifestFields 收集清单里的原始字段，最后在 finish 里统一校验。
type manifestFields struct {
	pkg                    string
	versionCode, codeMajor int64
	haveCode               bool
	versionName            string
	nameErr                error
	minSDK                 int
}

func parseManifest(data []byte) (Info, error) {
	fail := func(format string, args ...any) (Info, error) {
		return Info{}, fmt.Errorf("%w: AndroidManifest.xml: "+format, append([]any{ErrNotAPK}, args...)...)
	}
	typ, hsize, root, err := chunkAt(data, 0)
	if err != nil {
		return fail("%v", err)
	}
	if typ != resXMLType {
		return fail("not binary XML (chunk type 0x%04x)", typ)
	}
	var (
		pool    *stringPool
		resMap  []byte
		depth   int
		started bool
		f       manifestFields
	)
	for off := hsize; off < len(root); {
		typ, ch, c, err := chunkAt(root, off)
		if err != nil {
			return fail("%v", err)
		}
		off += len(c)
		switch typ {
		case resStringPoolType:
			if pool == nil {
				if pool, err = parseStringPool(c, ch); err != nil {
					return fail("%v", err)
				}
			}
		case resXMLResourceMap:
			if resMap == nil {
				resMap = c[ch:]
			}
		case resXMLStartElement:
			if pool == nil {
				return fail("element before string pool")
			}
			name, attrs, err := parseAttrs(c, ch)
			if err != nil {
				return fail("%v", err)
			}
			switch {
			case depth == 0:
				if started {
					return fail("multiple root elements")
				}
				started = true
				if !pool.is(name, "manifest") {
					return fail("root element is not <manifest>")
				}
				if err := f.readManifestAttrs(pool, resMap, attrs); err != nil {
					return fail("%v", err)
				}
			case depth == 1 && pool.is(name, "uses-sdk"):
				f.readUsesSDK(pool, resMap, attrs)
			}
			depth++
		case resXMLEndElement:
			depth--
			if depth < 0 {
				return fail("unbalanced end element")
			}
		}
		if started && depth == 0 {
			break // 根元素已结束，其后的内容与我们无关。
		}
	}
	if !started {
		return fail("no <manifest> element")
	}
	return f.finish()
}

// attrID 返回属性名对应的资源 ID；资源 ID 表里没有时返回 0。
func attrID(resMap []byte, name uint32) uint32 {
	if uint64(name) < uint64(len(resMap)/4) {
		return binary.LittleEndian.Uint32(resMap[name*4:])
	}
	return 0
}

// attrIs 判断属性是否为 id 指定的 android 属性：优先按资源 ID，资源 ID 表没有时按名字。
func attrIs(pool *stringPool, resMap []byte, a attr, id uint32, name string) bool {
	if rid := attrID(resMap, a.name); rid != 0 {
		return rid == id
	}
	return pool.is(a.name, name)
}

// attrString 返回属性的字面字符串值；rawValue 优先，其次 TYPE_STRING 的 data。
func attrString(pool *stringPool, a attr) (string, bool, error) {
	if a.rawValue != noIndex {
		s, err := pool.get(a.rawValue)
		return s, err == nil, err
	}
	if a.dataType == typeString {
		s, err := pool.get(a.data)
		return s, err == nil, err
	}
	return "", false, nil
}

// attrInt 返回整数属性值；字符串形式的十进制数也接受（个别构建工具会这样写）。
func attrInt(pool *stringPool, a attr) (int64, bool) {
	switch a.dataType {
	case typeIntDec, typeIntHex:
		return int64(int32(a.data)), true
	case typeString:
		if b, err := pool.raw(a.data); err != nil || len(b) > maxNameLen {
			return 0, false
		}
		s, err := pool.get(a.data)
		if err != nil {
			return 0, false
		}
		v, err := strconv.ParseInt(s, 10, 32)
		return v, err == nil
	}
	return 0, false
}

func (f *manifestFields) readManifestAttrs(pool *stringPool, resMap []byte, attrs []attr) error {
	for _, a := range attrs {
		switch {
		case a.ns == noIndex && attrID(resMap, a.name) == 0 && pool.is(a.name, "package"):
			s, _, err := attrString(pool, a)
			if err != nil {
				return fmt.Errorf("package: %v", err)
			}
			f.pkg = s
		case attrIs(pool, resMap, a, attrVersionCode, "versionCode"):
			if a.dataType == typeReference {
				return fmt.Errorf("versionCode is a resource reference")
			}
			v, ok := attrInt(pool, a)
			if !ok {
				return fmt.Errorf("versionCode is not an integer (type 0x%02x)", a.dataType)
			}
			f.versionCode, f.haveCode = v, true
		case attrIs(pool, resMap, a, attrVersionCodeMajor, "versionCodeMajor"):
			v, ok := attrInt(pool, a)
			if !ok {
				return fmt.Errorf("versionCodeMajor is not an integer (type 0x%02x)", a.dataType)
			}
			f.codeMajor = v
		case attrIs(pool, resMap, a, attrVersionName, "versionName"):
			if a.dataType != typeString {
				f.nameErr = fmt.Errorf("versionName is not a literal string (type 0x%02x)", a.dataType)
				continue
			}
			s, _, err := attrString(pool, a)
			if err != nil {
				return fmt.Errorf("versionName: %v", err)
			}
			f.versionName = s
		}
	}
	return nil
}

func (f *manifestFields) readUsesSDK(pool *stringPool, resMap []byte, attrs []attr) {
	for _, a := range attrs {
		if attrIs(pool, resMap, a, attrMinSDKVersion, "minSdkVersion") {
			if v, ok := attrInt(pool, a); ok && v > 0 {
				f.minSDK = int(v)
			}
		}
	}
}

func (f *manifestFields) finish() (Info, error) {
	fail := func(msg string) (Info, error) {
		return Info{}, fmt.Errorf("%w: AndroidManifest.xml: %s", ErrNotAPK, msg)
	}
	switch {
	case f.pkg == "":
		return fail("missing package name")
	case !f.haveCode:
		return fail("missing android:versionCode")
	case f.nameErr != nil:
		return fail(f.nameErr.Error())
	case f.versionName == "":
		return fail("missing android:versionName")
	}
	// 没有 versionCodeMajor 时 versionCode 本身必须为正；有 major 时低 32 位按无符号拼接。
	code := f.codeMajor<<32 | int64(uint32(f.versionCode))
	if f.codeMajor < 0 || (f.codeMajor == 0 && f.versionCode <= 0) || code <= 0 {
		return fail(fmt.Sprintf("versionCode must be positive, got major=%d code=%d", f.codeMajor, f.versionCode))
	}
	return Info{
		PackageName: f.pkg,
		VersionCode: code,
		VersionName: f.versionName,
		MinSDK:      f.minSDK,
	}, nil
}

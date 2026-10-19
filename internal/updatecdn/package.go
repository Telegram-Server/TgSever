package updatecdn

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"errors"
	"fmt"
	"io"
	"math"
	"math/big"
	"os"
	"sort"
	"strings"
	"time"
	"unicode/utf8"
)

// 本文件按 tdesktop 客户端 Telegram/SourceFiles/core/update_verify.cpp 逐字节实现
// v2 桌面更新包（TDUP 信封）的解析与验签。服务端与客户端必须对同一个包给出同样的
// 结论，因此格式、长度上限、JSON 字段规则与授权规则都与客户端保持一致；客户端依赖
// 自身状态的 ChannelPolicyAllows 与“版本必须比当前运行版本新”两项检查不在此实现。
//
// 包格式（整数均为小端）：
//
//	"TDUP" | u32 format=2 | u8 channel | u8 os | u8 arch | u64 version | u64 created
//	| u32 len + 密钥清单 JSON                  ← 从魔数到这里为签名区
//	| u32 len + 清单根签名（Ed25519）
//	| u32 count × (u32 len + 密钥 id, u32 len + 签名)
//	| u32 len + 负载                           ← 之后不得有任何字节
//
// 签名输入为 签名区 || SHA256(负载)。

const (
	packageMagic  = "TDUP"
	packageFormat = uint32(2)

	maxPackageManifestSize          = 256 * 1024
	maxPackageManifestSignatureSize = 256
	maxPackageSignatures            = 16
	maxPackageKeyIDSize             = 64
	maxPackageSignatureSize         = 512
	maxPackagePayloadSize           = 256 * 1024 * 1024
	maxManifestKeys                 = 64
	maxManifestChannels             = 16
	maxManifestGroups               = 8
	maxManifestGroupKeys            = 16
	maxManifestRevoked              = 64

	rawSignatureSize  = 64
	rawCoordinateSize = 32

	// packageHeaderSize 是魔数、format、channel/os/arch、version、created 的总长度。
	packageHeaderSize = 4 + 4 + 3 + 8 + 8
)

// MaxDesktopPackageSize 是结构上合法的 v2 包可能达到的最大字节数，读取包文件前用它设上限。
const MaxDesktopPackageSize = packageHeaderSize +
	4 + maxPackageManifestSize +
	4 + maxPackageManifestSignatureSize +
	4 + maxPackageSignatures*(4+maxPackageKeyIDSize+4+maxPackageSignatureSize) +
	4 + maxPackagePayloadSize

// maxPackagePrefixSize 是负载之前全部字段的最大总长。
const maxPackagePrefixSize = MaxDesktopPackageSize - maxPackagePayloadSize

// PackageChannel 是包内声明的发布通道（u8 channel 字段）。
type PackageChannel uint8

const (
	PackageChannelStable        PackageChannel = 0
	PackageChannelBeta          PackageChannel = 1
	PackageChannelCanaryPublic  PackageChannel = 2
	PackageChannelCanaryPrivate PackageChannel = 3
)

// String 返回密钥清单 channels 中使用的通道名。
func (c PackageChannel) String() string {
	switch c {
	case PackageChannelStable:
		return "stable"
	case PackageChannelBeta:
		return "beta"
	case PackageChannelCanaryPublic:
		return "canary-public"
	case PackageChannelCanaryPrivate:
		return "canary-private"
	default:
		return fmt.Sprintf("channel(%d)", uint8(c))
	}
}

// PackageOS 是包内声明的目标操作系统（u8 os 字段）。
type PackageOS uint8

const (
	PackageOSWindows PackageOS = 0
	PackageOSMac     PackageOS = 1
	PackageOSLinux   PackageOS = 2
)

// String 返回 v2 文件名中使用的 {os}。
func (o PackageOS) String() string {
	switch o {
	case PackageOSWindows:
		return "win"
	case PackageOSMac:
		return "mac"
	case PackageOSLinux:
		return "linux"
	default:
		return fmt.Sprintf("os(%d)", uint8(o))
	}
}

// PackageArch 是包内声明的目标架构（u8 arch 字段）。
type PackageArch uint8

const (
	PackageArchX86 PackageArch = 0
	PackageArchX64 PackageArch = 1
	PackageArchArm PackageArch = 2
)

// String 返回 v2 文件名中使用的 {arch}。
func (a PackageArch) String() string {
	switch a {
	case PackageArchX86:
		return "x86"
	case PackageArchX64:
		return "x64"
	case PackageArchArm:
		return "arm"
	default:
		return fmt.Sprintf("arch(%d)", uint8(a))
	}
}

// PackageTarget 是包构建所针对的平台，属于签名区，不能被改投到其他架构。
type PackageTarget struct {
	OS   PackageOS
	Arch PackageArch
}

// String 返回 v2 文件名中的 {os}-{arch}，如 win-x64。
func (t PackageTarget) String() string {
	return t.OS.String() + "-" + t.Arch.String()
}

// VerifiedPackage 是通过完整验签的 v2 包的元数据。
type VerifiedPackage struct {
	Channel PackageChannel
	Target  PackageTarget
	// Version 为 base<<32 | counter。
	Version uint64
	// Created 是 Packer 写入的打包时间（秒级 Unix 时间）。
	Created time.Time
}

// Base 返回版本的高 32 位，即 /current4 宣告的 build。
func (p VerifiedPackage) Base() uint32 { return uint32(p.Version >> 32) }

// Counter 返回版本的低 32 位；stable/beta 包为 0。
func (p VerifiedPackage) Counter() uint32 { return uint32(p.Version) }

// ParseRootPublicKey 解析更新根公钥：PEM 中第一个 PUBLIC KEY（SubjectPublicKeyInfo）块，
// 必须是 Ed25519 公钥。
func ParseRootPublicKey(pemData []byte) (ed25519.PublicKey, error) {
	rest := pemData
	for {
		var block *pem.Block
		block, rest = pem.Decode(rest)
		if block == nil {
			return nil, errors.New("update root public key: no PUBLIC KEY PEM block")
		}
		if block.Type != "PUBLIC KEY" {
			continue
		}
		key, err := x509.ParsePKIXPublicKey(block.Bytes)
		if err != nil {
			return nil, fmt.Errorf("update root public key: %w", err)
		}
		root, ok := key.(ed25519.PublicKey)
		if !ok {
			return nil, fmt.Errorf("update root public key: %T is not an Ed25519 key", key)
		}
		return root, nil
	}
}

// LoadRootPublicKey 从 PEM 文件读取更新根公钥（TELESRV_UPDATE_ROOT_PUBLIC_KEY 指向的文件）。
func LoadRootPublicKey(path string) (ed25519.PublicKey, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read update root public key: %w", err)
	}
	return ParseRootPublicKey(data)
}

// VerifyDesktopPackage 解析并完整验证一个 v2 桌面更新包：先用根公钥验证包内密钥清单的
// 签名再解析清单，然后要求包签名满足清单中该通道的全部签名组（AND-of-ORs）。不解压、
// 不解析负载内容。now 用于判断签名密钥是否过期。
func VerifyDesktopPackage(data []byte, root ed25519.PublicKey, now time.Time) (VerifiedPackage, error) {
	pkg, _, err := VerifyDesktopPackageStream(bytes.NewReader(data), int64(len(data)), root, now)
	return pkg, err
}

// VerifyDesktopPackageStream 与 VerifyDesktopPackage 相同，但从 r 顺序读取一个大小为
// size 的包：只把负载之前的部分（至多几百 KiB）放进内存，负载流式计算 SHA256，内存占用
// 与包大小无关。同时返回整个文件的 SHA256，供清单校验与发布使用。r 读出的字节数
// 必须恰好是 size。
func VerifyDesktopPackageStream(r io.Reader, size int64, root ed25519.PublicKey, now time.Time) (VerifiedPackage, [sha256.Size]byte, error) {
	var fileSum [sha256.Size]byte
	if len(root) == 0 {
		return VerifiedPackage{}, fileSum, errors.New("update root public key is required")
	}
	if len(root) != ed25519.PublicKeySize {
		return VerifiedPackage{}, fileSum, fmt.Errorf("update root public key has %d bytes, want %d", len(root), ed25519.PublicKeySize)
	}
	if size < 0 || size > MaxDesktopPackageSize {
		return VerifiedPackage{}, fileSum, fmt.Errorf("v2 package: size %d exceeds the v2 package limit of %d bytes", size, int64(MaxDesktopPackageSize))
	}

	// 结构上合法的包，负载之前的字段总长不超过 maxPackagePrefixSize，所以只读这么多就能
	// 解析出全部头部字段与负载位置；更长的部分全部属于负载。
	head := make([]byte, min(size, maxPackagePrefixSize))
	if n, err := io.ReadFull(r, head); err != nil {
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return VerifiedPackage{}, fileSum, fmt.Errorf("v2 package: read %d bytes, fewer bytes than its size %d", n, size)
		}
		return VerifiedPackage{}, fileSum, fmt.Errorf("read v2 package: %w", err)
	}
	envelope, err := parsePackageEnvelope(head, size)
	if err != nil {
		return VerifiedPackage{}, fileSum, err
	}

	fileHash := sha256.New()
	payloadHash := sha256.New()
	fileHash.Write(head)
	payloadHash.Write(head[envelope.payloadOffset:])
	rest, err := io.Copy(io.MultiWriter(fileHash, payloadHash), io.LimitReader(r, size-int64(len(head))+1))
	if err != nil {
		return VerifiedPackage{}, fileSum, fmt.Errorf("read v2 package: %w", err)
	}
	switch read := int64(len(head)) + rest; {
	case read < size:
		return VerifiedPackage{}, fileSum, fmt.Errorf("v2 package: read %d bytes, fewer bytes than its size %d", read, size)
	case read > size:
		return VerifiedPackage{}, fileSum, fmt.Errorf("v2 package: read more bytes than its size %d", size)
	}

	manifest, err := parseVerifiedPackageManifest(envelope.manifest, envelope.manifestSignature, root)
	if err != nil {
		return VerifiedPackage{}, fileSum, err
	}
	signingInput := payloadHash.Sum(append(make([]byte, 0, len(envelope.signedRegion)+sha256.Size), envelope.signedRegion...))
	if err := manifest.authorize(envelope.channel, signingInput, envelope.signatures, now.Unix()); err != nil {
		return VerifiedPackage{}, fileSum, err
	}
	fileHash.Sum(fileSum[:0])
	return VerifiedPackage{
		Channel: envelope.channel,
		Target:  envelope.target,
		Version: envelope.version,
		Created: time.Unix(envelope.created, 0),
	}, fileSum, nil
}

type packageSignature struct {
	keyID []byte
	sig   []byte
}

type packageEnvelope struct {
	channel           PackageChannel
	target            PackageTarget
	version           uint64
	created           int64
	manifest          []byte
	manifestSignature []byte
	signatures        []packageSignature
	// payloadOffset 是负载在包内的起始位置；负载一直延伸到包末尾。
	payloadOffset int
	signedRegion  []byte
}

// packageReader 对应客户端的 Reader：越界读取一律失败。
type packageReader struct {
	data   []byte
	offset int
}

func (r *packageReader) u8() (uint8, bool) {
	if len(r.data)-r.offset < 1 {
		return 0, false
	}
	v := r.data[r.offset]
	r.offset++
	return v, true
}

func (r *packageReader) u32() (uint32, bool) {
	if len(r.data)-r.offset < 4 {
		return 0, false
	}
	v := binary.LittleEndian.Uint32(r.data[r.offset:])
	r.offset += 4
	return v, true
}

func (r *packageReader) u64() (uint64, bool) {
	if len(r.data)-r.offset < 8 {
		return 0, false
	}
	v := binary.LittleEndian.Uint64(r.data[r.offset:])
	r.offset += 8
	return v, true
}

func (r *packageReader) bytes(count uint32) ([]byte, bool) {
	if uint64(count) > uint64(len(r.data)-r.offset) {
		return nil, false
	}
	v := r.data[r.offset : r.offset+int(count)]
	r.offset += int(count)
	return v, true
}

// lengthPrefixed 读取 u32 长度和随后的字节；长度必须在 1..max 之间。
func (r *packageReader) lengthPrefixed(max uint32, what string) ([]byte, error) {
	length, ok := r.u32()
	if !ok || length == 0 || length > max {
		return nil, fmt.Errorf("v2 package: bad %s size", what)
	}
	value, ok := r.bytes(length)
	if !ok {
		return nil, fmt.Errorf("v2 package: truncated %s", what)
	}
	return value, nil
}

// parsePackageEnvelope 对应客户端 ParseEnvelope。data 是总长为 size 的包的前缀
// （至少 min(size, maxPackagePrefixSize) 字节）；负载本身不在这里读取，只核对其长度
// 字段与包的实际长度恰好吻合：负载之后有多余字节或负载被截断都拒绝。
func parsePackageEnvelope(data []byte, size int64) (packageEnvelope, error) {
	var result packageEnvelope
	if len(data) < 4 || string(data[:4]) != packageMagic {
		return result, errors.New("v2 package: bad magic, want TDUP")
	}
	r := &packageReader{data: data, offset: 4}

	format, ok := r.u32()
	if !ok || format != packageFormat {
		return result, fmt.Errorf("v2 package: bad format, want %d", packageFormat)
	}
	channel, ok := r.u8()
	if !ok || channel > uint8(PackageChannelCanaryPrivate) {
		return result, errors.New("v2 package: bad channel")
	}
	result.channel = PackageChannel(channel)
	osValue, ok := r.u8()
	if !ok || osValue > uint8(PackageOSLinux) {
		return result, errors.New("v2 package: bad os")
	}
	arch, ok := r.u8()
	if !ok || arch > uint8(PackageArchArm) {
		return result, errors.New("v2 package: bad arch")
	}
	result.target = PackageTarget{OS: PackageOS(osValue), Arch: PackageArch(arch)}
	if result.version, ok = r.u64(); !ok {
		return result, errors.New("v2 package: truncated version")
	}
	created, ok := r.u64()
	if !ok {
		return result, errors.New("v2 package: truncated timestamp")
	}
	// 与客户端一样按有符号数解释。
	result.created = int64(created)

	manifest, err := r.lengthPrefixed(maxPackageManifestSize, "manifest")
	if err != nil {
		return result, err
	}
	result.manifest = manifest
	result.signedRegion = data[:r.offset]

	if result.manifestSignature, err = r.lengthPrefixed(maxPackageManifestSignatureSize, "manifest signature"); err != nil {
		return result, err
	}

	count, ok := r.u32()
	if !ok || count > maxPackageSignatures {
		return result, errors.New("v2 package: bad signature count")
	}
	result.signatures = make([]packageSignature, 0, count)
	for i := uint32(0); i < count; i++ {
		keyID, err := r.lengthPrefixed(maxPackageKeyIDSize, "key id")
		if err != nil {
			return result, err
		}
		sig, err := r.lengthPrefixed(maxPackageSignatureSize, "signature")
		if err != nil {
			return result, err
		}
		result.signatures = append(result.signatures, packageSignature{keyID: keyID, sig: sig})
	}

	payloadLength, ok := r.u32()
	if !ok || payloadLength == 0 || payloadLength > maxPackagePayloadSize {
		return result, errors.New("v2 package: bad payload size")
	}
	result.payloadOffset = r.offset
	switch end := int64(r.offset) + int64(payloadLength); {
	case end > size:
		return result, errors.New("v2 package: truncated payload")
	case end < size:
		return result, fmt.Errorf("v2 package: %d trailing bytes after the payload", size-end)
	}
	return result, nil
}

type packageManifestKey struct {
	id      string
	ed25519 ed25519.PublicKey
	es256   *ecdsa.PublicKey
	expires int64 // 0 表示永不过期
}

type packageManifest struct {
	version  uint32
	keys     []packageManifestKey
	channels map[string][][]string
	revoked  []string
}

// parseVerifiedPackageManifest 对应客户端 ParseVerifiedManifest：先验根签名，
// JSON 解析器只处理经根公钥认证的字节。
func parseVerifiedPackageManifest(manifest, signature []byte, root ed25519.PublicKey) (*packageManifest, error) {
	if len(manifest) == 0 || len(manifest) > maxPackageManifestSize {
		return nil, errors.New("v2 package: bad manifest size")
	}
	if len(signature) != ed25519.SignatureSize || !ed25519.Verify(root, manifest, signature) {
		return nil, errors.New("v2 package: bad manifest root signature")
	}

	// QJsonDocument::fromJson 拒绝非法 UTF-8，encoding/json 则会替换成 U+FFFD 继续解析。
	var object map[string]json.RawMessage
	if !utf8.Valid(manifest) {
		return nil, errors.New("v2 package: could not parse manifest JSON: invalid UTF-8")
	}
	if err := json.Unmarshal(manifest, &object); err != nil || object == nil {
		return nil, errors.New("v2 package: could not parse manifest JSON")
	}
	if format, ok := jsonNumber(object["format"]); !ok || format != 1 {
		return nil, errors.New("v2 package: bad manifest format, want 1")
	}
	version, ok := jsonInt64(object["manifest_version"])
	if !ok || version < 1 || version > math.MaxUint32 {
		return nil, errors.New("v2 package: bad manifest_version")
	}
	result := &packageManifest{version: uint32(version)}

	var err error
	if result.keys, err = parseManifestKeys(object["keys"]); err != nil {
		return nil, err
	}
	if result.channels, err = parseManifestChannels(object["channels"]); err != nil {
		return nil, err
	}
	if result.revoked, err = parseManifestRevoked(object["revoked"]); err != nil {
		return nil, err
	}
	return result, nil
}

// parseManifestKeys 对应客户端 ParseManifestKeys。未知算法的密钥被跳过：它永远
// 不能满足签名组，但不应让携带它的清单失效。
func parseManifestKeys(raw json.RawMessage) ([]packageManifestKey, error) {
	bad := errors.New("v2 package: bad manifest keys")
	list, ok := jsonArray(raw)
	if !ok || len(list) > maxManifestKeys {
		return nil, bad
	}
	keys := make([]packageManifestKey, 0, len(list))
	for _, entry := range list {
		object, ok := jsonObject(entry)
		if !ok {
			return nil, bad
		}
		id, ok := jsonLatin1String(object["id"])
		if !ok || id == "" || len(id) > maxPackageKeyIDSize {
			return nil, bad
		}
		for _, existing := range keys {
			if existing.id == id {
				return nil, bad
			}
		}
		key := packageManifestKey{id: id}
		if expiresRaw, present := object["expires"]; present {
			expires, ok := jsonInt64(expiresRaw)
			if !ok || expires <= 0 {
				return nil, bad
			}
			key.expires = expires
		}
		alg, ok := jsonString(object["alg"])
		if !ok {
			return nil, bad
		}
		switch alg {
		case "Ed25519":
			x, ok := jsonBase64URL(object["x"], rawCoordinateSize)
			if !ok {
				return nil, bad
			}
			key.ed25519 = ed25519.PublicKey(x)
		case "ES256":
			if crv, ok := jsonString(object["crv"]); !ok || crv != "P-256" {
				return nil, bad
			}
			x, okX := jsonBase64URL(object["x"], rawCoordinateSize)
			y, okY := jsonBase64URL(object["y"], rawCoordinateSize)
			if !okX || !okY {
				return nil, bad
			}
			point := append(append([]byte{0x04}, x...), y...)
			pub, err := ecdsa.ParseUncompressedPublicKey(elliptic.P256(), point)
			if err != nil {
				// 客户端在验签时才构造公钥，构造失败只让这把密钥无法给出有效签名。
				pub = nil
			}
			key.es256 = pub
		default:
			continue
		}
		keys = append(keys, key)
	}
	return keys, nil
}

// parseManifestChannels 对应客户端 ParseManifestChannels。
func parseManifestChannels(raw json.RawMessage) (map[string][][]string, error) {
	bad := errors.New("v2 package: bad manifest channels")
	object, ok := jsonObject(raw)
	if !ok || len(object) > maxManifestChannels {
		return nil, bad
	}
	// 按键名排序遍历（QJsonObject 的顺序），转换成 Latin-1 后重名时与客户端的
	// std::map::emplace 一样保留第一个，结果不随 Go map 的随机顺序变化。
	names := make([]string, 0, len(object))
	for name := range object {
		names = append(names, name)
	}
	sort.Strings(names)
	result := make(map[string][][]string, len(object))
	for _, name := range names {
		value := object[name]
		groupsList, ok := jsonArray(value)
		if !ok || len(groupsList) == 0 || len(groupsList) > maxManifestGroups {
			return nil, bad
		}
		groups := make([][]string, 0, len(groupsList))
		for _, groupValue := range groupsList {
			idsList, ok := jsonArray(groupValue)
			if !ok || len(idsList) == 0 || len(idsList) > maxManifestGroupKeys {
				return nil, bad
			}
			ids := make([]string, 0, len(idsList))
			for _, idValue := range idsList {
				id, ok := jsonLatin1String(idValue)
				if !ok || id == "" || len(id) > maxPackageKeyIDSize {
					return nil, bad
				}
				ids = append(ids, id)
			}
			groups = append(groups, ids)
		}
		if key := toLatin1(name); result[key] == nil {
			result[key] = groups
		}
	}
	return result, nil
}

// parseManifestRevoked 对应客户端 ParseManifestRevoked。
func parseManifestRevoked(raw json.RawMessage) ([]string, error) {
	bad := errors.New("v2 package: bad manifest revoked list")
	list, ok := jsonArray(raw)
	if !ok || len(list) > maxManifestRevoked {
		return nil, bad
	}
	result := make([]string, 0, len(list))
	for _, entry := range list {
		id, ok := jsonLatin1String(entry)
		if !ok || id == "" || len(id) > maxPackageKeyIDSize {
			return nil, bad
		}
		result = append(result, id)
	}
	return result, nil
}

// authorize 对应客户端 VerifyChannelAuthorization：该通道每个签名组都必须至少有一把
// 已列出、未吊销、未过期的密钥对签名输入给出有效签名。
func (m *packageManifest) authorize(channel PackageChannel, signingInput []byte, signatures []packageSignature, now int64) error {
	groups := m.channels[channel.String()]
	if len(groups) == 0 {
		return fmt.Errorf("v2 package: channel %s is not in the manifest", channel)
	}
	for index, group := range groups {
		if !m.groupSatisfied(group, signingInput, signatures, now) {
			return fmt.Errorf("v2 package: no valid signature for %s signing group %d %v", channel, index+1, group)
		}
	}
	return nil
}

func (m *packageManifest) groupSatisfied(group []string, signingInput []byte, signatures []packageSignature, now int64) bool {
	for _, id := range group {
		if m.isRevoked(id) {
			continue
		}
		key := m.findKey(id)
		if key == nil || (key.expires > 0 && key.expires <= now) {
			continue
		}
		for _, signature := range signatures {
			if string(signature.keyID) == id && key.verify(signingInput, signature.sig) {
				return true
			}
		}
	}
	return false
}

func (m *packageManifest) isRevoked(id string) bool {
	for _, revoked := range m.revoked {
		if revoked == id {
			return true
		}
	}
	return false
}

func (m *packageManifest) findKey(id string) *packageManifestKey {
	for i := range m.keys {
		if m.keys[i].id == id {
			return &m.keys[i]
		}
	}
	return nil
}

// verify 对应客户端 VerifySignature：Ed25519 直接验签名输入；ES256 验签名输入的
// SHA-256，签名为原始 r||s 64 字节。
func (k *packageManifestKey) verify(signingInput, signature []byte) bool {
	if len(signingInput) == 0 || len(signature) != rawSignatureSize {
		return false
	}
	if k.ed25519 != nil {
		return ed25519.Verify(k.ed25519, signingInput, signature)
	}
	if k.es256 == nil {
		return false
	}
	digest := sha256.Sum256(signingInput)
	r := new(big.Int).SetBytes(signature[:rawCoordinateSize])
	s := new(big.Int).SetBytes(signature[rawCoordinateSize:])
	return ecdsa.Verify(k.es256, digest[:], r, s)
}

// 以下 JSON 辅助函数按 QJsonValue 的类型判断取值：类型不符即视为缺失。

func jsonKind(raw json.RawMessage) byte {
	trimmed := bytes.TrimLeft(raw, " \t\r\n")
	if len(trimmed) == 0 {
		return 0
	}
	return trimmed[0]
}

func jsonNumber(raw json.RawMessage) (float64, bool) {
	kind := jsonKind(raw)
	if kind != '-' && (kind < '0' || kind > '9') {
		return 0, false
	}
	var value float64
	if err := json.Unmarshal(raw, &value); err != nil {
		return 0, false
	}
	return value, true
}

// jsonInt64 对应客户端 ReadInt64：JSON 数字必须是 ±2^53 以内的整数。
func jsonInt64(raw json.RawMessage) (int64, bool) {
	value, ok := jsonNumber(raw)
	if !ok || !(value > -9007199254740992) || !(value < 9007199254740992) || value != math.Trunc(value) {
		return 0, false
	}
	return int64(value), true
}

func jsonString(raw json.RawMessage) (string, bool) {
	if jsonKind(raw) != '"' {
		return "", false
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil {
		return "", false
	}
	return value, true
}

// jsonLatin1String 读取字符串并按 QString::toLatin1 转换，客户端按转换后的字节比较密钥 id。
func jsonLatin1String(raw json.RawMessage) (string, bool) {
	value, ok := jsonString(raw)
	if !ok {
		return "", false
	}
	return toLatin1(value), true
}

// toLatin1 模拟 QString::toLatin1：U+00FF 以内的字符取其低字节，其余每个 UTF-16
// 码元替换为 '?'（BMP 外的字符占两个码元）。
func toLatin1(value string) string {
	ascii := true
	for i := 0; i < len(value); i++ {
		if value[i] >= utf8.RuneSelf {
			ascii = false
			break
		}
	}
	if ascii {
		return value
	}
	var b strings.Builder
	for _, r := range value {
		switch {
		case r <= 0xFF:
			b.WriteByte(byte(r))
		case r > 0xFFFF:
			b.WriteString("??")
		default:
			b.WriteByte('?')
		}
	}
	return b.String()
}

func jsonArray(raw json.RawMessage) ([]json.RawMessage, bool) {
	if jsonKind(raw) != '[' {
		return nil, false
	}
	var value []json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

func jsonObject(raw json.RawMessage) (map[string]json.RawMessage, bool) {
	if jsonKind(raw) != '{' {
		return nil, false
	}
	var value map[string]json.RawMessage
	if err := json.Unmarshal(raw, &value); err != nil {
		return nil, false
	}
	return value, true
}

// jsonBase64URL 解码 base64url 原始坐标，接受有无尾部 '=' 两种写法，长度必须恰好为 size。
// Go 的解码器会忽略换行，Qt 的 AbortOnBase64DecodingErrors 不会，这里显式拒绝。
func jsonBase64URL(raw json.RawMessage, size int) ([]byte, bool) {
	value, ok := jsonString(raw)
	if !ok || strings.ContainsAny(value, "\r\n") {
		return nil, false
	}
	encoding := base64.RawURLEncoding
	if strings.HasSuffix(value, "=") {
		encoding = base64.URLEncoding
	}
	decoded, err := encoding.DecodeString(value)
	if err != nil || len(decoded) != size {
		return nil, false
	}
	return decoded, true
}

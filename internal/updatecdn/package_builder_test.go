package updatecdn

import (
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"crypto/x509"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"sync"
	"testing"
)

// 本文件是测试用的 v2 包构造器：用标准库现场生成根 Ed25519 密钥与 Ed25519、ES256
// 签名密钥，按 tdesktop Packer（BuildV2Envelope）的字节布局构造并签名清单与包。
// 不依赖任何外部样本文件。

type testSigner struct {
	id string
	ed ed25519.PrivateKey
	es *ecdsa.PrivateKey
}

// sign 对签名输入签名：Ed25519 直接签；ES256 签其 SHA-256，输出原始 r||s 64 字节。
func (s testSigner) sign(t *testing.T, input []byte) []byte {
	t.Helper()
	if s.ed != nil {
		return ed25519.Sign(s.ed, input)
	}
	digest := sha256.Sum256(input)
	r, sv, err := ecdsa.Sign(rand.Reader, s.es, digest[:])
	if err != nil {
		t.Fatal(err)
	}
	raw := make([]byte, 64)
	r.FillBytes(raw[:32])
	sv.FillBytes(raw[32:])
	return raw
}

// manifestKey 返回该签名密钥在密钥清单 keys 数组中的 JSON 条目。
func (s testSigner) manifestKey() map[string]any {
	if s.ed != nil {
		return map[string]any{
			"id": s.id, "alg": "Ed25519",
			"x": base64.RawURLEncoding.EncodeToString(s.ed.Public().(ed25519.PublicKey)),
		}
	}
	point, err := s.es.PublicKey.Bytes()
	if err != nil {
		panic(err)
	}
	return map[string]any{
		"id": s.id, "alg": "ES256", "crv": "P-256",
		"x": base64.RawURLEncoding.EncodeToString(point[1:33]),
		"y": base64.RawURLEncoding.EncodeToString(point[33:65]),
	}
}

type testKeySet struct {
	root    ed25519.PrivateKey
	rootPub ed25519.PublicKey
	rootPEM []byte
	ed      testSigner
	ed2     testSigner
	es      testSigner
}

var (
	testKeysOnce sync.Once
	testKeysVal  *testKeySet
)

// testKeys 在整个测试二进制内只生成一次密钥，避免每个用例重复生成拖慢测试。
func testKeys(t *testing.T) *testKeySet {
	t.Helper()
	testKeysOnce.Do(func() {
		rootPub, root, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		_, ed, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		_, ed2, err := ed25519.GenerateKey(rand.Reader)
		if err != nil {
			panic(err)
		}
		es, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
		if err != nil {
			panic(err)
		}
		testKeysVal = &testKeySet{
			root:    root,
			rootPub: rootPub,
			rootPEM: publicKeyPEM(rootPub),
			ed:      testSigner{id: "ed-test", ed: ed},
			ed2:     testSigner{id: "ed-second", ed: ed2},
			es:      testSigner{id: "es-test", es: es},
		}
	})
	return testKeysVal
}

func publicKeyPEM(key any) []byte {
	der, err := x509.MarshalPKIXPublicKey(key)
	if err != nil {
		panic(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "PUBLIC KEY", Bytes: der})
}

// defaultTestManifest 返回一份合法的密钥清单：stable 与 beta 都只要求 ed-test 签名。
func defaultTestManifest(keys *testKeySet) map[string]any {
	return map[string]any{
		"format":           1,
		"manifest_version": 1,
		"issued":           1755600000,
		"expires":          1900000000,
		"keys":             []any{keys.ed.manifestKey(), keys.ed2.manifestKey(), keys.es.manifestKey()},
		"channels": map[string]any{
			"stable": []any{[]any{"ed-test"}},
			"beta":   []any{[]any{"ed-test"}},
		},
		"revoked": []any{},
	}
}

type testEnvelopeSignature struct {
	keyID string
	sig   []byte
}

// testPackageSpec 描述一个待构造的包；buildTestPackage 先按默认值填好，
// 再交给调用方修改，最后序列化。
type testPackageSpec struct {
	magic   string
	format  uint32
	channel uint8
	os      uint8
	arch    uint8
	version uint64
	created uint64

	// manifest 为 nil 时由 manifestDoc 紧凑序列化得到。
	manifestDoc map[string]any
	manifest    []byte
	// manifestSig 为 nil 时用根私钥对 manifest 签名。
	manifestSig []byte

	// signers 在签名区与负载确定后对签名输入签名，按顺序写入签名列表。
	signers []testSigner
	// corruptSignatures 把 signers 产生的每个签名的首字节翻转。
	corruptSignatures bool
	// extraSignatures 原样追加在 signers 的签名之后。
	extraSignatures []testEnvelopeSignature

	payload []byte
	trailer []byte
}

func defaultTestPackageSpec(keys *testKeySet) *testPackageSpec {
	return &testPackageSpec{
		magic:       "TDUP",
		format:      2,
		channel:     0, // stable
		os:          0, // windows
		arch:        1, // x64
		version:     uint64(7000007) << 32,
		created:     1760000000,
		manifestDoc: defaultTestManifest(keys),
		signers:     []testSigner{keys.ed},
		payload:     []byte("compressed-update-payload"),
	}
}

// testPlatformTargets 是平台键到包头 os/arch 字节的映射，与 tdesktop TargetFromPlatformKey 一致。
var testPlatformTargets = map[string][2]uint8{
	"win":    {0, 0},
	"win64":  {0, 1},
	"winarm": {0, 2},
	"mac":    {1, 1},
	"armac":  {1, 2},
	"linux":  {2, 1},
}

// testDesktopPackage 构造与清单条目 desktop.<platform>.<channel>（build）完全一致的合法包。
// 未知平台或通道按 win-x64、stable 构造，供在包校验之前就失败的用例使用。
func testDesktopPackage(t *testing.T, platform, channel string, build uint64) []byte {
	t.Helper()
	return buildTestPackage(t, func(s *testPackageSpec) {
		if target, ok := testPlatformTargets[platform]; ok {
			s.os, s.arch = target[0], target[1]
		}
		if channel == "beta" {
			s.channel = 1
		}
		s.version = build << 32
	})
}

// buildTestPackage 构造一个默认合法（stable、win-x64、build 7000007、ed-test 签名）的包，
// mutate 可以在序列化前修改任意字段。
func buildTestPackage(t *testing.T, mutate func(*testPackageSpec)) []byte {
	t.Helper()
	keys := testKeys(t)
	spec := defaultTestPackageSpec(keys)
	if mutate != nil {
		mutate(spec)
	}
	return serializeTestPackage(t, keys, spec)
}

func serializeTestPackage(t *testing.T, keys *testKeySet, spec *testPackageSpec) []byte {
	t.Helper()
	manifest := spec.manifest
	if manifest == nil {
		var err error
		manifest, err = json.Marshal(spec.manifestDoc)
		if err != nil {
			t.Fatal(err)
		}
	}
	manifestSig := spec.manifestSig
	if manifestSig == nil {
		manifestSig = ed25519.Sign(keys.root, manifest)
	}

	var region []byte
	region = append(region, spec.magic...)
	region = binary.LittleEndian.AppendUint32(region, spec.format)
	region = append(region, spec.channel, spec.os, spec.arch)
	region = binary.LittleEndian.AppendUint64(region, spec.version)
	region = binary.LittleEndian.AppendUint64(region, spec.created)
	region = binary.LittleEndian.AppendUint32(region, uint32(len(manifest)))
	region = append(region, manifest...)

	payloadHash := sha256.Sum256(spec.payload)
	signingInput := append(append([]byte(nil), region...), payloadHash[:]...)
	signatures := make([]testEnvelopeSignature, 0, len(spec.signers)+len(spec.extraSignatures))
	for _, signer := range spec.signers {
		sig := signer.sign(t, signingInput)
		if spec.corruptSignatures {
			sig[0] ^= 0xFF
		}
		signatures = append(signatures, testEnvelopeSignature{keyID: signer.id, sig: sig})
	}
	signatures = append(signatures, spec.extraSignatures...)

	data := region
	data = binary.LittleEndian.AppendUint32(data, uint32(len(manifestSig)))
	data = append(data, manifestSig...)
	data = binary.LittleEndian.AppendUint32(data, uint32(len(signatures)))
	for _, signature := range signatures {
		data = binary.LittleEndian.AppendUint32(data, uint32(len(signature.keyID)))
		data = append(data, signature.keyID...)
		data = binary.LittleEndian.AppendUint32(data, uint32(len(signature.sig)))
		data = append(data, signature.sig...)
	}
	data = binary.LittleEndian.AppendUint32(data, uint32(len(spec.payload)))
	data = append(data, spec.payload...)
	data = append(data, spec.trailer...)
	return data
}

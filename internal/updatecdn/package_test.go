package updatecdn

import (
	"bytes"
	"crypto/ecdsa"
	"crypto/ed25519"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/sha256"
	"encoding/binary"
	"encoding/json"
	"encoding/pem"
	"fmt"
	"strings"
	"testing"
	"time"
)

var testNow = time.Unix(1770000000, 0)

func TestVerifyDesktopPackageAcceptsEd25519Package(t *testing.T) {
	keys := testKeys(t)
	data := buildTestPackage(t, nil)

	pkg, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
	if err != nil {
		t.Fatalf("VerifyDesktopPackage() error = %v", err)
	}
	if pkg.Channel != PackageChannelStable || pkg.Channel.String() != "stable" {
		t.Fatalf("channel = %v", pkg.Channel)
	}
	if pkg.Target.String() != "win-x64" {
		t.Fatalf("target = %v", pkg.Target)
	}
	if pkg.Version != 0x006ACFC7_00000000 || pkg.Base() != 7000007 || pkg.Counter() != 0 {
		t.Fatalf("version = %#x base = %d counter = %d", pkg.Version, pkg.Base(), pkg.Counter())
	}
	if !pkg.Created.Equal(time.Unix(1760000000, 0)) {
		t.Fatalf("created = %v", pkg.Created)
	}
}

func TestVerifyDesktopPackageReportsHeaderFields(t *testing.T) {
	keys := testKeys(t)
	data := buildTestPackage(t, func(s *testPackageSpec) {
		s.channel, s.os, s.arch = 1, 1, 2 // beta, mac-arm
		s.version = uint64(7000008)<<32 | 3
		s.created = 1765000000
	})
	pkg, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if pkg.Channel != PackageChannelBeta || pkg.Target != (PackageTarget{OS: PackageOSMac, Arch: PackageArchArm}) ||
		pkg.Target.String() != "mac-arm" || pkg.Base() != 7000008 || pkg.Counter() != 3 ||
		pkg.Created.Unix() != 1765000000 {
		t.Fatalf("package = %+v", pkg)
	}
}

// manifestChannels 替换密钥清单中的 channels。
func manifestChannels(channels map[string]any) func(*testPackageSpec) {
	return func(s *testPackageSpec) { s.manifestDoc["channels"] = channels }
}

// setKeyField 修改 keys 数组中指定 id 的密钥条目。
func setKeyField(s *testPackageSpec, id, field string, value any) {
	for _, entry := range s.manifestDoc["keys"].([]any) {
		key := entry.(map[string]any)
		if key["id"] == id {
			if value == nil {
				delete(key, field)
			} else {
				key[field] = value
			}
			return
		}
	}
	panic("no key " + id)
}

func testIDs(prefix string, n int) []any {
	ids := make([]any, n)
	for i := range ids {
		ids[i] = fmt.Sprintf("%s-%d", prefix, i)
	}
	return ids
}

func TestVerifyDesktopPackageAuthorization(t *testing.T) {
	keys := testKeys(t)
	tests := []struct {
		name    string
		mutate  func(*testPackageSpec)
		wantErr string
	}{
		{name: "ed25519 key", mutate: nil},
		{name: "es256 key", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"es-test"}}}
			s.signers = []testSigner{keys.es}
		}},
		{name: "two groups both signed", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ed-test"}, []any{"es-test"}}}
			s.signers = []testSigner{keys.es, keys.ed}
		}},
		{name: "any key of a group satisfies it", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ed-second", "es-test"}}}
			s.signers = []testSigner{keys.es}
		}},
		{name: "group missing a signature", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ed-test"}, []any{"es-test"}}}
			s.signers = []testSigner{keys.ed}
		}, wantErr: "no valid signature for stable signing group 2 [es-test]"},
		{name: "no signatures at all", mutate: func(s *testPackageSpec) { s.signers = nil },
			wantErr: "no valid signature for stable signing group 1"},
		{name: "signature by a key outside the group", mutate: func(s *testPackageSpec) {
			s.signers = []testSigner{keys.ed2}
		}, wantErr: "no valid signature for stable signing group 1"},
		{name: "signature labeled with another key id", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"es-test"}}}
			s.signers = []testSigner{{id: "es-test", ed: keys.ed.ed}}
		}, wantErr: "no valid signature"},
		{name: "key listed in a group but not in keys", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ghost"}}}
			s.signers = []testSigner{{id: "ghost", ed: keys.ed.ed}}
		}, wantErr: "no valid signature"},
		{name: "corrupt ed25519 signature", mutate: func(s *testPackageSpec) { s.corruptSignatures = true },
			wantErr: "no valid signature"},
		{name: "corrupt es256 signature", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"es-test"}}}
			s.signers = []testSigner{keys.es}
			s.corruptSignatures = true
		}, wantErr: "no valid signature"},
		{name: "revoked key", mutate: func(s *testPackageSpec) { s.manifestDoc["revoked"] = []any{"ed-test"} },
			wantErr: "no valid signature for stable signing group 1"},
		{name: "revoked key with another valid key in the group", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ed-test", "es-test"}}}
			s.manifestDoc["revoked"] = []any{"ed-test"}
			s.signers = []testSigner{keys.ed, keys.es}
		}},
		{name: "expired key", mutate: func(s *testPackageSpec) { setKeyField(s, "ed-test", "expires", testNow.Unix()) },
			wantErr: "no valid signature for stable signing group 1"},
		{name: "key expiring in the future", mutate: func(s *testPackageSpec) {
			setKeyField(s, "ed-test", "expires", testNow.Unix()+1)
		}},
		{name: "key without expires never expires", mutate: func(s *testPackageSpec) {
			setKeyField(s, "ed-test", "expires", nil)
		}},
		{name: "explicit zero expires is malformed", mutate: func(s *testPackageSpec) { setKeyField(s, "ed-test", "expires", 0) },
			wantErr: "bad manifest keys"},
		{name: "channel not in the manifest", mutate: func(s *testPackageSpec) {
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ed-test"}}}
			s.channel = 1
		},
			wantErr: "channel beta is not in the manifest"},
		{name: "canary channel not in the manifest", mutate: func(s *testPackageSpec) { s.channel = 2 },
			wantErr: "channel canary-public is not in the manifest"},
		{name: "unknown algorithm key is skipped", mutate: func(s *testPackageSpec) {
			keysList := s.manifestDoc["keys"].([]any)
			s.manifestDoc["keys"] = append(keysList, map[string]any{"id": "pq-test", "alg": "ML-DSA-65"})
		}},
		{name: "unknown algorithm key cannot satisfy a group", mutate: func(s *testPackageSpec) {
			keysList := s.manifestDoc["keys"].([]any)
			s.manifestDoc["keys"] = append(keysList, map[string]any{"id": "pq-test", "alg": "ML-DSA-65"})
			s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"pq-test"}}}
			s.signers = []testSigner{{id: "pq-test", ed: keys.ed.ed}}
		}, wantErr: "no valid signature"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyDesktopPackage(buildTestPackage(t, tt.mutate), keys.rootPub, testNow)
			assertVerifyError(t, err, tt.wantErr)
		})
	}
}

func assertVerifyError(t *testing.T, err error, want string) {
	t.Helper()
	if want == "" {
		if err != nil {
			t.Fatalf("VerifyDesktopPackage() error = %v, want nil", err)
		}
		return
	}
	if err == nil {
		t.Fatalf("VerifyDesktopPackage() error = nil, want %q", want)
	}
	if !strings.Contains(err.Error(), want) {
		t.Fatalf("VerifyDesktopPackage() error = %q, want it to contain %q", err, want)
	}
}

func TestVerifyDesktopPackageManifestRootSignature(t *testing.T) {
	keys := testKeys(t)
	otherRoot, _, err := ed25519.GenerateKey(rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	t.Run("other root key", func(t *testing.T) {
		_, err := VerifyDesktopPackage(buildTestPackage(t, nil), otherRoot, testNow)
		assertVerifyError(t, err, "bad manifest root signature")
	})
	t.Run("missing root key", func(t *testing.T) {
		_, err := VerifyDesktopPackage(buildTestPackage(t, nil), nil, testNow)
		assertVerifyError(t, err, "update root public key is required")
	})
	t.Run("manifest tampered after root signing", func(t *testing.T) {
		data := buildTestPackage(t, func(s *testPackageSpec) {
			signed, err := json.Marshal(s.manifestDoc)
			if err != nil {
				t.Fatal(err)
			}
			s.manifestSig = ed25519.Sign(keys.root, signed)
			// 把 ed-second 加进 stable 组：包签名照常覆盖篡改后的清单，只有根签名能发现。
			s.manifest = bytes.Replace(signed, []byte(`[["ed-test"]]`), []byte(`[["ed-second"]]`), 1)
			if bytes.Equal(s.manifest, signed) {
				t.Fatal("tamper did not apply")
			}
			s.signers = []testSigner{keys.ed2}
		})
		_, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
		assertVerifyError(t, err, "bad manifest root signature")
	})
	t.Run("short root signature", func(t *testing.T) {
		data := buildTestPackage(t, func(s *testPackageSpec) { s.manifestSig = make([]byte, 63) })
		_, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
		assertVerifyError(t, err, "bad manifest root signature")
	})
	t.Run("manifest signed by a package key", func(t *testing.T) {
		data := buildTestPackage(t, func(s *testPackageSpec) {
			raw, _ := json.Marshal(s.manifestDoc)
			s.manifestSig = ed25519.Sign(keys.ed.ed, raw)
		})
		_, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
		assertVerifyError(t, err, "bad manifest root signature")
	})
}

// 签名区（魔数到清单末尾）、签名本身或负载中任何一个字节被改动，签名都不再成立。
func TestVerifyDesktopPackageRejectsTampering(t *testing.T) {
	keys := testKeys(t)
	payload := []byte("compressed-update-payload")
	original := buildTestPackage(t, func(s *testPackageSpec) { s.payload = payload })
	if _, err := VerifyDesktopPackage(original, keys.rootPub, testNow); err != nil {
		t.Fatal(err)
	}
	payloadStart := len(original) - len(payload)
	signatureStart := payloadStart - 4 - 64
	tests := []struct {
		name   string
		offset int
	}{
		{name: "version", offset: 11},
		{name: "created", offset: 19},
		{name: "manifest byte", offset: packageHeaderSize + 4 + 10},
		{name: "signature byte", offset: signatureStart + 5},
		{name: "first payload byte", offset: payloadStart},
		{name: "last payload byte", offset: len(original) - 1},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			data := bytes.Clone(original)
			data[tt.offset] ^= 0x01
			_, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
			if err == nil {
				t.Fatal("tampered package accepted")
			}
		})
	}
	// 改动签名区里的清单字节会先被根签名拦下；头部与负载改动由包签名拦下。
	data := bytes.Clone(original)
	data[11] ^= 0x01
	_, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
	assertVerifyError(t, err, "no valid signature")
	data = bytes.Clone(original)
	data[len(data)-1] ^= 0x01
	_, err = VerifyDesktopPackage(data, keys.rootPub, testNow)
	assertVerifyError(t, err, "no valid signature")
}

func TestVerifyDesktopPackageEnvelopeStructure(t *testing.T) {
	keys := testKeys(t)
	tests := []struct {
		name    string
		mutate  func(*testPackageSpec)
		wantErr string
	}{
		{name: "bad magic", mutate: func(s *testPackageSpec) { s.magic = "TDUQ" }, wantErr: "bad magic"},
		{name: "v1 package", mutate: func(s *testPackageSpec) { s.magic = "TDU1" }, wantErr: "bad magic"},
		{name: "format 1", mutate: func(s *testPackageSpec) { s.format = 1 }, wantErr: "bad format"},
		{name: "format 3", mutate: func(s *testPackageSpec) { s.format = 3 }, wantErr: "bad format"},
		{name: "unknown channel", mutate: func(s *testPackageSpec) { s.channel = 4 }, wantErr: "bad channel"},
		{name: "unknown os", mutate: func(s *testPackageSpec) { s.os = 3 }, wantErr: "bad os"},
		{name: "unknown arch", mutate: func(s *testPackageSpec) { s.arch = 3 }, wantErr: "bad arch"},
		{name: "trailing byte", mutate: func(s *testPackageSpec) { s.trailer = []byte{0} }, wantErr: "1 trailing bytes"},
		{name: "empty payload", mutate: func(s *testPackageSpec) { s.payload = nil }, wantErr: "bad payload size"},
		{name: "empty manifest signature", mutate: func(s *testPackageSpec) { s.manifestSig = []byte{} },
			wantErr: "bad manifest signature size"},
		{name: "manifest signature over 256 bytes", mutate: func(s *testPackageSpec) { s.manifestSig = make([]byte, 257) },
			wantErr: "bad manifest signature size"},
		{name: "manifest signature of 256 bytes is parsed", mutate: func(s *testPackageSpec) { s.manifestSig = make([]byte, 256) },
			wantErr: "bad manifest root signature"},
		{name: "16 signatures", mutate: func(s *testPackageSpec) {
			for i := 0; i < 15; i++ {
				s.extraSignatures = append(s.extraSignatures, testEnvelopeSignature{keyID: "x", sig: make([]byte, 64)})
			}
		}},
		{name: "17 signatures", mutate: func(s *testPackageSpec) {
			for i := 0; i < 16; i++ {
				s.extraSignatures = append(s.extraSignatures, testEnvelopeSignature{keyID: "x", sig: make([]byte, 64)})
			}
		}, wantErr: "bad signature count"},
		{name: "key id of 64 bytes", mutate: func(s *testPackageSpec) {
			s.extraSignatures = []testEnvelopeSignature{{keyID: strings.Repeat("k", 64), sig: make([]byte, 64)}}
		}},
		{name: "key id over 64 bytes", mutate: func(s *testPackageSpec) {
			s.extraSignatures = []testEnvelopeSignature{{keyID: strings.Repeat("k", 65), sig: make([]byte, 64)}}
		}, wantErr: "bad key id size"},
		{name: "empty key id", mutate: func(s *testPackageSpec) {
			s.extraSignatures = []testEnvelopeSignature{{keyID: "", sig: make([]byte, 64)}}
		}, wantErr: "bad key id size"},
		{name: "signature of 512 bytes", mutate: func(s *testPackageSpec) {
			s.extraSignatures = []testEnvelopeSignature{{keyID: "x", sig: make([]byte, 512)}}
		}},
		{name: "signature over 512 bytes", mutate: func(s *testPackageSpec) {
			s.extraSignatures = []testEnvelopeSignature{{keyID: "x", sig: make([]byte, 513)}}
		}, wantErr: "bad signature size"},
		{name: "empty signature", mutate: func(s *testPackageSpec) {
			s.extraSignatures = []testEnvelopeSignature{{keyID: "x", sig: []byte{}}}
		}, wantErr: "bad signature size"},
		{name: "empty manifest", mutate: func(s *testPackageSpec) { s.manifest = []byte{} }, wantErr: "bad manifest size"},
		{name: "manifest of 256 KiB", mutate: func(s *testPackageSpec) {
			raw, _ := json.Marshal(s.manifestDoc)
			s.manifest = append(raw, bytes.Repeat([]byte(" "), 256*1024-len(raw))...)
		}},
		{name: "manifest over 256 KiB", mutate: func(s *testPackageSpec) {
			raw, _ := json.Marshal(s.manifestDoc)
			s.manifest = append(raw, bytes.Repeat([]byte(" "), 256*1024+1-len(raw))...)
		}, wantErr: "bad manifest size"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyDesktopPackage(buildTestPackage(t, tt.mutate), keys.rootPub, testNow)
			assertVerifyError(t, err, tt.wantErr)
		})
	}
}

// 负载长度字段超过 256 MiB 时在读取负载前就被拒绝，测试不需要真的分配 256 MiB。
func TestVerifyDesktopPackageRejectsOversizedPayloadLength(t *testing.T) {
	keys := testKeys(t)
	for _, tt := range []struct {
		length  uint32
		wantErr string
	}{
		{length: 256*1024*1024 + 1, wantErr: "bad payload size"},
		{length: 256 * 1024 * 1024, wantErr: "truncated payload"},
	} {
		data := buildTestPackage(t, func(s *testPackageSpec) { s.payload = []byte("x") })
		data = data[:len(data)-1]
		binary.LittleEndian.PutUint32(data[len(data)-4:], tt.length)
		_, err := VerifyDesktopPackage(data, keys.rootPub, testNow)
		assertVerifyError(t, err, tt.wantErr)
	}
}

// 任何截断都必须被拒绝，而不是越界读取或把残缺的包当成合法包。
func TestVerifyDesktopPackageRejectsEveryTruncation(t *testing.T) {
	keys := testKeys(t)
	data := buildTestPackage(t, func(s *testPackageSpec) {
		s.manifestDoc["channels"] = map[string]any{"stable": []any{[]any{"ed-test"}, []any{"es-test"}}}
		s.signers = []testSigner{keys.ed, keys.es}
	})
	if _, err := VerifyDesktopPackage(data, keys.rootPub, testNow); err != nil {
		t.Fatal(err)
	}
	for n := 0; n < len(data); n++ {
		if _, err := VerifyDesktopPackage(data[:n], keys.rootPub, testNow); err == nil {
			t.Fatalf("package truncated to %d of %d bytes accepted", n, len(data))
		}
	}
}

func TestVerifyDesktopPackageManifestContent(t *testing.T) {
	keys := testKeys(t)
	manyKeys := func(n int) func(*testPackageSpec) {
		return func(s *testPackageSpec) {
			list := []any{keys.ed.manifestKey()}
			for i := 1; i < n; i++ {
				key := keys.ed2.manifestKey()
				key["id"] = fmt.Sprintf("extra-%d", i)
				list = append(list, key)
			}
			s.manifestDoc["keys"] = list
		}
	}
	manyChannels := func(n int) func(*testPackageSpec) {
		return func(s *testPackageSpec) {
			channels := map[string]any{"stable": []any{[]any{"ed-test"}}}
			for i := 1; i < n; i++ {
				channels[fmt.Sprintf("extra-%d", i)] = []any{[]any{"ed-test"}}
			}
			s.manifestDoc["channels"] = channels
		}
	}
	manyGroups := func(n int) func(*testPackageSpec) {
		groups := make([]any, n)
		for i := range groups {
			groups[i] = []any{"ed-test"}
		}
		return manifestChannels(map[string]any{"stable": groups})
	}
	groupOf := func(n int) func(*testPackageSpec) {
		return manifestChannels(map[string]any{"stable": []any{append([]any{"ed-test"}, testIDs("other", n-1)...)}})
	}
	revokedOf := func(n int) func(*testPackageSpec) {
		return func(s *testPackageSpec) { s.manifestDoc["revoked"] = testIDs("old", n) }
	}
	setField := func(field string, value any) func(*testPackageSpec) {
		return func(s *testPackageSpec) {
			if value == nil {
				delete(s.manifestDoc, field)
			} else {
				s.manifestDoc[field] = value
			}
		}
	}
	tests := []struct {
		name    string
		mutate  func(*testPackageSpec)
		wantErr string
	}{
		{name: "not JSON", mutate: func(s *testPackageSpec) { s.manifest = []byte("{not json") },
			wantErr: "could not parse manifest JSON"},
		{name: "JSON array", mutate: func(s *testPackageSpec) { s.manifest = []byte("[]") },
			wantErr: "could not parse manifest JSON"},
		// Qt 的 JSON 解析器拒绝非法 UTF-8，Go 会悄悄替换成 U+FFFD，这里必须与客户端一致。
		{name: "invalid UTF-8 in a string", mutate: func(s *testPackageSpec) {
			raw, _ := json.Marshal(s.manifestDoc)
			s.manifest = bytes.Replace(raw, []byte(`"revoked":[]`), []byte("\"revoked\":[\"old-\xff\"]"), 1)
		}, wantErr: "could not parse manifest JSON"},
		{name: "format 2", mutate: setField("format", 2), wantErr: "bad manifest format"},
		{name: "format as string", mutate: setField("format", "1"), wantErr: "bad manifest format"},
		{name: "format missing", mutate: setField("format", nil), wantErr: "bad manifest format"},
		{name: "manifest_version 0", mutate: setField("manifest_version", 0), wantErr: "bad manifest_version"},
		{name: "manifest_version 2^32", mutate: setField("manifest_version", int64(1)<<32), wantErr: "bad manifest_version"},
		{name: "manifest_version 2^32-1", mutate: setField("manifest_version", int64(1)<<32-1)},
		{name: "manifest_version fractional", mutate: setField("manifest_version", 1.5), wantErr: "bad manifest_version"},
		{name: "keys missing", mutate: setField("keys", nil), wantErr: "bad manifest keys"},
		{name: "channels missing", mutate: setField("channels", nil), wantErr: "bad manifest channels"},
		{name: "revoked missing", mutate: setField("revoked", nil), wantErr: "bad manifest revoked list"},
		{name: "64 keys", mutate: manyKeys(64)},
		{name: "65 keys", mutate: manyKeys(65), wantErr: "bad manifest keys"},
		{name: "duplicate key id", mutate: func(s *testPackageSpec) {
			s.manifestDoc["keys"] = []any{keys.ed.manifestKey(), keys.ed.manifestKey()}
		}, wantErr: "bad manifest keys"},
		{name: "key id over 64 bytes", mutate: func(s *testPackageSpec) {
			key := keys.ed2.manifestKey()
			key["id"] = strings.Repeat("k", 65)
			s.manifestDoc["keys"] = append(s.manifestDoc["keys"].([]any), key)
		}, wantErr: "bad manifest keys"},
		{name: "ed25519 x wrong size", mutate: func(s *testPackageSpec) { setKeyField(s, "ed-test", "x", "AAAA") },
			wantErr: "bad manifest keys"},
		{name: "ed25519 x standard base64", mutate: func(s *testPackageSpec) {
			setKeyField(s, "ed-test", "x", strings.Repeat("+", 43))
		}, wantErr: "bad manifest keys"},
		{name: "ed25519 x padded base64url", mutate: func(s *testPackageSpec) {
			setKeyField(s, "ed-test", "x", keys.ed.manifestKey()["x"].(string)+"=")
		}},
		{name: "es256 wrong curve", mutate: func(s *testPackageSpec) { setKeyField(s, "es-test", "crv", "P-384") },
			wantErr: "bad manifest keys"},
		{name: "es256 missing y", mutate: func(s *testPackageSpec) { setKeyField(s, "es-test", "y", nil) },
			wantErr: "bad manifest keys"},
		{name: "16 channels", mutate: manyChannels(16)},
		{name: "17 channels", mutate: manyChannels(17), wantErr: "bad manifest channels"},
		{name: "8 groups", mutate: manyGroups(8)},
		{name: "9 groups", mutate: manyGroups(9), wantErr: "bad manifest channels"},
		{name: "empty group list", mutate: manifestChannels(map[string]any{"stable": []any{}}),
			wantErr: "bad manifest channels"},
		{name: "16 keys in a group", mutate: groupOf(16)},
		{name: "17 keys in a group", mutate: groupOf(17), wantErr: "bad manifest channels"},
		{name: "empty group", mutate: manifestChannels(map[string]any{"stable": []any{[]any{}}}),
			wantErr: "bad manifest channels"},
		{name: "group id over 64 bytes", mutate: manifestChannels(map[string]any{
			"stable": []any{[]any{"ed-test", strings.Repeat("k", 65)}},
		}), wantErr: "bad manifest channels"},
		{name: "64 revoked", mutate: revokedOf(64)},
		{name: "65 revoked", mutate: revokedOf(65), wantErr: "bad manifest revoked list"},
		{name: "empty revoked id", mutate: setField("revoked", []any{""}), wantErr: "bad manifest revoked list"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := VerifyDesktopPackage(buildTestPackage(t, tt.mutate), keys.rootPub, testNow)
			assertVerifyError(t, err, tt.wantErr)
		})
	}
}

// 流式验证与内存验证结论一致，并给出整个文件的 SHA256；读到的字节数与声明的大小
// 不符（文件在读取时被改动）时拒绝。
func TestVerifyDesktopPackageStream(t *testing.T) {
	keys := testKeys(t)
	data := buildTestPackage(t, nil)
	pkg, fileHash, err := VerifyDesktopPackageStream(bytes.NewReader(data), int64(len(data)), keys.rootPub, testNow)
	if err != nil {
		t.Fatal(err)
	}
	if fileHash != sha256.Sum256(data) || pkg.Base() != 7000007 || pkg.Target.String() != "win-x64" {
		t.Fatalf("package = %+v hash = %x", pkg, fileHash)
	}
	tampered := bytes.Clone(data)
	tampered[len(tampered)-1] ^= 1
	_, _, err = VerifyDesktopPackageStream(bytes.NewReader(tampered), int64(len(tampered)), keys.rootPub, testNow)
	assertVerifyError(t, err, "no valid signature")

	_, _, err = VerifyDesktopPackageStream(bytes.NewReader(append(bytes.Clone(data), 0)), int64(len(data)), keys.rootPub, testNow)
	assertVerifyError(t, err, "more bytes than its size")
	_, _, err = VerifyDesktopPackageStream(bytes.NewReader(data[:len(data)-1]), int64(len(data)), keys.rootPub, testNow)
	assertVerifyError(t, err, "fewer bytes than its size")
	_, _, err = VerifyDesktopPackageStream(bytes.NewReader(nil), MaxDesktopPackageSize+1, keys.rootPub, testNow)
	assertVerifyError(t, err, "exceeds the v2 package limit")
}

func TestParseRootPublicKey(t *testing.T) {
	keys := testKeys(t)
	root, err := ParseRootPublicKey(keys.rootPEM)
	if err != nil {
		t.Fatal(err)
	}
	if !root.Equal(keys.rootPub) {
		t.Fatal("parsed root key differs")
	}
	withLeadingBlock := append(pemBlock("CERTIFICATE REQUEST", []byte("junk")), keys.rootPEM...)
	if root, err := ParseRootPublicKey(withLeadingBlock); err != nil || !root.Equal(keys.rootPub) {
		t.Fatalf("PEM with a leading block: %v", err)
	}

	es, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	for name, data := range map[string][]byte{
		"P-256 key": publicKeyPEM(&es.PublicKey),
		"not PEM":   []byte("not a key"),
		"empty":     nil,
	} {
		if _, err := ParseRootPublicKey(data); err == nil {
			t.Fatalf("%s accepted as root key", name)
		}
	}
}

func pemBlock(kind string, data []byte) []byte {
	return pem.EncodeToMemory(&pem.Block{Type: kind, Bytes: data})
}

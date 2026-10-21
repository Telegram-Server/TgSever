package push

import (
	"bytes"
	"crypto/aes"
	"crypto/sha1"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"strings"
	"testing"
)

// nseDecrypt 按 iOS NSE（Account.swift decryptedNotificationPayload）的解密步骤独立实现，
// 不调用被测的加密代码，也不复用 td 的 crypto 包：
// key_id = sha1(secret) 末 8 字节；x=8；AES-256-IGE；校验 msg_key 覆盖整段解密明文（含填充）。
func nseDecrypt(secret []byte, p string) ([]byte, []byte, error) {
	s := strings.ReplaceAll(strings.ReplaceAll(p, "-", "+"), "_", "/")
	for len(s)%4 != 0 {
		s += "="
	}
	data, err := base64.StdEncoding.DecodeString(s)
	if err != nil {
		return nil, nil, err
	}
	if len(data) < 24 {
		return nil, nil, errors.New("short")
	}
	digest := sha1.Sum(secret)
	if !bytes.Equal(data[:8], digest[len(digest)-8:]) {
		return nil, nil, errors.New("key id mismatch")
	}
	const x = 8
	msgKey := data[8:24]
	raw := data[24:]
	a := sha256.Sum256(append(append([]byte{}, msgKey...), secret[x:x+36]...))
	b := sha256.Sum256(append(append([]byte{}, secret[40+x:40+x+36]...), msgKey...))
	aesKey := append(append(append([]byte{}, a[0:8]...), b[8:24]...), a[24:32]...)
	aesIV := append(append(append([]byte{}, b[0:8]...), a[8:24]...), b[24:32]...)
	plain, err := igeDecrypt(aesKey, aesIV, raw)
	if err != nil {
		return nil, nil, err
	}
	if len(plain) <= 4 {
		return nil, nil, errors.New("short plaintext")
	}
	n := int(int32(binary.LittleEndian.Uint32(plain[:4])))
	if n < 0 || n > len(plain)-4 {
		return nil, nil, errors.New("bad length")
	}
	check := sha256.Sum256(append(append([]byte{}, secret[88+x:88+x+32]...), plain...))
	if !bytes.Equal(check[8:24], msgKey) {
		return nil, nil, errors.New("msg key mismatch")
	}
	return plain[4 : 4+n], plain, nil
}

// igeDecrypt 是教科书式 AES-IGE 解密：iv 前半是 x_prev（上一密文块）、后半是 y_prev（上一明文块）。
func igeDecrypt(key, iv, src []byte) ([]byte, error) {
	if len(src)%aes.BlockSize != 0 {
		return nil, errors.New("not block aligned")
	}
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	prevCipher := append([]byte{}, iv[:16]...)
	prevPlain := append([]byte{}, iv[16:32]...)
	out := make([]byte, len(src))
	buf := make([]byte, 16)
	for i := 0; i < len(src); i += 16 {
		c := src[i : i+16]
		for j := range 16 {
			buf[j] = c[j] ^ prevPlain[j]
		}
		block.Decrypt(buf, buf)
		for j := range 16 {
			out[i+j] = buf[j] ^ prevCipher[j]
		}
		prevCipher = c
		prevPlain = out[i : i+16]
	}
	return out, nil
}

func testSecret() []byte {
	secret := make([]byte, 256)
	for i := range secret {
		secret[i] = byte(i*7 + 3)
	}
	return secret
}

func TestEncryptPayloadDecryptsWithNSEAlgorithm(t *testing.T) {
	secret := testSecret()
	for _, body := range []string{`{}`, `{"from_id":"42","msg_id":"77"}`, strings.Repeat("x", 333)} {
		p, err := encryptPayload(secret, []byte(body))
		if err != nil {
			t.Fatal(err)
		}
		if strings.ContainsAny(p, "+/=") {
			t.Fatalf("p = %q, want unpadded base64url", p)
		}
		raw, _ := base64.RawURLEncoding.DecodeString(p)
		digest := sha1.Sum(secret)
		if !bytes.Equal(raw[:8], digest[12:20]) {
			t.Fatalf("key_id = %x, want last 8 bytes of sha1(secret) %x", raw[:8], digest[12:20])
		}
		got, padded, err := nseDecrypt(secret, p)
		if err != nil {
			t.Fatalf("NSE decrypt: %v", err)
		}
		if string(got) != body {
			t.Fatalf("decrypted = %q, want %q", got, body)
		}
		if len(padded)%16 != 0 || len(padded) < 4+len(body) {
			t.Fatalf("plaintext len %d, want 16-byte aligned and >= %d", len(padded), 4+len(body))
		}
	}
}

func TestEncryptPayloadTamperingFailsNSEDecrypt(t *testing.T) {
	secret := testSecret()
	p, err := encryptPayload(secret, []byte(`{"from_id":"42","msg_id":"77"}`))
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.RawURLEncoding.DecodeString(p)
	raw[len(raw)-1] ^= 0x01
	if _, _, err := nseDecrypt(secret, base64.RawURLEncoding.EncodeToString(raw)); err == nil {
		t.Fatal("tampered ciphertext decrypted, want failure")
	}
	other := testSecret()
	other[0] ^= 0xff
	if _, _, err := nseDecrypt(other, p); err == nil {
		t.Fatal("decrypted with a different secret, want key id mismatch")
	}
}

func TestEncryptPayloadRejectsNon256ByteSecret(t *testing.T) {
	if _, err := encryptPayload(make([]byte, 32), []byte(`{}`)); err == nil {
		t.Fatal("32-byte secret accepted, want error")
	}
}

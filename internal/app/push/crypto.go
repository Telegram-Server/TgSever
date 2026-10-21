package push

import (
	"crypto/aes"
	"crypto/rand"
	"encoding/base64"
	"encoding/binary"
	"fmt"

	"github.com/gotd/ige"
	"github.com/iamxvbaba/td/crypto"
)

// SecretSize 是 iOS 登记时提交的推送密钥长度（keychain 里的 master-notification-secret）。
// 加密沿用 MTProto 2.0 的密钥推导，要求密钥恰好 256 字节。
const SecretSize = 256

// encryptPayload 按 iOS NSE 的约定加密解密后 JSON，返回 `p` 字段的值：
// base64url(key_id(8) || msg_key(16) || AES-256-IGE 密文)，不带填充。
//
// 明文 = int32le(len(data)) || data || 随机填充（对齐到 16 字节，至少 12 字节，同 MTProto）。
// key_id 取 sha1(secret) 末 8 字节（即 crypto.Key.ID）；msg_key 与 AES key/iv 按服务端方向（x=8）推导。
func encryptPayload(secret []byte, data []byte) (string, error) {
	if len(secret) != SecretSize {
		return "", fmt.Errorf("push secret must be %d bytes, got %d", SecretSize, len(secret))
	}
	var key crypto.Key
	copy(key[:], secret)

	plainLen := 4 + len(data)
	padding := 12 + (16-(plainLen+12)%16)%16
	plain := make([]byte, plainLen+padding)
	binary.LittleEndian.PutUint32(plain[:4], uint32(len(data)))
	copy(plain[4:], data)
	if _, err := rand.Read(plain[plainLen:]); err != nil {
		return "", err
	}

	msgKey := crypto.MessageKey(key, plain, crypto.Server)
	aesKey, aesIV := crypto.Keys(key, msgKey, crypto.Server)
	block, err := aes.NewCipher(aesKey[:])
	if err != nil {
		return "", err
	}
	id := key.ID()
	out := make([]byte, 0, 8+16+len(plain))
	out = append(out, id[:]...)
	out = append(out, msgKey[:]...)
	cipherText := make([]byte, len(plain))
	ige.EncryptBlocks(block, aesIV[:], cipherText, plain)
	out = append(out, cipherText...)
	return base64.RawURLEncoding.EncodeToString(out), nil
}

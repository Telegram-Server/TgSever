// Package srp 是服务端的 SRP-2048(Telegram 2FA 用的
// passwordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow)实现。
//
// 包内只做数学与参数校验,不依赖 domain 类型,也不负责持久化。调用方是两步验证
// (internal/app/account)和登录密码(internal/app/loginpassword)。调用方各自负责
// 把存储结构转换成 Challenge,并把这里的哨兵错误映射成各自的业务错误。
//
// g、p 固定为服务端默认参数:ValidateNewPassword 只接受与默认参数一致的 new_algo。
// salt1、salt2 随密码存档,Challenge 两者都携带,Verify 用存档里的值计算 M1,
// 与下发给客户端的 current_algo 保持一致。
//
// 安全范围(#32 加固):
//   - 群:g 生成 p 的二次剩余子群,阶 q = (p-1)/2 为素数。
//   - 服务端私密值 b:只看 b mod q 才有意义(g^b 只取决于它)。b mod q 为 0 时
//     S 退化为 ±1,任何人都能伪造 M1;b mod q 接近 0 或 q 时 g^b 可被枚举,B 会泄露
//     k*v。因此要求 2^1984 <= b mod q <= q - 2^1984,NewChallenge 直接在 [2^1984, q - 2^1984]
//     里均匀取值。
//   - 客户端公开值 A:按 Telegram 对 DH 的建议(https://core.telegram.org/mtproto/auth_key),
//     要求 2^1984 < A < p - 2^1984;另外拒绝 u == 0。
//
// 协议见 https://core.telegram.org/api/srp。
package srp

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"crypto/subtle"
	"encoding/hex"
	"errors"
	"math/big"
)

// HashSize 是 SRP 中所有大数(p、g、v、A、B、b)的定长字节数(2048 bit)。
const HashSize = 256

// newSalt1Suffix 是客户端设置新密码时追加到 salt1 后的随机字节数。
const newSalt1Suffix = 32

// safetyMarginBits 是 A 与 b mod q 离区间端点的最小距离(2^(2048-64))。
const safetyMarginBits = 2048 - 64

var (
	// ErrInvalidVerifier:verifier(new_password_hash)不在 (0, p) 内,或超过 HashSize 字节。
	ErrInvalidVerifier = errors.New("srp: verifier out of range")
	// ErrIncompleteChallenge:Challenge 缺少 verifier、私密值 b、B 或 salt2,或 b 不在安全范围内。
	ErrIncompleteChallenge = errors.New("srp: incomplete challenge")
	// ErrInvalidProof:客户端的 (A, M1) 不成立——A 越界、u 为零或 M1 不匹配。
	ErrInvalidProof = errors.New("srp: invalid proof")
	// ErrNewSettingsMissing:新密码缺少 new_algo 或 new_password_hash。
	ErrNewSettingsMissing = errors.New("srp: new algo or password hash missing")
	// ErrNewAlgoInvalid:new_algo 与服务端下发的参数不符(g、p、salt2 或 salt1 前缀)。
	ErrNewAlgoInvalid = errors.New("srp: new algo does not match server parameters")
	// ErrValueTooLarge:Pad 的输入超过 HashSize 字节。
	ErrValueTooLarge = errors.New("srp: value longer than HashSize")
)

var (
	baseSalt1 = []byte{0xEC, 0xF8, 0x73, 0x76, 0x65, 0xBC, 0x77, 0x5A}
	baseSalt2 = []byte{0xBE, 0xDE, 0x48, 0x88, 0x8C, 0x0F, 0x42, 0xAC, 0x34, 0xFF, 0xD1, 0xD4, 0x93, 0x5D, 0x8B, 0x21}
	// baseP 与 internal/app/phone/dh.go 的 dhPrimeHex 同值(SRP 与通话 DH 共用官方参数);改动任一处须同步另一处。
	baseP = mustDecodeHex("c71caeb9c6b1c9048e6c522f70f13f73980d40238e3e21c14934d037563d930f48198a0aa7c14058229493d22530f4dbfa336f6e0ac925139543aed44cce7c3720fd51f69458705ac68cd4fe6b6b13abdc9746512969328454f18faf8c595f642477fe96bb2a941d5bcd1d4ac8cc49880708fa9b378e3c4f3a9060bee67cf9a4a4a695811051907e162753b56b0f6b410dba74d8a84b2a14b3144e0ef1284754fd17ed950d5965b4b9dd46582db1178d169c6bc465b0d6ff9ca3928fef5b9ae4e418fc15e83ebea0f87fa9ff5eed70050ded2849f47bf959d956850ce929851f0d8115f635b105ee2e4e15d04b2454bf6f4fadf034b10403119cd8e3b92fcc5b")
	baseG = 3

	bigP = new(big.Int).SetBytes(baseP)
	bigG = big.NewInt(int64(baseG))
	// bigQ = (p-1)/2,g 生成的子群的阶。
	bigQ = new(big.Int).Rsh(new(big.Int).Sub(bigP, big.NewInt(1)), 1)
	// margin = 2^1984;A 必须落在 (margin, p - margin),b mod q 必须落在 [margin, q - margin]。
	margin  = new(big.Int).Lsh(big.NewInt(1), safetyMarginBits)
	aMax    = new(big.Int).Sub(bigP, margin)
	secMax  = new(big.Int).Sub(bigQ, margin)
	secSpan = new(big.Int).Add(new(big.Int).Sub(secMax, margin), big.NewInt(1))
	// k = H(p | g)
	bigK = new(big.Int).SetBytes(hashBytes(mustPad(baseP), mustPad(bigG.Bytes())))
	// H(p) xor H(g),M1 的第一段,与 salt 无关,预先算好。
	hpXorHg = xorBytes(hashBytes(mustPad(baseP)), hashBytes(mustPad(bigG.Bytes())))
)

// Algo 是 KDF 参数,对应 TL 的
// passwordKdfAlgoSHA256SHA256PBKDF2HMACSHA512iter100000SHA256ModPow。
type Algo struct {
	Salt1 []byte
	Salt2 []byte
	G     int
	P     []byte
}

// DefaultAlgo 返回服务端下发给客户端的默认参数(每次返回独立副本)。
func DefaultAlgo() Algo {
	return Algo{
		Salt1: append([]byte(nil), baseSalt1...),
		Salt2: append([]byte(nil), baseSalt2...),
		G:     baseG,
		P:     append([]byte(nil), baseP...),
	}
}

// Challenge 是一次 SRP 校验所需的服务端存档:verifier v、服务端私密值 b、
// 下发给客户端的 B = k*v + g^b,以及客户端计算 x 时用的 salt1、salt2
// (即设置该密码时的 new_algo.salt1 / salt2,也就是下发的 current_algo)。
type Challenge struct {
	Verifier []byte
	Secret   []byte
	B        []byte
	Salt1    []byte
	Salt2    []byte
}

// NewChallenge 为 verifier 生成新的随机私密值 b 和对应的 B。b 在 [2^1984, q - 2^1984]
// 内均匀取值,永远满足 Verify 对 b 的安全范围要求。
func NewChallenge(verifier []byte) (secret, b []byte, err error) {
	if !isGoodLarge(new(big.Int).SetBytes(verifier)) {
		return nil, nil, ErrInvalidVerifier
	}
	n, err := rand.Int(rand.Reader, secSpan)
	if err != nil {
		return nil, nil, err
	}
	n.Add(n, margin)
	secret = mustPad(n.Bytes())
	b, err = ComputeB(verifier, secret)
	if err != nil {
		return nil, nil, err
	}
	return secret, b, nil
}

// ComputeB 用 verifier 和服务端私密值 b 计算 B = (k*v + g^b) mod p,按 HashSize 定长返回。
// b 不在安全范围内返回 ErrIncompleteChallenge。
func ComputeB(verifier, secret []byte) ([]byte, error) {
	v := new(big.Int).SetBytes(verifier)
	if !isGoodLarge(v) {
		return nil, ErrInvalidVerifier
	}
	b := new(big.Int).SetBytes(secret)
	if !isSafeSecret(b) {
		return nil, ErrIncompleteChallenge
	}
	kv := new(big.Int).Mul(bigK, v)
	kv.Mod(kv, bigP)
	gb := new(big.Int).Exp(bigG, b, bigP)
	out := new(big.Int).Add(kv, gb)
	out.Mod(out, bigP)
	return mustPad(out.Bytes()), nil
}

// Verify 校验客户端针对 ch 发来的 (A, M1)。M1 用常数时间比较。
//
// 返回 nil 表示密码正确;ErrIncompleteChallenge 表示存档不完整或损坏(含 b 不在安全
// 范围内、缺 salt2);ErrInvalidVerifier 表示存档的 verifier 越界;其余失败均为 ErrInvalidProof。
func Verify(ch Challenge, aBytes, m1 []byte) error {
	if len(ch.Verifier) == 0 || len(ch.Secret) == 0 || len(ch.B) == 0 || len(ch.Salt2) == 0 {
		return ErrIncompleteChallenge
	}
	aForHash, err := Pad(aBytes)
	if err != nil {
		return ErrInvalidProof
	}
	a := new(big.Int).SetBytes(aBytes)
	if a.Cmp(margin) <= 0 || a.Cmp(aMax) >= 0 {
		return ErrInvalidProof
	}
	v := new(big.Int).SetBytes(ch.Verifier)
	if !isGoodLarge(v) {
		return ErrInvalidVerifier
	}
	b := new(big.Int).SetBytes(ch.Secret)
	if !isSafeSecret(b) {
		// b mod q 退化时 S 可预测(b ≡ 0 时恒为 ±1),任何人都能伪造 M1;按存档损坏处理。
		return ErrIncompleteChallenge
	}
	bForHash, err := Pad(ch.B)
	if err != nil {
		return ErrIncompleteChallenge
	}
	// u = H(A | B)
	u := new(big.Int).SetBytes(hashBytes(aForHash, bForHash))
	if u.Sign() <= 0 {
		return ErrInvalidProof
	}
	// S = (A * v^u)^b mod p,K = H(S)
	vu := new(big.Int).Exp(v, u, bigP)
	sBase := new(big.Int).Mul(a, vu)
	sBase.Mod(sBase, bigP)
	s := new(big.Int).Exp(sBase, b, bigP)
	k := hashBytes(mustPad(s.Bytes()))
	// M1 = H(H(p) xor H(g) | H(salt1) | H(salt2) | A | B | K)
	want := hashBytes(
		hpXorHg,
		hashBytes(ch.Salt1),
		hashBytes(ch.Salt2),
		aForHash,
		bForHash,
		k,
	)
	if subtle.ConstantTimeCompare(want, m1) != 1 {
		return ErrInvalidProof
	}
	return nil
}

// ValidateNewPassword 校验客户端设置新密码时提交的 new_algo 与 new_password_hash:
// g、p、salt2 必须与服务端参数一致,salt1 必须是默认 salt1 追加 32 字节,
// verifier 必须不超过 HashSize 字节且在 (0, p) 内。algo 为 nil 表示客户端没有提交 new_algo。
// 通过校验的 verifier 交给 Pad 一定成功。
func ValidateNewPassword(algo *Algo, verifier []byte) error {
	if algo == nil || len(verifier) == 0 {
		return ErrNewSettingsMissing
	}
	if algo.G != baseG || !bytes.Equal(algo.P, baseP) || !bytes.Equal(algo.Salt2, baseSalt2) {
		return ErrNewAlgoInvalid
	}
	if len(algo.Salt1) != len(baseSalt1)+newSalt1Suffix || !bytes.Equal(algo.Salt1[:len(baseSalt1)], baseSalt1) {
		return ErrNewAlgoInvalid
	}
	if len(verifier) > HashSize || !isGoodLarge(new(big.Int).SetBytes(verifier)) {
		return ErrInvalidVerifier
	}
	return nil
}

// Pad 把大端字节串左补零到 HashSize,用于存储 verifier 等定长值。
// 输入超过 HashSize 字节返回 ErrValueTooLarge(从不截断)。
func Pad(in []byte) ([]byte, error) {
	if len(in) > HashSize {
		return nil, ErrValueTooLarge
	}
	out := make([]byte, HashSize)
	copy(out[HashSize-len(in):], in)
	return out, nil
}

// mustPad 用于包内已知不超过 HashSize 的值(mod p 的结果、p、g)。
func mustPad(in []byte) []byte {
	out, err := Pad(in)
	if err != nil {
		panic(err)
	}
	return out
}

func hashBytes(parts ...[]byte) []byte {
	h := sha256.New()
	for _, part := range parts {
		_, _ = h.Write(part)
	}
	return h.Sum(nil)
}

func xorBytes(a, b []byte) []byte {
	out := make([]byte, len(a))
	for i := range a {
		out[i] = a[i] ^ b[i]
	}
	return out
}

// isGoodLarge 判断 0 < n < p。
func isGoodLarge(n *big.Int) bool {
	return n.Sign() > 0 && n.Cmp(bigP) < 0
}

// isSafeSecret 判断服务端私密值 b 是否在安全范围内:2^1984 <= b mod q <= q - 2^1984。
func isSafeSecret(b *big.Int) bool {
	if b.Sign() <= 0 {
		return false
	}
	e := new(big.Int).Mod(b, bigQ)
	return e.Cmp(margin) >= 0 && e.Cmp(secMax) <= 0
}

func mustDecodeHex(s string) []byte {
	out, err := hex.DecodeString(s)
	if err != nil {
		panic(err)
	}
	return out
}

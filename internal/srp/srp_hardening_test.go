package srp_test

import (
	"bytes"
	"crypto/sha256"
	"errors"
	"math/big"
	"testing"

	tdsrp "github.com/iamxvbaba/td/crypto/srp"

	"telesrv/internal/srp"
)

// 本文件是 #32 对共享 SRP 包的加固测试：登录密码第一次在未登录状态下调用 Verify，
// 服务端私密值 b、客户端公开值 A、定长填充和 salt2 都要拒绝退化或越界的输入。

// groupParams 返回测试里自己算的 p、q = (p-1)/2、g 和 k = H(p | g)，不依赖包内实现。
func groupParams() (p, q, g, k *big.Int) {
	algo := srp.DefaultAlgo()
	p = new(big.Int).SetBytes(algo.P)
	q = new(big.Int).Rsh(new(big.Int).Sub(p, big.NewInt(1)), 1)
	g = big.NewInt(int64(algo.G))
	h := sha256.New()
	h.Write(pad256(p.Bytes()))
	h.Write(pad256(g.Bytes()))
	k = new(big.Int).SetBytes(h.Sum(nil))
	return p, q, g, k
}

func pad256(in []byte) []byte {
	out := make([]byte, srp.HashSize)
	copy(out[srp.HashSize-len(in):], in)
	return out
}

func sha(parts ...[]byte) []byte {
	h := sha256.New()
	for _, part := range parts {
		h.Write(part)
	}
	return h.Sum(nil)
}

// rawB 不经 ComputeB 直接按定义算 B = (k*v + g^b) mod p，用来构造 ComputeB 会拒绝的存档。
func rawB(verifier []byte, secret *big.Int) []byte {
	p, _, g, k := groupParams()
	v := new(big.Int).SetBytes(verifier)
	out := new(big.Int).Mul(k, v)
	out.Add(out, new(big.Int).Exp(g, secret, p))
	out.Mod(out, p)
	return pad256(out.Bytes())
}

// forgedProof 在不知道密码的情况下，假定 S = 1 算出 M1。b 是群阶 q 的倍数时，
// 对子群里的 A 和 v（诚实客户端的 A、v 都在子群里）S 恒为 1，这个 M1 就会被接受。
func forgedProof(algo srp.Algo, a, b []byte) []byte {
	p, _, g, _ := groupParams()
	hp := sha(pad256(p.Bytes()))
	hg := sha(pad256(g.Bytes()))
	xor := make([]byte, len(hp))
	for i := range hp {
		xor[i] = hp[i] ^ hg[i]
	}
	return sha(xor, sha(algo.Salt1), sha(algo.Salt2), pad256(a), pad256(b), sha(pad256([]byte{1})))
}

func TestVerifyRejectsSecretsThatCollapseTheSharedKey(t *testing.T) {
	password := []byte("degenerate secret")
	algo, verifier := clientNewPassword(t, password)
	verifier = mustPad(t, verifier)
	p, q, _, _ := groupParams()

	for name, secret := range map[string]*big.Int{
		// b = q：A、v 在子群里时 S = 1；b = p-1 = 2q：任何底数 S 都是 1（费马小定理）。
		"b = q":     q,
		"b = p - 1": new(big.Int).Sub(p, big.NewInt(1)),
	} {
		b := rawB(verifier, secret)
		ch := srp.Challenge{Verifier: verifier, Secret: pad256(secret.Bytes()), B: b, Salt1: algo.Salt1, Salt2: algo.Salt2}
		// 任意一个合法范围内的 A：攻击者不需要知道密码。
		a := clientProof(t, algo, []byte("not the password"), b).A
		if err := srp.Verify(ch, a, forgedProof(algo, a, b)); !errors.Is(err, srp.ErrIncompleteChallenge) {
			t.Fatalf("%s: Verify(forged M1 with S=1) = %v, want ErrIncompleteChallenge", name, err)
		}
		if _, err := srp.ComputeB(verifier, pad256(secret.Bytes())); !errors.Is(err, srp.ErrIncompleteChallenge) {
			t.Fatalf("%s: ComputeB = %v, want ErrIncompleteChallenge", name, err)
		}
	}

	// b ≡ ±1 (mod q)：g^b 只有两种可能，B 直接泄露 k*v，等于把 verifier 交给攻击者离线爆破。
	for name, secret := range map[string]*big.Int{
		"b = q + 1": new(big.Int).Add(q, big.NewInt(1)),
		"b = q - 1": new(big.Int).Sub(q, big.NewInt(1)),
		"b = 2":     big.NewInt(2),
	} {
		b := rawB(verifier, secret)
		ch := srp.Challenge{Verifier: verifier, Secret: pad256(secret.Bytes()), B: b, Salt1: algo.Salt1, Salt2: algo.Salt2}
		honest := clientProof(t, algo, password, b)
		if err := srp.Verify(ch, honest.A, honest.M1); !errors.Is(err, srp.ErrIncompleteChallenge) {
			t.Fatalf("%s: Verify(honest proof against a weak b) = %v, want ErrIncompleteChallenge", name, err)
		}
	}
}

func TestNewChallengeSecretStaysInTheSafeRange(t *testing.T) {
	_, q, _, _ := groupParams()
	low := new(big.Int).Lsh(big.NewInt(1), 2048-64)
	high := new(big.Int).Sub(q, low)
	_, verifier := clientNewPassword(t, []byte("range"))
	verifier = mustPad(t, verifier)
	for i := 0; i < 64; i++ {
		secret, _, err := srp.NewChallenge(verifier)
		if err != nil {
			t.Fatalf("NewChallenge: %v", err)
		}
		b := new(big.Int).SetBytes(secret)
		if b.Cmp(low) < 0 || b.Cmp(high) > 0 {
			t.Fatalf("NewChallenge secret %x outside [2^1984, q - 2^1984]", secret)
		}
	}
}

func TestVerifyRejectsClientValuesOutsideTheRecommendedRange(t *testing.T) {
	password := []byte("small A")
	algo, verifier := clientNewPassword(t, password)
	verifier = mustPad(t, verifier)
	secret, b, err := srp.NewChallenge(verifier)
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	ch := srp.Challenge{Verifier: verifier, Secret: secret, B: b, Salt1: algo.Salt1, Salt2: algo.Salt2}

	// 知道密码的客户端选了退化的 a：A = g^0 = 1、A = g^1 = 3。旧实现只要求 0 < A < p，
	// 这些证明都会被接受；按 Telegram 的建议，A 必须落在 (2^1984, p - 2^1984) 内。
	for name, random := range map[string][]byte{
		"a = 0 (A = 1)": {0},
		"a = 1 (A = g)": {1},
	} {
		answer, err := tdsrp.NewSRP(nil).Hash(password, b, random, tdInput(algo))
		if err != nil {
			t.Fatalf("%s: td Hash: %v", name, err)
		}
		if err := srp.Verify(ch, answer.A, answer.M1); !errors.Is(err, srp.ErrInvalidProof) {
			t.Fatalf("%s: Verify = %v, want ErrInvalidProof", name, err)
		}
	}

	p, _, _, _ := groupParams()
	low := new(big.Int).Lsh(big.NewInt(1), 2048-64)
	honest := clientProof(t, algo, password, b)
	for name, a := range map[string]*big.Int{
		"A = 2^1984":     low,
		"A = p - 2^1984": new(big.Int).Sub(p, low),
		"A = p - 1":      new(big.Int).Sub(p, big.NewInt(1)),
	} {
		if err := srp.Verify(ch, pad256(a.Bytes()), honest.M1); !errors.Is(err, srp.ErrInvalidProof) {
			t.Fatalf("%s: Verify = %v, want ErrInvalidProof", name, err)
		}
	}

	// A 超过 HashSize 字节（即使前导零让数值合法）不再被静默截断后接受。
	long := append([]byte{0}, honest.A...)
	if err := srp.Verify(ch, long, honest.M1); !errors.Is(err, srp.ErrInvalidProof) {
		t.Fatalf("Verify(A with 257 bytes) = %v, want ErrInvalidProof", err)
	}
	// 同一个 challenge 下正常客户端仍然通过。
	if err := srp.Verify(ch, honest.A, honest.M1); err != nil {
		t.Fatalf("Verify(honest) = %v, want nil", err)
	}
}

func TestPadRejectsOversizedInput(t *testing.T) {
	if _, err := srp.Pad(make([]byte, srp.HashSize+1)); !errors.Is(err, srp.ErrValueTooLarge) {
		t.Fatalf("Pad(257 bytes) = %v, want ErrValueTooLarge", err)
	}
	out, err := srp.Pad([]byte{1, 2})
	if err != nil || len(out) != srp.HashSize || out[srp.HashSize-1] != 2 || out[srp.HashSize-2] != 1 {
		t.Fatalf("Pad(2 bytes) = %x, %v", out, err)
	}
	full := bytes.Repeat([]byte{0xAB}, srp.HashSize)
	if out, err := srp.Pad(full); err != nil || !bytes.Equal(out, full) {
		t.Fatalf("Pad(256 bytes) = %v, want identity", err)
	}

	// 新密码的 verifier 超长时由 ValidateNewPassword 拒绝，调用方之后的 Pad 不会失败。
	algo, verifier := clientNewPassword(t, []byte("long hash"))
	if err := srp.ValidateNewPassword(&algo, append([]byte{0}, verifier...)); !errors.Is(err, srp.ErrInvalidVerifier) {
		t.Fatalf("ValidateNewPassword(257-byte hash) = %v, want ErrInvalidVerifier", err)
	}
}

func TestVerifyUsesTheChallengeSalt2(t *testing.T) {
	password := []byte("own salt2")
	algo := srp.DefaultAlgo()
	algo.Salt2 = bytes.Repeat([]byte{0x77}, 16)
	hash, newSalt1, err := tdsrp.NewSRP(bytes.NewReader(fixedSaltRandom)).NewHash(password, tdInput(algo))
	if err != nil {
		t.Fatalf("td NewHash: %v", err)
	}
	algo.Salt1 = newSalt1
	verifier := mustPad(t, hash)
	secret, b, err := srp.NewChallenge(verifier)
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	answer := clientProof(t, algo, password, b)
	ch := srp.Challenge{Verifier: verifier, Secret: secret, B: b, Salt1: algo.Salt1, Salt2: algo.Salt2}
	if err := srp.Verify(ch, answer.A, answer.M1); err != nil {
		t.Fatalf("Verify with the challenge's own salt2 = %v, want nil", err)
	}
	ch.Salt2 = srp.DefaultAlgo().Salt2
	if err := srp.Verify(ch, answer.A, answer.M1); !errors.Is(err, srp.ErrInvalidProof) {
		t.Fatalf("Verify with a different salt2 = %v, want ErrInvalidProof", err)
	}
	ch.Salt2 = nil
	if err := srp.Verify(ch, answer.A, answer.M1); !errors.Is(err, srp.ErrIncompleteChallenge) {
		t.Fatalf("Verify without salt2 = %v, want ErrIncompleteChallenge", err)
	}
}

func mustPad(t *testing.T, in []byte) []byte {
	t.Helper()
	out, err := srp.Pad(in)
	if err != nil {
		t.Fatalf("Pad: %v", err)
	}
	return out
}

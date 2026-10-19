package srp_test

import (
	"bytes"
	"errors"
	"testing"

	tdsrp "github.com/iamxvbaba/td/crypto/srp"

	"telesrv/internal/srp"
)

// 固定向量:客户端随机数全部取定值,保证测试可复现。
var (
	fixedSaltRandom = bytes.Repeat([]byte{0x5A}, 32)
	fixedClientA    = bytes.Repeat([]byte{0x23}, srp.HashSize)
)

func tdInput(algo srp.Algo) tdsrp.Input {
	return tdsrp.Input{Salt1: algo.Salt1, Salt2: algo.Salt2, G: algo.G, P: algo.P}
}

// clientNewPassword 用 td 客户端实现走一遍"设置新密码":基于服务端下发的默认算法
// 生成 new_algo(salt1 追加 32 字节随机)和 new_password_hash(verifier)。
func clientNewPassword(t *testing.T, password []byte) (srp.Algo, []byte) {
	t.Helper()
	algo := srp.DefaultAlgo()
	hash, newSalt1, err := tdsrp.NewSRP(bytes.NewReader(fixedSaltRandom)).NewHash(password, tdInput(algo))
	if err != nil {
		t.Fatalf("td NewHash: %v", err)
	}
	algo.Salt1 = newSalt1
	return algo, hash
}

// clientProof 用 td 客户端实现根据 challenge 的 B 算出 (A, M1)。
func clientProof(t *testing.T, algo srp.Algo, password, b []byte) tdsrp.Answer {
	t.Helper()
	answer, err := tdsrp.NewSRP(nil).Hash(password, b, fixedClientA, tdInput(algo))
	if err != nil {
		t.Fatalf("td Hash: %v", err)
	}
	return answer
}

func TestTDClientProofIsAcceptedByServer(t *testing.T) {
	password := []byte("correct horse battery staple")
	algo, verifier := clientNewPassword(t, password)
	if err := srp.ValidateNewPassword(&algo, verifier); err != nil {
		t.Fatalf("ValidateNewPassword(td output) = %v, want nil", err)
	}
	verifier = srp.Pad(verifier)

	secret, b, err := srp.NewChallenge(verifier)
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	challenge := srp.Challenge{Verifier: verifier, Secret: secret, B: b, Salt1: algo.Salt1}

	answer := clientProof(t, algo, password, b)
	if err := srp.Verify(challenge, answer.A, answer.M1); err != nil {
		t.Fatalf("Verify(td proof) = %v, want nil", err)
	}

	wrong := clientProof(t, algo, []byte("Tr0ub4dor&3"), b)
	if err := srp.Verify(challenge, wrong.A, wrong.M1); !errors.Is(err, srp.ErrInvalidProof) {
		t.Fatalf("Verify(wrong password) = %v, want ErrInvalidProof", err)
	}

	tampered := append([]byte(nil), answer.M1...)
	tampered[len(tampered)-1] ^= 0x01
	if err := srp.Verify(challenge, answer.A, tampered); !errors.Is(err, srp.ErrInvalidProof) {
		t.Fatalf("Verify(tampered M1) = %v, want ErrInvalidProof", err)
	}
	if err := srp.Verify(challenge, answer.A, answer.M1[:len(answer.M1)-1]); !errors.Is(err, srp.ErrInvalidProof) {
		t.Fatalf("Verify(truncated M1) = %v, want ErrInvalidProof", err)
	}
}

func TestProofDoesNotSurviveChallengeRotation(t *testing.T) {
	password := []byte("rotate me")
	algo, verifier := clientNewPassword(t, password)
	verifier = srp.Pad(verifier)

	oldSecret, oldB, err := srp.NewChallenge(verifier)
	if err != nil {
		t.Fatalf("NewChallenge old: %v", err)
	}
	answer := clientProof(t, algo, password, oldB)
	if err := srp.Verify(srp.Challenge{Verifier: verifier, Secret: oldSecret, B: oldB, Salt1: algo.Salt1}, answer.A, answer.M1); err != nil {
		t.Fatalf("Verify against its own challenge = %v, want nil", err)
	}

	newSecret, newB, err := srp.NewChallenge(verifier)
	if err != nil {
		t.Fatalf("NewChallenge new: %v", err)
	}
	if err := srp.Verify(srp.Challenge{Verifier: verifier, Secret: newSecret, B: newB, Salt1: algo.Salt1}, answer.A, answer.M1); !errors.Is(err, srp.ErrInvalidProof) {
		t.Fatalf("Verify(old proof, new challenge) = %v, want ErrInvalidProof", err)
	}
}

func TestComputeBIsDeterministicForGivenSecret(t *testing.T) {
	password := []byte("deterministic")
	algo, verifier := clientNewPassword(t, password)
	verifier = srp.Pad(verifier)
	secret := bytes.Repeat([]byte{0x42}, srp.HashSize)

	b, err := srp.ComputeB(verifier, secret)
	if err != nil {
		t.Fatalf("ComputeB: %v", err)
	}
	again, err := srp.ComputeB(verifier, secret)
	if err != nil || !bytes.Equal(b, again) {
		t.Fatalf("ComputeB not deterministic: err=%v", err)
	}
	// 用 (verifier, b, B) 存档后,客户端基于这个 B 的证明能被接受。
	answer := clientProof(t, algo, password, b)
	if err := srp.Verify(srp.Challenge{Verifier: verifier, Secret: secret, B: b, Salt1: algo.Salt1}, answer.A, answer.M1); err != nil {
		t.Fatalf("Verify(proof against ComputeB) = %v, want nil", err)
	}
}

func TestVerifyRejectsIncompleteChallengeAndBadValues(t *testing.T) {
	password := []byte("incomplete")
	algo, verifier := clientNewPassword(t, password)
	verifier = srp.Pad(verifier)
	secret, b, err := srp.NewChallenge(verifier)
	if err != nil {
		t.Fatalf("NewChallenge: %v", err)
	}
	answer := clientProof(t, algo, password, b)
	full := srp.Challenge{Verifier: verifier, Secret: secret, B: b, Salt1: algo.Salt1}

	for name, ch := range map[string]srp.Challenge{
		"no verifier": {Secret: secret, B: b, Salt1: algo.Salt1},
		"no secret":   {Verifier: verifier, B: b, Salt1: algo.Salt1},
		"no B":        {Verifier: verifier, Secret: secret, Salt1: algo.Salt1},
		"zero secret": {Verifier: verifier, Secret: make([]byte, srp.HashSize), B: b, Salt1: algo.Salt1},
	} {
		if err := srp.Verify(ch, answer.A, answer.M1); !errors.Is(err, srp.ErrIncompleteChallenge) {
			t.Fatalf("Verify(%s) = %v, want ErrIncompleteChallenge", name, err)
		}
	}

	algoDefault := srp.DefaultAlgo()
	for name, a := range map[string][]byte{
		"zero A": make([]byte, srp.HashSize),
		"A = p":  algoDefault.P,
	} {
		if err := srp.Verify(full, a, answer.M1); !errors.Is(err, srp.ErrInvalidProof) {
			t.Fatalf("Verify(%s) = %v, want ErrInvalidProof", name, err)
		}
	}

	if _, _, err := srp.NewChallenge(make([]byte, srp.HashSize)); !errors.Is(err, srp.ErrInvalidVerifier) {
		t.Fatalf("NewChallenge(zero verifier) = %v, want ErrInvalidVerifier", err)
	}
	if _, _, err := srp.NewChallenge(algoDefault.P); !errors.Is(err, srp.ErrInvalidVerifier) {
		t.Fatalf("NewChallenge(verifier = p) = %v, want ErrInvalidVerifier", err)
	}
}

func TestValidateNewPassword(t *testing.T) {
	algo, verifier := clientNewPassword(t, []byte("validate"))

	if err := srp.ValidateNewPassword(nil, verifier); !errors.Is(err, srp.ErrNewSettingsMissing) {
		t.Fatalf("nil algo = %v, want ErrNewSettingsMissing", err)
	}
	if err := srp.ValidateNewPassword(&algo, nil); !errors.Is(err, srp.ErrNewSettingsMissing) {
		t.Fatalf("empty hash = %v, want ErrNewSettingsMissing", err)
	}

	mutate := map[string]func(*srp.Algo){
		"wrong g":            func(a *srp.Algo) { a.G = 2 },
		"wrong p":            func(a *srp.Algo) { a.P = append([]byte(nil), a.P...); a.P[0] ^= 0x01 },
		"wrong salt2":        func(a *srp.Algo) { a.Salt2 = append([]byte(nil), a.Salt2...); a.Salt2[0] ^= 0x01 },
		"salt1 not extended": func(a *srp.Algo) { a.Salt1 = srp.DefaultAlgo().Salt1 },
		"salt1 bad prefix":   func(a *srp.Algo) { a.Salt1 = append([]byte(nil), a.Salt1...); a.Salt1[0] ^= 0x01 },
	}
	for name, fn := range mutate {
		bad := algo
		fn(&bad)
		if err := srp.ValidateNewPassword(&bad, verifier); !errors.Is(err, srp.ErrNewAlgoInvalid) {
			t.Fatalf("%s = %v, want ErrNewAlgoInvalid", name, err)
		}
	}

	for name, hash := range map[string][]byte{
		"zero hash": make([]byte, srp.HashSize),
		"hash = p":  srp.DefaultAlgo().P,
	} {
		if err := srp.ValidateNewPassword(&algo, hash); !errors.Is(err, srp.ErrInvalidVerifier) {
			t.Fatalf("%s = %v, want ErrInvalidVerifier", name, err)
		}
	}
}

func TestDefaultAlgoReturnsIndependentCopies(t *testing.T) {
	a := srp.DefaultAlgo()
	a.Salt1[0] ^= 0xFF
	a.Salt2[0] ^= 0xFF
	a.P[0] ^= 0xFF
	b := srp.DefaultAlgo()
	if b.Salt1[0] == a.Salt1[0] || b.Salt2[0] == a.Salt2[0] || b.P[0] == a.P[0] {
		t.Fatal("DefaultAlgo shares backing arrays between calls")
	}
}

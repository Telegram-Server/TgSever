package mtprotoedge

import (
	"math/big"
	"os"
	"testing"
)

// testHandshakePQ = 1000003 * 1000033。本包上百个端到端测试各做一次或多次握手，生产 pq 让
// 客户端每次分解约 0.3 秒，占了整包 CPU 的六成；小因子的 pq 分解在微秒级。
var testHandshakePQ = new(big.Int).Mul(big.NewInt(1000003), big.NewInt(1000033))

func TestMain(m *testing.M) {
	handshakePQ = testHandshakePQ
	os.Exit(m.Run())
}

func TestHandshakePQFactorsArePrime(t *testing.T) {
	for _, f := range []int64{1000003, 1000033} {
		if !big.NewInt(f).ProbablyPrime(20) {
			t.Fatalf("%d is not prime", f)
		}
	}
}

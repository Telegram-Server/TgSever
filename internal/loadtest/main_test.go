package loadtest

import (
	"os"
	"testing"

	"telesrv/internal/store/postgres"
	"telesrv/internal/store/postgres/pgtest"
)

// TestMain 让压测用例跑在从模板库克隆出的独立库上（见 pgtest），结束时删除；
// 克隆库只在用例真正需要时才创建。
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Options{Prefix: "loadtest", Migrate: postgres.Migrate}))
}

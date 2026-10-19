package postgres

import (
	"os"
	"testing"

	"telesrv/internal/store/postgres/pgtest"
)

// TestMain 给本包准备一个从模板库克隆出的独立库（见 pgtest），并把
// TELESRV_TEST_POSTGRES_DSN 改写成它：testPool 和直接读环境变量的测试都落在这个库里，
// 不再与其他包共用一个库。原始 DSN 用 pgtest.BaseDSN 取。
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Options{Prefix: "storepg", Migrate: Migrate, ExportEnv: true}))
}

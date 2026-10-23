package rpc

import (
	"context"
	"os"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/store/postgres"
	"telesrv/internal/store/postgres/pgtest"
)

// TestMain 让本包的 Postgres 测试使用从模板库克隆出的独立库（见 pgtest），
// 不再与其他包共用一个库；只跑内存测试时不会启动 PostgreSQL。
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Options{Prefix: "rpc", Migrate: postgres.Migrate}))
}

// testPostgresPool 打开指向本包共享克隆库的连接池（首次调用时启动私有 PostgreSQL）。
// 同包测试共享这个库，数据要用唯一 id；依赖全表状态的测试用 pgtest.NewDatabase(t)。
func testPostgresPool(t *testing.T) *pgxpool.Pool {
	t.Helper()
	pool, err := postgres.Open(context.Background(), pgtest.DSN(t))
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	t.Cleanup(pool.Close)
	return pool
}

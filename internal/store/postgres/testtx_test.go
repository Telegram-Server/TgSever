package postgres

import (
	"context"
	"sync"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"telesrv/internal/store/postgres/pgtest"
	"telesrv/internal/store/postgres/sqlcgen"
)

var txTestPool struct {
	once sync.Once
	pool *pgxpool.Pool
	err  error
}

// testTx 返回包级共享库上的一个事务，测试结束时回滚，并让测试并行运行。store 在它之上调用的
// Begin/Commit 由 pgx 变成 SAVEPOINT/RELEASE，所以 store 代码不用改。
//
// 它比 testPool（每个测试克隆一个库，约 160ms）快两个数量级，适合单连接、顺序执行的测试。
// 以下情况仍用 testPool：
//   - 并发：多个 goroutine 同时访问库，或依赖连接池本身的行为；
//   - 依赖真正提交的语义：其他连接看得见、NOTIFY、提交时的触发器、跨事务的 advisory lock；
//   - store 在一个查询的结果还没读完时发起下一个查询——同一连接上会报 "conn busy"。
//
// 数据只在本事务内可见，测试之间互不影响；但唯一约束冲突会让并行的测试互相等待，值仍要用
// randomSuffix 之类生成。
func testTx(t *testing.T) pgx.Tx {
	t.Helper()
	t.Parallel()
	txTestPool.once.Do(func() {
		txTestPool.pool, txTestPool.err = Open(context.Background(), pgtest.DSN(t))
	})
	if txTestPool.err != nil {
		t.Fatalf("open shared test pool: %v", txTestPool.err)
	}
	tx, err := txTestPool.pool.Begin(context.Background())
	if err != nil {
		t.Fatalf("begin test transaction: %v", err)
	}
	t.Cleanup(func() { _ = tx.Rollback(context.Background()) })
	return tx
}

// testDB 是测试辅助函数接收的库：testPool 的连接池和 testTx 的事务都满足它。
type testDB interface {
	sqlcgen.DBTX
	Begin(ctx context.Context) (pgx.Tx, error)
}

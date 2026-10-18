package postgres

import (
	"context"
	"errors"
	"os"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/deploy"
)

// down 脚本只在回滚时才会执行，语法错误平时谁也看不见，直到真正需要回滚的那天。
// 简单查询协议会先把整段脚本解析完再执行，所以在一个空 schema 里跑每个 down 脚本：
// 缺表、故意 RAISE 的单向迁移等执行期错误都无所谓，只有 syntax_error 说明脚本本身写错了。
func TestEmbeddedDownMigrationsParse(t *testing.T) {
	dsn := os.Getenv("TELESRV_TEST_POSTGRES_DSN")
	if dsn == "" {
		t.Skip("set TELESRV_TEST_POSTGRES_DSN to run postgres integration test")
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)

	downs := embeddedMigrationVersions(t, ".down.sql")
	names := make([]string, 0, len(downs))
	for _, name := range downs {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		body, err := deploy.Migrations.ReadFile("migrations/" + name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		tx, err := conn.Begin(ctx)
		if err != nil {
			t.Fatalf("begin: %v", err)
		}
		if _, err := tx.Exec(ctx, "CREATE SCHEMA down_syntax_probe; SET LOCAL search_path TO down_syntax_probe"); err != nil {
			_ = tx.Rollback(ctx)
			t.Fatalf("prepare probe schema: %v", err)
		}
		_, execErr := tx.Conn().PgConn().Exec(ctx, string(body)).ReadAll()
		_ = tx.Rollback(ctx)

		var pgErr *pgconn.PgError
		if errors.As(execErr, &pgErr) && pgErr.Code == "42601" {
			t.Errorf("%s does not parse: %s (near position %d: %q)", name, pgErr.Message, pgErr.Position, snippetAt(string(body), int(pgErr.Position)))
		}
	}
}

func snippetAt(body string, position int) string {
	if position <= 0 || position > len(body) {
		return strings.TrimSpace(body)
	}
	start := max(position-30, 0)
	end := min(position+30, len(body))
	return body[start:end]
}

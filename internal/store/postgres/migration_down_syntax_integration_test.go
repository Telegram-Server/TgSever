package postgres

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"telesrv/deploy"
	"telesrv/internal/store/postgres/pgtest"
)

// down 脚本只在回滚时才会执行，语法错误平时谁也看不见，直到真正需要回滚的那天。
// 简单查询协议会先把整段脚本解析完再执行，所以在一个空 schema 里跑每个 down 脚本：
// 缺表、故意 RAISE 的单向迁移等执行期错误都无所谓，只有 syntax_error 说明脚本本身写错了。
//
// 局限：plpgsql 函数体（CREATE FUNCTION ... AS $$ ... $$、DO 块）只是字符串字面量，
// 整段解析时不检查，要到语句执行时才校验；而脚本里前面的语句一旦遇到执行期错误（缺表等），
// 整段就中止，后面的语句——包括其中的函数体——都不会执行。所以这里只能保证脚本的顶层
// SQL 能解析，不能保证执行期错误之后的函数体语法正确。
func TestEmbeddedDownMigrationsParse(t *testing.T) {
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, pgtest.DSN(t))
	if err != nil {
		t.Fatalf("connect: %v", err)
	}
	defer conn.Close(ctx)
	// 本包共享一个库，schema 名带随机后缀，避免与并发的同名探针相撞。
	probe := "down_syntax_probe_" + randomSuffix(t)

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
		if _, err := tx.Exec(ctx, "CREATE SCHEMA "+probe+"; SET LOCAL search_path TO "+probe); err != nil {
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

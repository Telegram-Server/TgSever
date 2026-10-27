package main

import (
	"context"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
)

// 账号搜索是手写 SQL：名字包含匹配、通配符转义和排序只能对着真实 schema 证明。
// 每个用例用独立数据库，断言的是整张结果表。

func seedSearchUser(t *testing.T, pool *pgxpool.Pool, id int64, first, last, username string) {
	t.Helper()
	if _, err := pool.Exec(context.Background(), `
INSERT INTO users (id, access_hash, phone, first_name, last_name, username, created_at, updated_at)
VALUES ($1, $1, '', $2, $3, $4, now(), now())`, id, first, last, username); err != nil {
		t.Fatalf("seed user %d: %v", id, err)
	}
}

func searchAccountIDs(t *testing.T, store *readStore, q string) []int64 {
	t.Helper()
	rows, err := store.SearchAccounts(context.Background(), q)
	if err != nil {
		t.Fatalf("SearchAccounts(%q): %v", q, err)
	}
	ids := make([]int64, 0, len(rows))
	for _, row := range rows {
		ids = append(ids, row.ID)
	}
	return ids
}

func assertSearchIDs(t *testing.T, store *readStore, q string, want ...int64) {
	t.Helper()
	got := searchAccountIDs(t, store, q)
	if len(got) != len(want) {
		t.Fatalf("SearchAccounts(%q) = %v, want %v", q, got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("SearchAccounts(%q) = %v, want %v", q, got, want)
		}
	}
}

func TestSearchAccountsMatchesPartOfFullName(t *testing.T) {
	store, pool := isolatedReadStore(t)
	seedSearchUser(t, pool, 4_100_000_001, "Wang", "Xiaoming", "")
	seedSearchUser(t, pool, 4_100_000_002, "Li", "Wang", "")
	seedSearchUser(t, pool, 4_100_000_003, "Zhang", "San", "")

	// 不区分大小写，名和姓任一侧都能命中。
	assertSearchIDs(t, store, "wANg", 4_100_000_001, 4_100_000_002)
	// 匹配的是“名 + 空格 + 姓”，关键词可以跨过中间的空格。
	assertSearchIDs(t, store, "g xiao", 4_100_000_001)
	assertSearchIDs(t, store, "nobody", []int64{}...)
}

func TestSearchAccountsSkipsNameMatchForSingleCharacter(t *testing.T) {
	store, pool := isolatedReadStore(t)
	seedSearchUser(t, pool, 4_100_000_011, "张", "", "")
	seedSearchUser(t, pool, 4_100_000_012, "张伟", "", "")

	// 长度按字符而不是字节算：“张”在 UTF-8 里占 3 字节，仍只算 1 个字符。
	assertSearchIDs(t, store, "张", []int64{}...)
	assertSearchIDs(t, store, " 张 ", []int64{}...)
	assertSearchIDs(t, store, "张伟", 4_100_000_012)
}

func TestSearchAccountsTreatsLikeWildcardsLiterally(t *testing.T) {
	store, pool := isolatedReadStore(t)
	seedSearchUser(t, pool, 4_100_000_021, "50%_off", "", "")
	seedSearchUser(t, pool, 4_100_000_022, "50xyoff", "", "")
	seedSearchUser(t, pool, 4_100_000_023, `back\slash`, "", "")

	assertSearchIDs(t, store, "%_off", 4_100_000_021)
	assertSearchIDs(t, store, "0_", []int64{}...)
	assertSearchIDs(t, store, `k\s`, 4_100_000_023)
}

func TestSearchAccountsRanksExactMatchesBeforeNameMatches(t *testing.T) {
	store, pool := isolatedReadStore(t)
	// 精确匹配的账号 id 最大，名字匹配的账号 id 更小且数量超过上限：
	// 结果必须先列精确匹配，再按 id 列名字匹配，总数截在上限。
	const exactID = 4_100_000_999
	seedSearchUser(t, pool, exactID, "Someone", "Else", "kowalski")
	nameIDs := make([]int64, 0, accountSearchLimit+3)
	for i := 0; i < accountSearchLimit+3; i++ {
		id := int64(4_100_000_100 + i)
		seedSearchUser(t, pool, id, "Kowalski", "", "")
		nameIDs = append(nameIDs, id)
	}

	want := append([]int64{exactID}, nameIDs[:accountSearchLimit-1]...)
	assertSearchIDs(t, store, "KOWALSKI", want...)
	// 带 @ 是明确的用户名搜索：名字里没有 “@” 的账号不会被名字匹配带出来。
	assertSearchIDs(t, store, "@Kowalski", exactID)
}

// 搜索结果带 bot 标记：白名单按搜索添加成员时（#160），名单不用再读一次库就能标出 bot。
func TestSearchAccountsMarksBots(t *testing.T) {
	store, pool := isolatedReadStore(t)
	seedSearchUser(t, pool, 4_100_000_101, "Quillfeather", "Person", "")
	seedSearchUser(t, pool, 4_100_000_102, "Quillfeather", "Bot", "")
	if _, err := pool.Exec(context.Background(), `UPDATE users SET is_bot = true WHERE id = 4100000102`); err != nil {
		t.Fatal(err)
	}
	rows, err := store.SearchAccounts(context.Background(), "quillfeather")
	if err != nil {
		t.Fatal(err)
	}
	if len(rows) != 2 || rows[0].ID != 4_100_000_101 || rows[0].Bot || rows[1].ID != 4_100_000_102 || !rows[1].Bot {
		t.Fatalf("rows=%+v", rows)
	}
}

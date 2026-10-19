package main

import (
	"crypto/rand"
	"encoding/binary"
	"os"
	"testing"

	"telesrv/internal/store/postgres"
	"telesrv/internal/store/postgres/pgtest"
)

// TestMain 让本包的 Postgres 集成测试使用从模板库克隆出的独立库（见 pgtest），
// 不再与其他包共用 TELESRV_TEST_POSTGRES_DSN 指向的库。
func TestMain(m *testing.M) {
	os.Exit(pgtest.Run(m, pgtest.Options{Prefix: "admin", Migrate: postgres.Migrate}))
}

// randomBelow returns a crypto-random value in [0, n). Integration tests share
// the package database, so ids and phone suffixes come from here rather than
// from the clock, where two tests started in the same instant collide.
func randomBelow(t testing.TB, n int64) int64 {
	t.Helper()
	var b [8]byte
	if _, err := rand.Read(b[:]); err != nil {
		t.Fatal(err)
	}
	return int64(binary.LittleEndian.Uint64(b[:])>>1) % n
}

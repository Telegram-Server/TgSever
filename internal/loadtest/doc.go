// Package loadtest 承载 message 模块的压测基线。
//
// 这里只放 env-gated 的压测用例（真实 PostgreSQL + Redis），不被生产代码 import。
// 目标与基线方法见 docs/message-module.md 的「Load Baseline」一节。
//
// 运行方式（PowerShell）：
//
//	$env:TELESRV_TEST_POSTGRES_DSN = "postgres://telesrv:telesrv@localhost:5432/telesrv_test?sslmode=disable"
//	$env:TELESRV_TEST_REDIS_ADDR   = "localhost:6399"
//	go test ./internal/loadtest/ -run TestMessageSendBaseline -v -count=1
//
// 未设置上述两个环境变量时用例直接 Skip，因此对默认 `go test ./...` 无副作用。
// DSN 的库名须含 "test"、账号须有 CREATEDB：数据写进由模板库克隆的临时库（pgtest），
// 用例结束即删除，不写进 DSN 指向的库本身。
package loadtest

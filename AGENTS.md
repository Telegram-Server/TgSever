# AGENTS.md

Telegram-compatible MTProto server written in Go. This file is a module map so you can find and fix things without re-learning the codebase.

## Identity and branches

- Go module is `telesrv` (Go 1.25), repo is `gramsrv`, binary is `telesrv`. First compatibility target: **Telegram Desktop**. Protocol comes from `github.com/iamxvbaba/td` (fork of gotd/td); do not vendor or fork `td` here.
- `main` = **monolith** (this branch), `v2` = microservices (Edge/Core/Egress/File/SFU). CI runs on `main`, `grammy`, `admin`. Current checkout: `main`.
- Comments and package docs are commonly written in **Chinese** (e.g. `cmd/telesrv/main.go`, `internal/config/config.go`). Read them; match the style for new code. PowerShell artifacts may display `???` for Chinese in some terminals — the files are UTF-8.
- No Makefile, no linter config, no test framework beyond stdlib `testing`. Formatting = `gofmt`.

## Commands

```powershell
go build -o bin/gramsrv.exe ./cmd/telesrv        # main server
go vet ./...
go test ./... -count=1                            # CI gate, passes without external services
```

### Tests that need Postgres

Postgres integration tests are `*_integration_test.go` in `internal/store/postgres` and `cmd/telesrv-admin` (plus a few in `internal/rpc` and `internal/loadtest`). They **skip** unless `TELESRV_TEST_POSTGRES_DSN` is set, and the DSN's database name **must contain "test"** or they fail fast. The DSN's role needs **CREATEDB**.

The shared machinery is `internal/store/postgres/pgtest` (wired in each package's `TestMain`):

- **Template database** `telesrv_tmpl_<hash>_test`, named after a hash of the embedded migrations. Built once — migrated from empty to head and checked against the head version — then marked `IS_TEMPLATE`. Reused while the migrations are unchanged; a migration change builds a new one (cold run ≈ 15 s). Stale templates from old hashes are not pruned automatically: `ALTER DATABASE … IS_TEMPLATE false; DROP DATABASE …`.
- **Per-test clone, parallel** — the default in `internal/store/postgres`: `testPool(t)` (and `pgtest.TestDSN(t)`) clone a database for the calling top-level test and mark it `t.Parallel()`. Isolation is structural, so tests run concurrently without auditing them for shared state; repeated calls within one test (and its subtests) return the same database. At most 8 such databases live at once (`PGTEST_PARALLEL_DBS`; 24 is fine on the tmpfs instance below). Only tests that depend on the whole server (connection admission, `max_connections`) stay on the serial per-package database.
- **Per-package clone** (`<pkg>_<rand>_test`), dropped when the test binary exits. `pgtest.DSN(t)` points at it; in `internal/store/postgres` the `TestMain` also rewrites `TELESRV_TEST_POSTGRES_DSN` to it. Tests on it run serially and share it: use unique ids or clean up.
- **Per-test clone without parallelism**: `pgtest.NewDatabase(t)`.
- **Use the tmpfs test instance.** Every test creates and drops a database; on a disk-backed server each `CREATE/DROP DATABASE` writes WAL and forces a checkpoint, and the package is disk-bound (≈160 s vs ≈110 s, full `go test ./...` ≈440 s → ≈150 s). `docker compose -f deploy/docker-compose.yml --profile test up -d postgres-test` starts one on port 5433 with `fsync=off` on tmpfs; point `TELESRV_TEST_POSTGRES_DSN` at `postgres://telesrv@127.0.0.1:5433/telesrv_test?sslmode=disable` and set `PGPASSWORD` to the `POSTGRES_PASSWORD` of the `postgres-test` service (pgx reads it). Never point a dev server at it — it empties on restart.
- A killed test run (timeout, Ctrl-C) can leave `*_test` clones behind; drop them by hand.

Run a focused subset like CI does:

```powershell
$env:TELESRV_TEST_POSTGRES_DSN="postgres://inventory:inventory-test@127.0.0.1:5432/gift_inventory_test?sslmode=disable"
go test ./internal/store/postgres -run '^(TestOfficialStarGiftInventoryPostgres|TestStarsLedgerPostgres)$' -count=1
go test ./cmd/telesrv-admin -run '^(TestOperatorCommand|TestAuditTrail)$' -count=1
```

- `deploy` Compose integration test (`deploy/docker-compose_test.go`) SKIPS unless `TELESRV_RUN_COMPOSE_INTEGRATION=1` — it actually boots a Docker stack. Don't run by accident.
- Most `internal/rpc` and `internal/app` tests run against in-memory stores (`internal/store/memory`, `rpc_testkit_*`) and are pure `go test`.
- Load testing: `cmd/telesrv-load` binary + `internal/loadtest`, `internal/loadharness`.

### Building the browser surfaces (Node, different repos)

- Admin web UI: `cmd/telesrv-admin/web` (`npm ci`, `npm run build`).
- Grammy store bot: `cmd/bots/grammystore` (TypeScript, own deployment: `npm run check`, `npm test`, own Dockerfile). Only target these if the task is about the admin UI or the store bot.

### Adding a page to the admin panel

The panel's navigation was a flat sidebar that every feature added a row to, and
the three things that render it -- the sidebar, the in-page tab bars, the route
table -- each kept their own list and their own idea of permissions. They
drifted: twelve sidebar entries rendered unconditionally while every endpoint
behind them was gated, so an operator without `accounts.read` saw Accounts and
got a bare 403. `src/nav.ts` replaced all three lists with one declaration.

The rules that keep it that way:

- **A sibling page is a tab, not a second sidebar row.** If the new page does the
  same job as an existing one over a different slice, add it to that section's
  `tabs` array. Commerce holds Prices and the Stars ledger; Media holds Storage;
  Accounts holds Account Rating and Shared devices. A new row is only for a
  genuinely new job. A section with a single visible tab draws no tab bar.
- **Declare the tab's real right.** It must be the right the route enforces
  server-side. A right that is not in the table has no route of its own -- it
  widens an action on a page already gated by its read right. If you need a new
  right, it goes in `permissions.tsx` (`permissionLabels` *and*
  `permissionGroups`) *and* `assignablePermissions()` in `security.go`; a right
  outside that list cannot be granted to a named operator.
- **Do not hand-wrap a route in `PermissionGate`.** The gate already comes from
  `routePermission(path)`. Hand-wrapping is what let the gaps accumulate.
- **A section shows when the session holds *any* of its tabs' rights**, and links
  to the first visible tab -- don't add your own visibility logic.
- **Translate every string** via `useI18n()` into all three language blocks of
  `i18n.tsx`.
- **Rebuild `dist` and commit it.** `cmd/telesrv-admin/server.go` has
  `//go:embed web/dist`, so a non-Docker build ships the committed bundle: a
  `web/src` change without a rebuilt `dist` does not ship. Delete the old hashed
  assets rather than leaving them.
- **Regenerate the API docs:** `python3 scripts/gen_admin_panel_doc_tables.py --write`
  (both `docs/admin-panel-api.*.md`); `--check` fails if they are stale. A new
  route in `cmd/telesrv-admin/server.go` also needs a hand-written row in the
  doc's `## API: ...` tables in both languages: `--check` fails on a route with
  no row and on a row for a route that is gone, and `--write` fills in the
  Permission column of the action tables from the route's gate.

### SQL codegen

`internal/store/postgres/sqlcgen` (package `sqlcgen`, pgx/v5, `emit_pointers_for_null_types`) is generated by sqlc from `sqlc.yaml`. Schema is **only** `deploy/migrations/*.up.sql`; the `.down.sql` files are deliberately excluded so sqlc never sees DROPs. Changes to `internal/store/postgres/queries/*.sql` or `.up.sql` require regenerating: `sqlc generate` (install sqlc v1.31.1 first: `go install github.com/sqlc-dev/sqlc/cmd/sqlc@v1.31.1`; not in PATH by default). Generated code is committed — keep it in sync; CI runs `sqlc diff` and fails when it is stale, so a migration that adds a column must be committed together with the regenerated `sqlcgen`.

**New database code uses sqlc.** Write the query in `queries/<feature>.sql` and call the generated method; sqlc checks every column and type against the migrations at generate time, while hand-written SQL only fails at runtime. Hand-written SQL is allowed only where sqlc cannot express the statement, and the reason goes in a comment next to it:

- dynamic SQL — optional WHERE clauses, a caller-chosen ORDER BY, IN lists built at runtime;
- pgx-only features — `CopyFrom`, `SendBatch`, advisory locks, LISTEN/NOTIFY;
- syntax sqlc cannot parse (multi-argument `unnest`, for one — prefer rewriting as `unnest … WITH ORDINALITY` joins first).

Most of the existing store is hand-written SQL from before this rule. Leave it unless you are rewriting that code anyway; do not copy its style into new code.

## Request flow (how the server is wired)

`cmd/telesrv/main.go` (one huge `run()` ~1900 lines) wires everything:

```
MTProto client
  → internal/mtprotoedge   TCP + WS transport, handshake, auth keys, sessions,
                           encryption, admission budget (conn limits, RPC inflight,
                           byte budgets)
  → internal/rpc           TL dispatch (Router), wrapper handling
                           (invokeWithLayer/initConnection/invokeAfter, max depth 8),
                           layer binding, temp-key resolution, panic isolation
  → internal/app/*         feature business logic (43 packages)
  → internal/store         persistence: postgres | memory | redisstore
  ← updates/state          outbox dispatchers, durable delivery, projections
```

Binaries in `cmd/`: `telesrv` (server), `telesrv-admin` (admin dashboard), `telesrv-load`, `telesrv-update` (desktop update CDN: `telesrv-update publish <pkg>` adds a signed v2 package to the manifest; with no subcommand it serves the manifest standalone. `telesrv` itself serves the same handler under `/_updates/` on the public link listener when `TELESRV_UPDATE_MANIFEST` is set — guide in `docs/update-service.md`), `gramsrv-backup` (bundle capture/restore/verify; guide in `docs/backup-restore.md`, wrappers in `scripts/backup.{sh,ps1}`, `scripts/restore.{sh,ps1}`, `scripts/check-media.{sh,ps1}`), plus fetch/seed utilities (`catalogfetch`, `giftfetch` — guide in `docs/giftfetch.md`, including the `-login-qr` session flow — `langpackfetch`, `stickerseeddeploy`, `blobmigrate`, `telegramloginkeygen`, `walletminiapp`, `bots/*`).

## Module map (find-and-fix guide)

- **`internal/domain/`** — pure protocol-agnostic types (users, messages, media, star gifts, premium…). Shared structs; change here when a field must appear everywhere.
- **`internal/rpc/`** (460 files, the biggest package) — one file (or `_rpc_test.go`) per feature/namespace: `messages_send.go`, `channels_members.go`, `payments_star_gifts.go`, `presence_work.go`, `convert_*.go` (TL conversion), `errors.go` (error mapping to TL), `layer_*.go` (layer gating). **Most bug fixes land here or in `app/`.** `router.go` holds dispatch, `deps.go` the injected service set (`rpc.Deps`, validated by `deps_validation_test.go`).
- **`internal/app/*`** — feature business logic used by the RPC layer. Pattern: `service.go` + tests. Start from the `.go` file matching the feature name, e.g. `app/stargifts`, `app/messages`, `app/verification`.
- **`internal/store/`** — three parallel backends, same store interfaces:
  - `postgres/` — real persistence; hand-written SQL in `queries/*.sql`, sqlc output in `sqlcgen/`, store methods in `<feature>.go`, tests in `*_integration_test.go` + in-memory `storetest`.
  - `memory/` — in-memory implementations used by fast RPC tests (not production).
  - `redisstore/` — ephemeral state: rate limits, ephemeral keys, delivery.
- **`internal/mtprotoedge/`** — protocol edge: transport, auth key lifecycle, session management, frame budgets. Concurrency/budget bugs (10k sessions, reconnects) are here.
- **`internal/compat/{android,ios,tdesktop}/`** — per-client quirks. If a fix targets one specific client, check here first (e.g. `compat/tdesktop` for TDesktop wire behavior).
- **Bot API** — not served here. An external fork of telegram-bot-api (TDLib) logs in as the bot over MTProto; the server only has to get MTProto bot semantics right. Bot qts sequence (member changes etc.): `internal/rpc/bot_updates.go` + `store/*/bot_updates.go`, retention `TELESRV_BOT_UPDATE_RETENTION`. TDLib's expectations are pinned in `internal/mtprotoedge/tdlib_bot_contract_e2e_test.go`. `scripts/botapi_*.py`, `cmd/bots/*` and `cmd/walletminiapp` are manual smoke tools that must be pointed at telegram-bot-api. Built-in bots (`internal/app/bots`) run in-process. Guide: `docs/bot-api.en.md`.
- **`internal/admin/`**, **`internal/adminapi/`** — admin panel state/ops + admin HTTP API (port 2599) with RBAC (`adminapi/rbac.go`, scoped tokens).
- **`cmd/telesrv-admin/web/`** — the panel's React SPA. **`src/nav.ts` is the only place navigation is declared**: every sidebar section, the paths belonging to it, and the right each path enforces server-side. `Layout.tsx` renders the sidebar from it, `SectionTabs.tsx` reads each section's tab list from it, and `Routes.tsx` gates the whole surface from it through `routePermission(path)`. **Read this before adding a page** — the rules are below.
- **`internal/web/`** — public-link web server, port 2401, `/healthz` returns `ok`.
- **`internal/sfu/`** — embedded WebRTC SFU (pion); **`internal/turnsrv/`** — embedded TURN/STUN. Both optional, log `sfu listening`/`turn listening` on readiness.
- **`internal/otpdelivery/`** — phone-code delivery providers (`smtp/`, `webhook/`, `development`). `internal/telegramloginhttp/` + `internal/app/telegramlogin/` — Telegram Login/OIDC provider (needs `cmd/telegramloginkeygen`).
- **`internal/branding/`**, **`internal/config/`**, **`internal/observability/{metrics,dbtrace}/`**, **`internal/readmodelcache/`**, **`internal/links/`** — cross-cutting.
- **`deploy/`** — `migrations/` (embedded via `deploy/deploy.go` → `embed.FS`), `docker-compose.yml` (dev deps: Postgres 17 + Redis 7, same branch's DBs), `docker/` (production compose, entrypoint, assets, bridge-network override), `postgres-init/`.

## Migrations

- `deploy/migrations/NNNN_name.up.sql` + matching `.down.sql`; single source of truth for both golang-migrate (embedded, run on startup via `store/postgres.Migrate`) and sqlc. **Migration 0123 is deliberately skipped** — the sequence jumps 0122 → 0124, don't reuse 0123.
- After 0223 versions are timestamps (`YYYYMMDDhhmmss_name`). A new migration must sort after every existing one: golang-migrate keeps a single `schema_migrations.version`, so one slotted in between is never run on a deployed database. `TestEmbeddedMigrationsAreContiguousAndReversible` checks this against `publishedTimestampVersions`; adding a migration at the end needs no test change (append it to that list once it has shipped).
- DBs are branch-isolated: `telesrv_main` and `telesrv_v2` in one container (`deploy/postgres-init/010_branch_databases.sql`). Legacy `telesrv` DB is kept for compat/migration only — never touch it.
- Windows dev scripts derive the Postgres DB from the current branch name (`telesrv_main` for `main`, `telesrv_v2` for `v2`).

## Config and dev loop

- Config loads from `.env` in the working dir (or process env; `TELESRV_CONFIG` overrides the file path). Only `.env.example` is tracked — never commit `.env`.
- Dev login code is `12345`. RSA key auto-generated at `data/server_rsa.pem` if missing.
- Windows helpers (repo-root PowerShell, the normal dev loop):
  - `scripts/restart-local-server.ps1` — build `cmd/telesrv`, kill old process on port, restart, wait for listen; resolves the branch-implied Postgres DSN via `ensure-local-databases.ps1`.
  - `scripts/check-local-runtime.ps1` — read-only health check (listener, migrations, log readiness line/errors, adb).
  - `scripts/start-docker.ps1` — one-command Docker launch; generates `deploy/docker/.env` on first run.
- Ports: MTProto **2398**, RTMP/livestream **2400**, public links **2401**, admin API **2599**, admin UI **2600**, SFU UDP **12399**, TURN UDP **12400**, TURN relay 12500–12999 (bridge mode max 12563), Postgres host **15432** (dev compose: 5432), Redis host **16379** (dev compose: 6399).

## Docker

- Production compose: `deploy/docker/compose.yaml` (host networking by default; `compose.bridge-network.yaml` for hosts without it). Images: `ghcr.io/iamxvbaba/gramsrv/{server,admin,grammystore}`.
- Hardened containers: `read_only` rootfs, `cap_drop: [ALL]`, `pids_limit: 1024`, user 10001:10001, entrypoint rejects empty/`changeme` secrets. CI (`docker-smoke`) asserts all of this; keep it true.
- Image build targets: `server` (generated RSA identity) vs `server-test` (embedded public test key, default). CI publishes `server-test`.

## Gotchas

- `go test ./...` takes a while (many packages, chromedp-free but heavy). Prefix `-run` when iterating.
- Some tests assert on log lines like `sfu listening` / `telesrv 服务就绪` — don't remove those readiness messages.
- `scripts/install.sh` is git-ignored on purpose (internal deployment script, not part of the public repo). Don't add it.
- `telesrv-admin.exe` at the repo root is a force-added artifact; ignore it.
- `.session` and `*.session.qr.png` are **Telegram account credentials**, produced by `giftfetch -login-qr` / `appearancefetch` / `scripts/giftfetch_telethon.py` (whose `--session` defaults to a bare relative name, so running it from the repo root drops one in the tree). They are git-ignored; keep it that way and never quote a session path into a committed example.
- `docs/admin-panel-api.{en,ru}.md` carry **generated** route and permission tables (`scripts/gen_admin_panel_doc_tables.py`). Edit `nav.ts` / `permissions.tsx` and rerun with `--write`; `--check` is the form worth running in CI. They drifted badly once because nothing checked them.

## Agent skills

### Issue tracker

Issues are tracked in GitHub Issues for Telegram-Server/TgSever, via the `gh` CLI. See `docs/agents/issue-tracker.md`.

### Triage labels

The five default triage labels: `needs-triage`, `needs-info`, `ready-for-agent`, `ready-for-human`, `wontfix`. See `docs/agents/triage-labels.md`.

### Domain docs

Single-context: one `CONTEXT.md` and `docs/adr/` at the repo root, created lazily. See `docs/agents/domain.md`.

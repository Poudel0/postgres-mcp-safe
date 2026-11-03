# Safe Postgres MCP server: spec, stack, architecture (v1)

Status: draft, 2026-11-01. Scope: `SCOPE.md` (not widened here). Package name: `<pkg>` (TBD).

## 1. Summary

- A Go binary that speaks MCP over stdio and replaces `@modelcontextprotocol/server-postgres@0.6.2`.
- Same CLI shape (one positional connection URL), same `query` tool, same `postgres://.../<table>/schema` resources.
- Read-only is enforced by Postgres, not by parsing SQL: one statement per call (extended protocol), only statements that return rows, inside a `READ ONLY` transaction, on a fresh connection that is closed afterwards.
- Timeout, row cap and size cap are on by default. `EXPLAIN ANALYZE` is off by default.
- Ships through npm (`npx -y <pkg> postgresql://...`), Docker and GitHub release binaries, and is listed in the MCP registry.

## 2. Verified facts about the old server

Source read from the npm tarball `@modelcontextprotocol/server-postgres@0.6.2` (`dist/index.js`) and from
`modelcontextprotocol/servers-archived/src/postgres/index.ts`. The two are the same code.

| # | Fact | Source |
|---|---|---|
| F1 | CLI: `mcp-server-postgres <databaseUrl>`. Only `argv[2]` is used; extra args are ignored. No arg: prints `Please provide a database URL as a command-line argument` to stderr, exits 1. | `index.ts` L26-32 |
| F2 | The URL must parse with `new URL()`, so only URL-form strings work (no `host=... dbname=...` DSNs). The DB is reached through `pg.Pool` with no timeout or limits. | L34-40 |
| F3 | One tool: `{"name":"query","description":"Run a read-only SQL query","inputSchema":{"type":"object","properties":{"sql":{"type":"string"}}}}`. No `required`. | L94-109 |
| F4 | `query` result: `content:[{type:"text", text: JSON.stringify(result.rows, null, 2)}]`, `isError:false`. Rows are node-pg row objects (column name to value). | L118-121 |
| F5 | Errors are thrown, so the TS SDK 1.0.1 sends a JSON-RPC error: code `-32603` (pg error codes are strings, not integers) with the pg message. Never `isError:true`. | `@modelcontextprotocol/sdk@1.0.1` `shared/protocol.js` L113-125 |
| F6 | `resources/list`: `SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'`. Each entry: `uri`, `mimeType:"application/json"`, `name:"\"<table>\" database schema"`. No pagination. | L44-60 |
| F7 | URI = `new URL("<table>/schema", base)` where base is the DB URL with protocol `postgres:` and the password cleared. Relative resolution drops the DB name and query string: `postgresql://alice:pw@db:5432/mydb?sslmode=require` gives `postgres://alice@db:5432/users/schema`. The username stays. | L34-36, L52; checked with Node's URL |
| F8 | `resources/read`: takes the last two path segments; the last must be `schema`. Runs `SELECT column_name, data_type FROM information_schema.columns WHERE table_name = $1`, with **no schema filter and no ORDER BY**. Returns `contents:[{uri, mimeType:"application/json", text: JSON.stringify(rows, null, 2)}]`. | L62-92 |
| F9 | Read-only mechanism: `BEGIN TRANSACTION READ ONLY`, then `client.query(sql)` (simple protocol), then `ROLLBACK` (not awaited), then the connection goes back to the pool. | L115-133 |
| F10 | Why `COMMIT; ...` works: node-pg's `client.query(string)` sends a simple Query message, which may hold many statements. `COMMIT` closes the read-only transaction and the rest runs outside it. Postgres docs: if a Query message "contains a COMMIT or ROLLBACK closing the existing transaction block, then any following statements are executed in an implicit transaction block". Second attack: `COMMIT; SET statement_timeout TO 1;` sticks to a pooled connection and hits later callers. | Datadog article; postgresql.org/docs/current/protocol-flow.html |
| F11 | Datadog Security Labs, "MCP vulnerability case study: SQL injection in the Postgres MCP server", Santiago Mola, **2025-08-21**. It says the package was deprecated on 2025-07-10. | article front matter |
| F12 | A fix (prepared statement + `client.release(true)`) was merged in `modelcontextprotocol/servers` PR #1889 on 2025-05-29. It was **never published**: npm is still 0.6.2 (2024-12-04), and the copy in `servers-archived` is the pre-fix code. | `gh pr view 1889`; npm `time` |
| F13 | **No official successor.** The archived README says only "no security guarantees" and links to `modelcontextprotocol/servers`, which has no Postgres server. Issue #4785 (open, triaged Low on 2026-10-19) says "There is no replacement to migrate to". The only named alternative anywhere is Datadog's pointer to Zed's fork (`@zeddotdev/postgres-context-server`, now 0.1.7, 873 downloads/week). | archived `README.md`; issue #4785 |
| F14 | Docker image `mcp/postgres`, used as `docker run -i --rm mcp/postgres postgresql://host.docker.internal:5432/mydb`. | archived `src/postgres/README.md` |
| F15 | Old server speaks protocol `2024-11-05` (SDK 1.0.1 `LATEST_PROTOCOL_VERSION`). | sdk `types.js` |

**Assumptions (not verified):** clients key on tool name and resource URIs, not on `serverInfo.name`
(`example-servers/postgres`). Clients don't parse the JSON text strictly (the model reads it). Nobody relies on
the JSON-RPC error shape from F5.

## 3. Stack

| Choice | Version (checked 2026-11-01) | Why |
|---|---|---|
| Go | 1.27 | One static binary per platform, no runtime to install. Makes npm, Docker and release binaries the same artifact. |
| `github.com/jackc/pgx/v5` | v5.12.0 | Pure Go (no cgo, so easy cross-builds). Lets us pick the protocol per call, read the statement's result columns before running it, and cap message size. |
| `github.com/modelcontextprotocol/go-sdk` (`mcp`) | v1.8.0 (2026-09-14), 5.3k stars | Official SDK, run by the MCP org. Supports protocol 2024-11-05 up to 2026-07-28. Lets us set a raw `InputSchema` (byte-exact compat), has receiving middleware (for dynamic resources) and a client with `CommandTransport` (for compat tests). |

Rejected: `mark3labs/mcp-go` v1.5.0 (9.2k stars, releases every 1-2 weeks, same protocol range). It is good and more
popular, but the official SDK is the safer long-term bet for a "boring and correct" pitch. GoReleaser's built-in npm
publisher: Pro-only, alpha, and uses `postinstall`.

## 4. Architecture

One Go `main` package, four files. No interfaces: there is one database and one transport.

| File | Job |
|---|---|
| `main.go` | Parse flags and the URL (`pgx.ParseConfig`, fail fast if it's bad), build the server, `server.Run(ctx, &mcp.StdioTransport{})`. |
| `db.go` | `readOnly(ctx, fn)`: the only way to touch the DB. `runQuery(ctx, tx, sql)`: prepare, check, read rows under the caps. |
| `tools.go` | `query`, `list_tables`, `describe_table`, `explain`: schemas as literal JSON, handlers. |
| `resources.go` | Middleware that answers `resources/list` and `resources/read` exactly like F6-F8. |

Flow of one `query` call:

```
client --stdio--> go-sdk --> query handler
                               | validate input (sql is a non-empty string)
                               v
                   readOnly(ctx with deadline = --timeout + 1s)
                     1 pgx.ConnectConfig        fresh connection, MaxProtocolMessageBodyLen = --max-bytes
                     2 BeginTx(ReadOnly)        BEGIN READ ONLY
                     3 superuser check + set_config('statement_timeout', <ms>, true), one round trip
                     4 tx.Prepare("q", sql)     Parse + Describe. Many statements -> error here
                     5 len(sd.Fields) == 0 ?    reject: no result columns, never executed
                     6 tx.Query(ctx, "q")       Bind + Execute
                     7 read rows                stop at --max-rows or --max-bytes of JSON
                     8 Close                    always, in defer; ends the transaction
                               |
client <--stdio-- go-sdk <-- content[0] = JSON rows, content[1] = truncation note (only if capped)
```

Where each limit lives: timeout at steps 1-8 (context, client side) and step 3 (server side); single statement at
step 4 (server); result-column rule at step 5; row and byte caps at step 7; per-message size in step 1.
`list_tables`, `describe_table`, `explain` and both resource methods use the same `readOnly` path with fixed SQL and
bind parameters.

Why a fresh connection per call: the Datadog session-state attack (F10) needs a pooled connection to survive. With
no pool there is nothing to clean or reset. A stdio server has one client and a low call rate, so the connect cost is
fine (see open question Q3).

Errors are values. `db.go` wraps with context (`fmt.Errorf("prepare: %w", err)`). Handlers turn execution errors into
`isError:true` results that carry the Postgres message and SQLSTATE, so the model can fix its SQL. That is the spec's
rule for tool execution errors. Logs go to stderr only and never print the password.

## 5. Compatibility contract

| Surface | Contract | Test |
|---|---|---|
| CLI | `<pkg> [flags] <databaseUrl>`. Same no-arg message and exit 1 (F1). Flags must come **before** the URL (Go `flag` stops at the first non-flag arg). Keyword DSNs and `PG*` env vars also work (pgx), which is a superset of F2. | `exec` tests on argv |
| `query` tool | Name, description and `inputSchema` byte-identical to F3. Result: one text item holding a 2-space-indented JSON array of row objects (F4). | golden `tools/list` and `tools/call` |
| Extra tools | `list_tables`, `describe_table`, `explain`. Additive; old configs ignore them. | golden |
| Resources | List query, `uri`, `name`, `mimeType` as in F6-F7. Read output shape as in F8. | golden `resources/list` and `resources/read` |
| Docker | `docker run -i --rm <image> <url>`, same as F14. | CI smoke test |

Known, intended differences (each listed in the README):

1. Tool errors come back as `isError:true` results, not JSON-RPC `-32603` (F5).
2. `resources/read` adds `table_schema = 'public'` and `ORDER BY ordinal_position`. The old query mixed columns of
   same-named tables from other schemas, and the list only ever shows `public` tables.
3. Percent-encoded table names in a URI are decoded. The old server passed them encoded, so they never matched.
4. Values are Postgres's own text output, typed only where it's unambiguous: `bool` is a JSON boolean; integer,
   float, `numeric` and `oid` are JSON numbers when the text is a valid JSON number (so `NaN`/`Infinity` stay
   strings); `json`/`jsonb` are embedded as-is; everything else is a string exactly as `psql` prints it. So `bigint`
   and `numeric` are numbers (node-pg: strings), timestamps look like `2026-01-02 03:04:05+00` (node-pg: UTC ISO with
   ms), `bytea` is `\x0102` (node-pg: Buffer object), arrays are `{x,y}` (node-pg: JS arrays). Chosen over Go types
   because it works for every type, including ones we don't know, and can't fail to encode.
5. Statements with no result columns (`SET`, `DO`, `COPY`, ...) are rejected. The old server ran them and returned `[]`.

## 6. Safety design

Layers, from the protocol inward. Each layer stands on its own; the bypass suite tests them one at a time.

| Layer | Mechanism | Verified basis |
|---|---|---|
| 1 Single statement | User SQL only goes to `tx.Prepare`, i.e. a Parse message. Postgres: "The query string contained in a Parse message cannot include more than one SQL statement; else a syntax error is reported." **Never pass user SQL to `Exec`**: pgx `Conn.exec` says "Always use simple protocol when there are no arguments." Empty SQL is rejected at the boundary, because pgx `Query("")` also falls back to simple protocol. The statement name must be non-empty (`""` would turn the later `Query("")` into a simple-protocol call). | protocol-flow docs; pgx `conn.go` (`exec`, `Query`, `Prepare`) |
| 2 Rows only | After Describe, `StatementDescription.Fields` must be non-empty, or the statement is never executed. This one rule blocks `COMMIT`, `SET`, `DO`, `COPY ... TO PROGRAM`, `LOAD`, `PREPARE` and friends without reading any SQL text. | pgconn `Prepare` sends Describe; `StatementDescription.Fields` |
| 3 Read-only transaction | `BeginTx(TxOptions{AccessMode: pgx.ReadOnly})`, always rolled back. Postgres blocks writes, DDL, `nextval()` and turning read-write back on after the first snapshot. | pgx `tx.go` |
| 4 Fresh session | New connection per call, closed after. No state (GUCs, prepared statements, advisory locks, temp tables) lives past one call. | design |
| 5 Time | `statement_timeout` via `set_config(..., true)`, plus a client-side context deadline that SQL cannot change. `set_config` is used instead of a startup parameter because PgBouncer-style poolers reject unknown startup params (UNVERIFIED for each pooler). | pgx `context` cancellation |
| 6 Size | Stop at `--max-rows` rows or `--max-bytes` of JSON. `PgConn().Frontend().SetMaxBodyLen(--max-bytes)` caps one row's message, so a single huge value can't exhaust memory. | pgproto3 `Frontend.SetMaxBodyLen` (v5.12.0) |
| 7 Explain | SQL sent as `EXPLAIN (ANALYZE <bool>) <sql>`. Because the option list is already closed, a smuggled `ANALYZE ...` is a syntax error. `analyze:true` needs `--allow-explain-analyze`. | PG `EXPLAIN` grammar |
| 8 Role (docs) | Docs page: create a role with `pg_read_all_data` (PG 14+) or explicit `SELECT` grants, and `ALTER ROLE ... SET default_transaction_read_only = on`. Every call also checks if the current role is a superuser **or a member of one** (`pg_has_role(current_user, oid, 'MEMBER')`, same round trip as `set_config`) and refuses unless `--allow-superuser`. Why: `lo_export(<existing large object>, '/path')` writes a server file inside a READ ONLY transaction (verified on PG 14, 17, 18), and a member can `set_config('role', ...)` mid-statement. | tested 2026-11-01 |

Bypass suite (`bypass_test.go`, table-driven; each row is one test against real Postgres, with a `canary` table checked after every row):

| # | Tool / payload | Expected |
|---|---|---|
| 1 | `COMMIT; DROP SCHEMA public CASCADE;` | Rejected at Parse (multiple commands). Canary intact. |
| 2 | `SELECT 1; COMMIT; DROP TABLE canary;` | Same as 1. |
| 3 | `COMMIT; SET statement_timeout TO 1;` | Same as 1. Next call runs normally. |
| 4 | `COMMIT` | Rejected by layer 2, never executed. |
| 5 | `SET statement_timeout TO 1` / `SET TRANSACTION READ WRITE` | Rejected by layer 2. |
| 6 | `DELETE FROM canary RETURNING *` | Executes, fails: read-only transaction (SQLSTATE 25006). |
| 7 | `WITH d AS (DELETE FROM canary RETURNING *) SELECT * FROM d` | 25006. |
| 8 | `SELECT set_config('transaction_read_only','off',true)` | Error, still read-only (exact SQLSTATE pinned by the test). |
| 9 | `DO $$BEGIN DELETE FROM canary; END$$` / `CALL p()` | Rejected by layer 2 (a `CALL` with OUT params would run and then hit 25006). |
| 10 | `COPY (SELECT 1) TO PROGRAM 'touch /tmp/pwn'` (as superuser) | Rejected by layer 2; file not created. |
| 11 | `PREPARE x AS DELETE FROM canary` | Rejected by layer 2. |
| 12 | `SELECT nextval('s')` | 25006. |
| 13 | `SELECT pg_sleep(600)` | Cancelled at `--timeout` (57014). |
| 14 | `SELECT set_config('statement_timeout','0',true), pg_sleep(600)` | Still 57014: the timer is armed at statement start, so changing it mid-statement does nothing. The client deadline (`--timeout` + 1s) stays as the backstop for what SQL can't reach (slow connect, hung network). |
| 15 | `SELECT pg_advisory_lock(1)` | Runs. The next call can take the same lock (session gone). |
| 16 | `SELECT generate_series(1,10000000)` | First `--max-rows` rows plus a truncation note. Returns before the timeout. |
| 17 | `SELECT repeat('x', 300000000)` | Error: row over `--max-bytes`. Memory stays bounded. |
| 18 | `""`, missing `sql`, `sql: 5` | Tool error, no DB round trip. |
| 19 | `explain` with `ANALYZE SELECT pg_sleep(5)` | Syntax error, nothing runs. |
| 20 | `explain` with `SELECT 1; DELETE FROM canary` | Rejected at Parse. |
| 21 | `explain` with `analyze:true`, flag off | Tool error before any DB call. |
| 22 | Resource URI `postgres://h/x';DROP TABLE canary;--/schema` | Bind parameter, returns `[]`. |
| 23 | `describe_table` with `table: "canary; DROP TABLE canary"` | Bind parameter, "not found". |
| 24 | `SELECT dblink_exec('dbname=app','DROP TABLE canary')` | **Not blocked by this server.** It's a separate session; only role privileges stop it. Test asserts it fails for the `mcp_ro` role. |
| 25 | `SELECT pg_terminate_backend(<pid>)` | **Not blocked by this server.** Fails for `mcp_ro`; documented. |

| x1 | `SELECT lo_from_bytea(0, 'x')` | PG 14: runs (15+: 25006). Rolled back either way; no large object survives. |
| x2 | `SELECT pg_read_file('/etc/hostname')` | Fails for `mcp_ro` (42501). Works for a superuser: read-only stops writes, not reads. |
| x3 | `SELECT pg_ls_dir('.')` | Same as x2. |
| x4 | `SELECT lo_export(<oid>, '/tmp/x')` | Fails for `mcp_ro` (42501). **Writes the file for a superuser even in READ ONLY**, hence superuser refusal. |

Rows 24-25 and x2-x4 are why layer 8 exists and why the README must not claim more than "read-only at the transaction level".

## 7. Configuration

Flags only, all before the URL. No env vars, no config file.

| Flag | Default | Note |
|---|---|---|
| `--timeout` | `30s` | `time.Duration`. Server `statement_timeout` and client deadline. |
| `--max-rows` | `1000` | Rows returned per call. |
| `--max-bytes` | `262144` (256 KiB) | JSON text size per call, and max size of one row message. |
| `--allow-explain-analyze` | `false` | Lets `explain` take `analyze: true`. |
| `--allow-superuser` | `false` | Allows a superuser role (or a member of one). Off: every call is refused with a fix-it message. On: one stderr warning. |

Setting a limit to 0 is an error, not "off" (scope: none of them off). New tools' schemas:
`list_tables {schema?: string}`, `describe_table {table: string (required), schema?: string = "public"}`,
`explain {sql: string (required), analyze?: boolean}`.

## 8. Testing

- **Integration:** `go test ./...` against a real Postgres. CI uses a GitHub Actions `services: postgres` container,
  run on the oldest and newest supported Postgres majors. Locally: `docker run postgres` and `DATABASE_URL`. No
  testcontainers dependency. Fixture SQL creates `canary`, a `mcp_ro` role, a sequence and mixed column types.
- **Bypass suite:** the section 6 table, one subtest per row. A new bypass report becomes a new row before the fix.
- **Compatibility:** `make golden` (run by hand when fixtures change) starts the pinned
  `@modelcontextprotocol/server-postgres@0.6.2` against the fixture DB through the go-sdk client
  (`mcp.CommandTransport`), and records `tools/list`, `resources/list`, `resources/read` and a set of `query` calls to
  `testdata/golden/`. The CI test replays the same calls against our binary. `tools/list[query]` and resources must
  match exactly; query outputs must match after applying the documented differences in section 5.
- **Packaging smoke:** in CI, `npx -y ./npm/<pkg>-<ver>.tgz <url>` and `docker run -i`, then one `tools/call query`.

## 9. Distribution

| Channel | How |
|---|---|
| GitHub releases | GoReleaser (OSS): `CGO_ENABLED=0`, linux/darwin amd64+arm64, windows amd64; archives and `checksums.txt`. |
| npm | esbuild's pattern: main package `<pkg>` with a ~20-line JS shim as `bin`, plus `optionalDependencies` on `<pkg>-linux-x64`, `-linux-arm64`, `-darwin-x64`, `-darwin-arm64`, `-win32-x64`. Each has `os`/`cpu` fields and holds one binary. The shim resolves the right package and `spawn`s it with `stdio: "inherit"`, passing through args and the exit code. No `postinstall`, so it works with `--ignore-scripts`, Bun and offline caches. A CI script publishes the platform packages first and the main package last (publishes are not atomic), with `--provenance`. `package.json` carries `mcpName`. |
| Docker | `gcr.io/distroless/static` base (has CA certs), non-root, `ENTRYPOINT ["/<pkg>"]`, `LABEL io.modelcontextprotocol.server.name="io.github.<user>/<pkg>"`. |
| MCP registry | `server.json` with an `npm` package (`transport: stdio`, one `packageArguments` entry `type: positional`, `isRequired: true`, `valueHint: postgresql_url`) and an `oci` package. Then `mcp-publisher login github` and `mcp-publisher publish`. With GitHub auth the name must start with `io.github.<user>/`. |
| Glama | Indexes GitHub repos. Add `glama.json` with `maintainers: ["<user>"]` at the repo root, then run its claim flow. |
| Old users | Comment on issue #4785 with the one-line config change; README section "Migrating from server-postgres". |

## 10. Open questions and risks

| # | Item | Recommendation |
|---|---|---|
| Q1 | Package name and npm scope. Blocks the npm, registry and Docker names. | Pick an unscoped, searchable name with "postgres" and "mcp" in it. |
| Q2 | Superuser connections: warn (drop-in friendly) or refuse (safer)? | **Decided 2026-11-01: refuse by default, `--allow-superuser` to opt in.** The `lo_export` file write (layer 8) is a real bypass in the default setup. Cost: the default Docker `postgres` user needs the flag or a role. |
| Q3 | Connect-per-call cost on remote TLS databases. | **Measured 2026-11-01** (PG 17, TLS 1.3, SCRAM, toxiproxy at 50 ms RTT): connect 321 ms (~6 RTT), full `query` call 583 ms; 533 ms with `sslnegotiation=direct`. Local: 14 ms. **Decision: keep a fresh connection per call.** Model time per tool call is seconds, so 0.5 s doesn't matter, and reuse weakens layer 4 (`DISCARD ALL` doesn't close `dblink` connections, for one). If it ever matters: pipeline `BEGIN` + setup + `Parse` into one round trip (~2 RTT saved, no safety cost). |
| Q4 | Defaults of 30s / 1000 rows / 256 KiB are judgement calls. | Keep; revisit after real usage. |
| Q5 | Should the connection URL also be readable from an env var, to keep passwords out of argv? | Not in v1 (nobody asked). Easy to add later. |
| R1 | Dynamic `resources/list` uses go-sdk receiving middleware, which is documented for auth and logging, not for overriding a method. | Confirm it works in a day-1 spike. Fallback: list tables at startup and register them as static resources. |
| R2 | `EXPLAIN` of SQL with `$1` placeholders fails (no bind values). | Document. `GENERIC_PLAN` (PG 16+) is a later option. |
| R3 | Protocol 2026-07-28 drops the `initialize` handshake. We depend on go-sdk handling both eras. | Golden tests use a 2024-11-05 client; add one 2026-07-28 client test. |
| R4 | Crowded space (Zed fork, crystaldba, Rust rebuild, pg-guard-mcp). | The bypass suite is the pitch. Publish it in the README as a table. |

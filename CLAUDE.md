# pg-mcp

A safe, drop-in replacement for the deprecated `@modelcontextprotocol/server-postgres@0.6.2`.
Go binary, MCP over stdio, read-only enforced by Postgres itself.

- `SCOPE.md`: what v1 does and won't do. Don't widen it; raise scope changes in chat.
- `SPEC.md`: stack, architecture, compat contract, safety layers, bypass suite. It's the source of
  truth. When code and spec disagree, stop and ask which one is wrong.

Status (2026-11-01): v1 core built and tested (PG 14, 17, 18). Name: `postgres-mcp-safe`, module
`github.com/Poudel0/postgres-mcp-safe`. Packaging built and tested locally; nothing published yet.

## Releasing

Push a `vX.Y.Z` tag; `.github/workflows/release.yml` runs tests, GoReleaser, npm, ghcr.io and the MCP
registry, in that order (the registry checks the npm package and image exist). Needs repo secret
`NPM_TOKEN`. Local dry run: `go run github.com/goreleaser/goreleaser/v2@v2.19.0 release --snapshot --clean`
then `node npm/build.mjs 0.0.0-snapshot`. Versions in `server.json` and `npm/*/package.json` are
placeholders stamped at release; don't bump them by hand.

## Stack

Go 1.27, `github.com/jackc/pgx/v5` v5.12.0, `github.com/modelcontextprotocol/go-sdk` v1.8.0.
Don't add dependencies without asking. No testcontainers, no config libs, no SQL parser.

## Layout

One `main` package, four files, no interfaces (one DB, one transport):
`main.go` (flags, URL, run), `db.go` (`readOnly`, `userQuery`, `fixedQuery`), `tools.go`, `resources.go`.
A fifth file needs a reason.

## Safety rules (never break these)

These are the product. Each one maps to a SPEC §6 layer and a bypass test.

1. User SQL only reaches Postgres through `tx.Prepare` with a **non-empty** statement name.
   Never pass user SQL to `Exec`, or to `Query` as raw text: pgx falls back to the simple
   protocol, which allows many statements (the original `COMMIT; ...` bug).
2. Reject empty or missing `sql` at the boundary, before any DB call.
3. After Prepare, if `StatementDescription.Fields` is empty, reject. Never execute it.
4. Every DB touch goes through `readOnly(ctx, fn)`: fresh connection, `BEGIN READ ONLY`, closed
   in `defer` (closing ends the transaction). No pool. Untrusted SQL only via `userQuery`.
5. Time limit is both server-side (`statement_timeout`) and a client deadline 1s later. Size limit
   is both a row/byte cap while reading and `MaxProtocolMessageBodyLen` per row message.
6. `explain` builds `EXPLAIN (ANALYZE <bool>) <sql>`. `analyze:true` needs `--allow-explain-analyze`.
7. Our own SQL (schema tools, resources) uses bind parameters. Never string-build identifiers.
8. Superusers and members of superuser roles are refused unless `--allow-superuser`. `lo_export`
   writes server files even in a READ ONLY transaction, so this check is not optional.
9. Don't claim more than "read-only at the transaction level" anywhere (see bypass rows 24-25, x2-x4).

## Compatibility

- `query` tool name, description and `inputSchema` stay byte-identical to SPEC F3.
- Resource URIs, names and shapes match F6-F8.
- Only the five differences in SPEC §5 are allowed, and each is listed in the README.
  A new difference means updating SPEC §5 first.

## Conventions

- Errors are values: wrap with `fmt.Errorf("step: %w", err)`. Tool failures become
  `isError:true` results carrying the Postgres message and SQLSTATE.
- Logs go to stderr only. Never log the password or full URL.
- Config is flags only, placed before the URL. A limit of 0 is an error, not "off".

## Testing

- `make test` against real Postgres via `DATABASE_URL` (a **superuser** URL on a throwaway DB: the
  fixture resets `public` and creates roles). Local container: `pg-mcp-test` on 127.0.0.1:5434
  (`postgres`/`postgres`). Don't point tests at `velapays-pg`: that's another project's database.
- Values are Postgres text output mapped to JSON (SPEC §5.4), not pgx Go types.
- Bypass suite (`bypass_test.go`) is table-driven, one row per SPEC §6 payload, with a
  `canary` check after each. A new bypass report becomes a failing row **before** the fix.
- Golden compat tests: `make golden` records the old server's output into `testdata/golden/`
  (needs npx). It's run by hand; CI only replays.
- Don't call a change done until `go test ./...` passes against a live database.

## Known quirks

- go-sdk drops in-flight responses when stdin closes, so `printf ... | binary` loses answers. Hold
  stdin open (`(printf ...; sleep 1) | ...`) when poking it by hand. Real clients are fine.
- go-sdk adds `ttlMs`/`cacheScope` to list results even on protocol 2024-11-05. Harmless extra fields.

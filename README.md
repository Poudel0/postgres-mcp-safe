# postgres-mcp-safe

A drop-in replacement for the deprecated `@modelcontextprotocol/server-postgres`, but actually read-only.

Lets AI tools (Claude, Cursor, Zed, ...) query a Postgres database, read its schema and explain queries. Swap the
package name in your MCP config and keep everything else.

[![npm](https://img.shields.io/npm/v/postgres-mcp-safe)](https://www.npmjs.com/package/postgres-mcp-safe) [![ci](https://github.com/Poudel0/postgres-mcp-safe/actions/workflows/ci.yml/badge.svg)](https://github.com/Poudel0/postgres-mcp-safe/actions/workflows/ci.yml)

[![Postgres (read-only, safe) MCP server](https://mcprush.com/badge/postgres-read-only-safe-mcp/card.svg)](https://mcprush.com/to/postgres-read-only-safe-mcp?ref=badge)

## Migrating from server-postgres

```diff
 {
   "mcpServers": {
     "postgres": {
       "command": "npx",
-      "args": ["-y", "@modelcontextprotocol/server-postgres", "postgresql://localhost/mydb"]
+      "args": ["-y", "postgres-mcp-safe", "postgresql://localhost/mydb"]
     }
   }
 }
```

Docker: `docker run -i --rm ghcr.io/poudel0/postgres-mcp-safe postgresql://host.docker.internal:5432/mydb`.
Binaries for Linux, macOS and Windows are on the [releases page](https://github.com/Poudel0/postgres-mcp-safe/releases).

**One catch:** if your URL logs in as a superuser (like Docker's default `postgres` user), every call is refused
until you either [create a read-only role](#use-a-read-only-role-too) (recommended) or add `--allow-superuser`
before the URL. A superuser can get around a read-only transaction, so we won't pretend otherwise.

The old server's `query` tool and `postgres://.../<table>/schema` resources work the same. See
[Differences](#differences-from-server-postgres) for the small, deliberate changes.

## Why

The old server's "read-only" mode can be broken with `COMMIT; DROP TABLE ...`
([Datadog Security Labs, 2025-08-21](https://securitylabs.datadoghq.com/articles/mcp-vulnerability-case-study-SQL-injection-in-the-postgresql-mcp-server/)).
A fix was merged upstream but never published, and the package is deprecated with no official successor.

## Flags

Flags go **before** the URL.

| Flag | Default | |
|---|---|---|
| `--timeout` | `30s` | Time limit per call. |
| `--max-rows` | `1000` | Rows returned per call. Extra rows are cut off with a note. |
| `--max-bytes` | `262144` | JSON bytes returned per call, and the max size of a single row. |
| `--allow-explain-analyze` | off | Lets `explain` run `EXPLAIN ANALYZE`, which executes the query. |
| `--allow-superuser` | off | Allows a superuser role, or a member of one. Read-only is then **not** guaranteed. |

Each call opens a fresh connection (that's part of the safety model). Against a remote database over TLS that costs a
few round trips: about 0.5 s per call at 50 ms latency. On Postgres 17+ you can save one round trip by adding
`sslnegotiation=direct` to the URL.

Limits can't be turned off; `0` is an error. The URL can also be a keyword DSN (`host=db dbname=app`), and the
standard `PG*` environment variables (`PGPASSWORD`, ...) are read as libpq would.

## Tools

| Tool | Input | |
|---|---|---|
| `query` | `sql` | One read-only statement. Returns a JSON array of rows. |
| `list_tables` | `schema?` | Tables and views with schema and type. |
| `describe_table` | `table`, `schema?` (`public`) | Columns, constraints and indexes. |
| `explain` | `sql`, `analyze?` | Query plan. `analyze` needs `--allow-explain-analyze`. |

## How read-only is enforced

Postgres enforces it, not a SQL parser. Every call:

1. Opens a **fresh connection** (no session state survives a call) and starts a `READ ONLY` transaction.
2. Sends your SQL as a single protocol-level *Parse* message. Postgres rejects more than one statement there, so
   `COMMIT; ...` fails before anything runs.
3. Runs it only if Postgres says it returns rows. `COMMIT`, `SET`, `DO`, `COPY`, `CALL`, `PREPARE` and friends are
   rejected without running.
4. Applies `statement_timeout`, a client deadline, and row and byte caps.
5. Closes the connection, which ends the transaction.

The [bypass suite](bypass_test.go) runs every known payload against a real database and checks a canary table after
each one:

| Payload | Result |
|---|---|
| `COMMIT; DROP SCHEMA public CASCADE;` | Rejected: multiple statements |
| `COMMIT` / `SET TRANSACTION READ WRITE` / `DO $$...$$` / `CALL p()` | Rejected: returns no rows |
| `COPY (SELECT 1) TO PROGRAM '...'` (as superuser) | Rejected: returns no rows |
| `DELETE ... RETURNING *` / `WITH d AS (DELETE ...) SELECT ...` | Read-only transaction error |
| `SELECT nextval('s')` | Read-only transaction error |
| `SELECT set_config('transaction_read_only','off',true)` | Error |
| `SELECT pg_sleep(600)`, even after `set_config('statement_timeout','0')` | Cancelled at `--timeout` |
| `SELECT pg_advisory_lock(1)` | Released when the call ends |
| `SELECT generate_series(1,10000000)` / `repeat('x', 10000000)` | Capped / rejected, memory stays bounded |
| `explain` with `ANALYZE SELECT ...` | Syntax error: can't smuggle `ANALYZE` |

Found a bypass? Open an issue. It becomes a test row before the fix.

## Use a read-only role too

This server makes each call read-only **at the transaction level**. Some things are out of a transaction's reach,
and only the database role stops them:

- `dblink_exec(...)` opens a *separate* session that isn't read-only.
- `pg_terminate_backend(...)` kills other connections.
- A superuser can read server files (`pg_read_file`, `pg_ls_dir`): read-only stops writes, not reads.
- A superuser can **write** server files with `lo_export`, even inside a read-only transaction.

So connect as a role that can only read:

```sql
CREATE ROLE mcp_ro LOGIN PASSWORD '...';
GRANT pg_read_all_data TO mcp_ro;                     -- PG 14+; or GRANT SELECT on what it needs
ALTER ROLE mcp_ro SET default_transaction_read_only = on;
```

Because of the last two, the server refuses superuser roles (and members of them) unless you pass `--allow-superuser`.

## Differences from server-postgres

1. Errors in a tool call come back as a tool result with `isError: true` and the Postgres message and SQLSTATE, so
   the model can fix its SQL. The old server sent a JSON-RPC error.
2. `resources/read` only shows columns of the `public` table, in column order. The old one mixed in columns from
   same-named tables in other schemas.
3. Percent-encoded table names in resource URIs are decoded (the old server never matched them).
4. Values are what Postgres prints, typed where it's clear: booleans, numbers (including `bigint` and `numeric`)
   and `json` are JSON values; everything else is a string such as `"2026-01-02 03:04:05+00"`, `"\\x0102"` or
   `"{x,y}"`. `NaN` is `"NaN"` (the old server gave `null`).
5. Statements that return no rows (`SET`, `DO`, `COPY`, ...) are rejected. The old server ran them and returned `[]`.

## Development

```sh
docker run -d --name pg-mcp-test -e POSTGRES_PASSWORD=postgres -p 127.0.0.1:5434:5432 postgres:17-alpine
export DATABASE_URL='postgresql://postgres:postgres@127.0.0.1:5434/postgres?sslmode=disable'
make test     # all tests, including the bypass suite
make golden   # re-record the old server's output (needs npx)
```

`DATABASE_URL` must be a superuser: the fixture resets the `public` schema and creates the `mcp_ro` role. Use a
throwaway database.

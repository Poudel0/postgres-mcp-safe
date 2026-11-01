# Safe Postgres MCP server — v1 scope

Working name: TBD.

## What and who

A small program that lets AI coding tools (Claude, Cursor, etc.) look at a Postgres database:
run read-only queries, see the schema, explain queries. For the developers still using the
deprecated official server.

## Proof people use it (checked 2026-11-01)

| Fact | Number | Source |
|---|---|---|
| Weekly npm downloads, deprecated `@modelcontextprotocol/server-postgres` | 179,957 (Oct 23–29, 2026) | api.npmjs.org |
| Its "read-only" mode can be broken | `COMMIT; ...` ends the read-only transaction | Datadog Security Labs case study |
| Best-known replacement is shaky | crystaldba/postgres-mcp: 3,447 stars; "0.3.0 unusable" issue, 14 👍; last push 2026-08-17 | GitHub #237 |
| Others moving in | Rust rebuild (dev.to), pg-guard-mcp (PyPI), Zed's patched fork | search, 2026-11-01 |

**Honest read:** demand is proven, but the space is filling up. We win only by being the
easiest switch *and* actually safe.

## The one promise

**Swap the package name in your config, keep everything else, and it really is read-only.**

## v1 does

1. **Drop-in for the old server.** Same install shape (`npx -y <ours> postgresql://...`),
   same tool name (`query`), same schema resources. Existing MCP configs work after changing
   one word.
2. **Really read-only.** One statement per call, enforced by the database protocol, not by
   checking the text. Runs inside a read-only transaction. A test suite of known bypass
   payloads ships with it.
3. **Limits on by default.** Query timeout, row cap and result size cap. All configurable,
   none of them off by default.
4. **Schema tools.** List schemas and tables, describe a table (columns, types, keys,
   indexes).
5. **Explain tool.** Plain `EXPLAIN` only. `EXPLAIN ANALYZE` actually runs the query, so it's
   off unless turned on.
6. **Install any way people want.** npm (`npx`), Docker, a single binary from GitHub releases.
7. **Docs page:** "Use a read-only Postgres role too." The database's own permissions are the
   last line of defence.

## v1 won't

- Writes of any kind. Maybe opt-in later, if people ask.
- Other databases (MySQL, SQLite).
- A hosted version, a GUI, or AI features of its own.
- Multi-database or tenant routing.

## How people find it

- MCP directories (the official MCP registry, Glama, Smithery). **Check how each one lists
  servers.**
- npm search for "postgres mcp".
- The open issue on the servers repo about the archived servers (#4785), and the deprecated
  package's own users searching for "server-postgres replacement".

## "Used" at 90 days (judgement calls)

| Signal | Target | Why it matters |
|---|---|---|
| Weekly npm downloads | 2,000 | About 1% of the stranded users; real daily use |
| GitHub stars | 500 | Directory ranking |
| Bypass reports that hold up | 0 | The whole pitch is safety |

## Risks

- **Crowded.** Several rebuilds already exist. If one becomes the default, we're one of many.
- **The MCP project points users elsewhere.** Check whether the archived server's README
  already names a recommended replacement. If it does, that changes the plan.
- **MCP spec changes** need keeping up with.

## Check before building (done 2026-11-01, see SPEC §2)

- Old server's tools and resources: SPEC F1-F15, pinned by golden tests.
- Recommended successor: none (F13).
- Datadog article date: 2025-08-21 (F11).

# Launch copy — postgres-mcp-safe

Draft, 2026-11-17. Not committed unless you want it. Honest framing only: the
angle is **safe by default**, not "I found a vuln in Microsoft's server."

The one true claim to lead with:
> Most Postgres MCP servers are only as safe as the database role you hand them.
> The role people actually paste is the `postgres` superuser — and against that,
> a "read-only" MCP server can still be bypassed (e.g. `dblink`). postgres-mcp-safe
> refuses superuser roles by default, so the careless case is safe too.

Measured 2026-11-17 against `@microsoft/postgres-mcp@0.3.0` on a local PG: as the
`postgres` superuser, its read-only `postgres_mcp_query` ran `dblink_exec(...)` to
DELETE a row and `pg_read_file('/etc/hostname')` to read a server file. With a real
read-only role, both were blocked. That matches Microsoft's own documented model
("your database decides"); it is not a secret hole, so **do not** pitch it as one.

---

## 1. Show HN

**Title:** Show HN: postgres-mcp-safe – a read-only Postgres MCP server that's safe by default

**Body:**
The official `@modelcontextprotocol/server-postgres` is deprecated, and its
"read-only" mode could be broken with `COMMIT; DROP TABLE ...` (Datadog Security
Labs wrote it up). It still gets ~150k downloads a week with no official successor.

postgres-mcp-safe is a drop-in replacement in Go. You change one word in your MCP
config and keep everything else — same `query` tool, same schema resources.

What's different:
- Read-only is enforced by Postgres (one prepared statement per call, inside a
  `BEGIN READ ONLY` transaction), not by string-matching the SQL.
- It ships a test suite of known bypass payloads (`COMMIT; ...`, CTE deletes,
  `COPY ... TO PROGRAM`, `lo_export`, etc.) that must stay red before a fix.
- Safe by default: it refuses superuser connections unless you pass
  `--allow-superuser`. A superuser can escape any read-only transaction (via
  `dblink`, `lo_export`, server-side file reads), so the common "just paste the
  `postgres` user" setup isn't actually read-only. This closes that.

It's one static binary (also npm and Docker). Not a hosted service, no telemetry.

GitHub: https://github.com/Poudel0/postgres-mcp-safe

Honest about scope: reads only, Postgres only, and the space is crowded (Microsoft,
Supabase, Neon, crystaldba all ship one). The thing I care about is being the one
that's safe when you're careless about roles.

---

## 2. r/PostgreSQL / r/mcp

**Title:** I made a read-only Postgres MCP server that refuses superuser roles by default

Short version of the Show HN. Lead with the Datadog bypass, the deprecation of the
official server, and the "safe by default" point. Link the bypass test file
directly: `bypass_test.go`. Ask: "what bypass would you try?" — invites people to
add rows, which is free hardening and engagement.

---

## 3. awesome-mcp-servers PR entry

Repo: https://github.com/punkpeye/awesome-mcp-servers (and wong2's fork)

Add under the Postgres / databases section:
> - [Poudel0/postgres-mcp-safe](https://github.com/Poudel0/postgres-mcp-safe) 🏎️ 🏠 –
>   Read-only Postgres MCP server, drop-in for the deprecated server-postgres.
>   Enforced by Postgres, ships a bypass test suite, refuses superuser roles by default.

(Check their legend for the right emoji: Go/fast, local/self-hosted.)

---

## 4. Comment on modelcontextprotocol/servers#4785

You already posted there on 2026-11-01. A short follow-up is fair once there's news
(a real release tag, or the safe-by-default result), not a repeat. Keep it to:
"Follow-up: tagged v0.1.x; added a documented-bypass test suite and superuser refusal
by default. Repo: ..." One line. Don't spam the thread.

---

## 5. Zed / Cursor / Claude Desktop directories

- Submit to Glama (`glama.json` already in the repo — check it lists correctly).
- MCP registry: already listed as `io.github.Poudel0/postgres-mcp-safe` — confirm the
  new description shows after the next release.
- Zed extensions and Cursor's MCP directory: check each one's submission process
  (SCOPE.md note "check how each one lists servers" still applies).

---

## What NOT to do
- Don't claim Microsoft's (or anyone's) server is "vulnerable." It's their documented
  model. You'll lose credibility and pick a fight you don't need.
- Don't promise "100% safe" — the README already caps the claim at "read-only at the
  transaction level." Keep every post inside that cap.

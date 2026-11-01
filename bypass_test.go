package main

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// TestBypass is SPEC §6, one row per payload. Rows run as superuser with
// --allow-superuser (the worst case) unless asRO is set. A new bypass report becomes a row here before the
// fix. After every row the canary must be intact.
func TestBypass(t *testing.T) {
	ctx := context.Background()
	watch, err := pgx.Connect(ctx, superURL) // also the backend row 25 tries to kill
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close(ctx)

	super := connect(t, superURL, serverOpts{allowSuperuser: true})
	ro := connect(t, roURL, serverOpts{})

	tests := []struct {
		row  string
		tool string
		args map[string]any
		asRO bool
		want string // substring of the output; "" means the call must succeed
	}{
		{"1", "query", sql("COMMIT; DROP SCHEMA public CASCADE;"), false, "42601"},
		{"2", "query", sql("SELECT 1; COMMIT; DROP TABLE canary;"), false, "42601"},
		{"3", "query", sql("COMMIT; SET statement_timeout TO 1;"), false, "42601"},
		{"4", "query", sql("COMMIT"), false, "only statements that return rows"},
		{"5a", "query", sql("SET statement_timeout TO 1"), false, "only statements that return rows"},
		{"5b", "query", sql("SET TRANSACTION READ WRITE"), false, "only statements that return rows"},
		{"6", "query", sql("DELETE FROM canary RETURNING *"), false, "25006"},
		{"7", "query", sql("WITH d AS (DELETE FROM canary RETURNING *) SELECT * FROM d"), false, "25006"},
		{"8", "query", sql("SELECT set_config('transaction_read_only','off',true)"), false, "25001"},
		{"9a", "query", sql("DO $$BEGIN DELETE FROM canary; END$$"), false, "only statements that return rows"},
		{"9b", "query", sql("CALL p()"), false, "only statements that return rows"},
		{"10", "query", sql("COPY (SELECT 1) TO PROGRAM 'touch /tmp/pwn'"), false, "only statements that return rows"},
		{"11", "query", sql("PREPARE x AS DELETE FROM canary"), false, "only statements that return rows"},
		{"12", "query", sql("SELECT nextval('s')"), false, "25006"},
		{"13", "query", sql("SELECT pg_sleep(600)"), false, "57014"},
		{"14", "query", sql("SELECT set_config('statement_timeout','0',true), pg_sleep(600)"), false, "57014"},
		{"16", "query", sql("SELECT generate_series(1,10000000)"), false, "--max-rows"},
		{"17", "query", sql("SELECT repeat('x', 10000000)"), false, "invalid body length"},
		{"18a", "query", sql(""), false, "sql is required"},
		{"18b", "query", map[string]any{}, false, "sql is required"},
		{"18c", "query", map[string]any{"sql": 5}, false, "invalid arguments"},
		{"19", "explain", sql("ANALYZE SELECT pg_sleep(5)"), false, "42601"},
		{"20", "explain", sql("SELECT 1; DELETE FROM canary"), false, "42601"},
		{"21", "explain", map[string]any{"sql": "SELECT 1", "analyze": true}, false, "analyze is off"},
		{"23", "describe_table", map[string]any{"table": "canary; DROP TABLE canary"}, false, "not found"},
		{"24", "query", sql("SELECT dblink_exec('dbname=postgres', 'DROP TABLE canary')"), true, "password"},
		{"25", "query", sql("SELECT pg_terminate_backend(" + pid(t, watch) + ")"), true, "42501"},
		// Not in SPEC §6. A superuser can read server files: read-only stops
		// writes, not reads. Only the role (layer 8) stops this.
		// PG 14 allows this in a READ ONLY transaction (25006 from 15 on); the
		// rollback undoes it either way, checked after the loop.
		{"x1", "query", sql("SELECT lo_from_bytea(0, 'x')"), false, ""},
		{"x2", "query", sql("SELECT pg_read_file('/etc/hostname')"), true, "42501"},
		{"x3", "query", sql("SELECT pg_ls_dir('.')"), true, "42501"},
		// lo_export writes a server file even in a READ ONLY transaction (PG 14-18).
		// This is why superusers are refused unless --allow-superuser.
		{"x4", "query", sql("SELECT lo_export(0, '/tmp/pwn-lo')"), true, "42501"},
	}
	for _, tt := range tests {
		t.Run(tt.row, func(t *testing.T) {
			cs := super
			if tt.asRO {
				cs = ro
			}
			start := time.Now()
			out := call(t, cs, tt.tool, tt.args)
			if elapsed := time.Since(start); elapsed > testLimits.timeout+clientGrace+time.Second {
				t.Errorf("took %v, limit is %v", elapsed, testLimits.timeout)
			}
			if !strings.Contains(strings.Join(out.texts, "\n"), tt.want) {
				t.Errorf("got %s\nwant it to contain %q", out, tt.want)
			}
			assertCanary(t, watch)
		})
	}

	// Row x1: no large object survived.
	var los int
	if err := watch.QueryRow(ctx, "SELECT count(*) FROM pg_largeobject_metadata").Scan(&los); err != nil || los != 0 {
		t.Errorf("row x1: %d large objects exist, err=%v", los, err)
	}
	// Row 10: the COPY must never have run.
	var exists bool
	if err := watch.QueryRow(ctx, "SELECT pg_stat_file('/tmp/pwn', true) IS NOT NULL").Scan(&exists); err != nil || exists {
		t.Errorf("row 10: /tmp/pwn exists=%v err=%v", exists, err)
	}
}

// Row 3, second half: a rejected SET must not leak into the next call.
// Row 15: a session-level lock must not outlive its call.
func TestBypassSessionState(t *testing.T) {
	cs := connect(t, superURL, serverOpts{allowSuperuser: true})
	call(t, cs, "query", sql("COMMIT; SET statement_timeout TO 1;"))
	if out := call(t, cs, "query", sql("SELECT pg_sleep(0.1)")); out.isError {
		t.Errorf("row 3: next call failed: %s", out)
	}
	call(t, cs, "query", sql("SELECT pg_advisory_lock(1)"))
	rows := rowsOf(t, call(t, cs, "query", sql("SELECT pg_try_advisory_lock(1) AS got")))
	if rows[0]["got"] != true {
		t.Errorf("row 15: lock survived its session: %v", rows)
	}
}

// Row 22: the table name from a resource URI is a bind parameter.
func TestBypassResourceURI(t *testing.T) {
	cs := connect(t, superURL, serverOpts{allowSuperuser: true})
	res, err := cs.ReadResource(context.Background(), &mcp.ReadResourceParams{URI: "postgres://h/x';DROP TABLE canary;--/schema"})
	if err != nil {
		t.Fatal(err)
	}
	if got := res.Contents[0].Text; got != "[]" {
		t.Errorf("got %s, want []", got)
	}
	watch, err := pgx.Connect(context.Background(), superURL)
	if err != nil {
		t.Fatal(err)
	}
	defer watch.Close(context.Background())
	assertCanary(t, watch)
}

func sql(s string) map[string]any { return map[string]any{"sql": s} }

func pid(t *testing.T, conn *pgx.Conn) string {
	t.Helper()
	var p string
	if err := conn.QueryRow(context.Background(), "SELECT pg_backend_pid()::text").Scan(&p); err != nil {
		t.Fatal(err)
	}
	return p
}

func assertCanary(t *testing.T, watch *pgx.Conn) {
	t.Helper()
	var note string
	var n int
	if err := watch.QueryRow(context.Background(), "SELECT count(*), min(note) FROM canary").Scan(&n, &note); err != nil || n != 1 || note != "alive" {
		t.Fatalf("canary hurt: count=%d note=%q err=%v", n, note, err)
	}
}

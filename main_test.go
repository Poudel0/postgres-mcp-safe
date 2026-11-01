package main

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Tests run against a real Postgres. DATABASE_URL must be a superuser URL:
// the fixture creates the mcp_ro role and resets the public schema.
var (
	superURL string
	roURL    string
	binPath  string
)

func TestMain(m *testing.M) {
	superURL = os.Getenv("DATABASE_URL")
	if superURL == "" {
		fmt.Fprintln(os.Stderr, "DATABASE_URL is not set (e.g. postgresql://postgres:postgres@127.0.0.1:5434/postgres?sslmode=disable)")
		os.Exit(1)
	}
	u, err := url.Parse(superURL)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	u.User = url.UserPassword("mcp_ro", "mcp_ro")
	roURL = u.String()

	if err := loadFixture(); err != nil {
		fmt.Fprintln(os.Stderr, "fixture:", err)
		os.Exit(1)
	}
	dir, err := os.MkdirTemp("", "pgmcp")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	binPath = filepath.Join(dir, "postgres-mcp-safe")
	if out, err := exec.Command("go", "build", "-o", binPath, ".").CombinedOutput(); err != nil {
		fmt.Fprintf(os.Stderr, "build: %v\n%s", err, out)
		os.Exit(1)
	}
	code := m.Run()
	os.RemoveAll(dir)
	os.Exit(code)
}

func loadFixture() error {
	sql, err := os.ReadFile("testdata/fixture.sql")
	if err != nil {
		return err
	}
	ctx := context.Background()
	conn, err := pgx.Connect(ctx, superURL)
	if err != nil {
		return err
	}
	defer conn.Close(ctx)
	_, err = conn.Exec(ctx, string(sql)) // no args: simple protocol, many statements
	return err
}

var testLimits = limits{timeout: 2 * time.Second, maxRows: 100, maxBytes: 64 << 10}

type serverOpts struct {
	allowSuperuser, allowAnalyze bool
	latestProtocol               bool // default: 2024-11-05, the old server's version
}

// connect starts an in-process server and a client for it.
func connect(t *testing.T, dbURL string, o serverOpts) *mcp.ClientSession {
	t.Helper()
	cfg, err := pgx.ParseConfig(dbURL)
	if err != nil {
		t.Fatal(err)
	}
	s := newServer(newDB(cfg, testLimits, o.allowSuperuser), resourceBase(dbURL, cfg), o.allowAnalyze)
	ct, st := mcp.NewInMemoryTransports()
	ctx := context.Background()
	if _, err := s.Connect(ctx, st, nil); err != nil {
		t.Fatal(err)
	}
	opts := &mcp.ClientSessionOptions{ProtocolVersion: "2024-11-05"}
	if o.latestProtocol {
		opts = nil
	}
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).Connect(ctx, ct, opts)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

type toolOutput struct {
	texts   []string
	isError bool
}

func (o toolOutput) String() string {
	return fmt.Sprintf("isError=%v %s", o.isError, strings.Join(o.texts, " | "))
}

func call(t *testing.T, cs *mcp.ClientSession, tool string, args any) toolOutput {
	t.Helper()
	res, err := cs.CallTool(context.Background(), &mcp.CallToolParams{Name: tool, Arguments: args})
	if err != nil {
		t.Fatalf("%s: protocol error: %v", tool, err)
	}
	out := toolOutput{isError: res.IsError}
	for _, c := range res.Content {
		out.texts = append(out.texts, c.(*mcp.TextContent).Text)
	}
	return out
}

func rowsOf(t *testing.T, o toolOutput) []map[string]any {
	t.Helper()
	if o.isError {
		t.Fatalf("unexpected tool error: %s", o)
	}
	var rows []map[string]any
	if err := json.Unmarshal([]byte(o.texts[0]), &rows); err != nil {
		t.Fatalf("not a JSON array of rows: %v\n%s", err, o.texts[0])
	}
	return rows
}

func TestCLI(t *testing.T) {
	tests := []struct {
		name    string
		args    []string
		want    string
		notWant string
	}{
		{"no url", nil, "Please provide a database URL as a command-line argument", ""},
		{"zero limit", []string{"--max-rows", "0", superURL}, "must be greater than zero", ""},
		{"bad url", []string{"postgresql://alice:hunter2@db:notaport/x"}, "cannot parse", "hunter2"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			out, err := exec.Command(binPath, tt.args...).CombinedOutput()
			if code := exitCode(err); code != 1 {
				t.Errorf("exit code = %d, want 1", code)
			}
			if !strings.Contains(string(out), tt.want) {
				t.Errorf("output %q does not contain %q", out, tt.want)
			}
			if tt.notWant != "" && strings.Contains(string(out), tt.notWant) {
				t.Errorf("output %q leaks %q", out, tt.notWant)
			}
		})
	}
}

func exitCode(err error) int {
	if ee, ok := err.(*exec.ExitError); ok {
		return ee.ExitCode()
	}
	if err != nil {
		return -1
	}
	return 0
}

// TestStdio runs the real binary the way MCP clients do.
func TestStdio(t *testing.T) {
	ctx := context.Background()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
		Connect(ctx, &mcp.CommandTransport{Command: exec.Command(binPath, roURL)}, &mcp.ClientSessionOptions{ProtocolVersion: "2024-11-05"})
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	if rows := rowsOf(t, call(t, cs, "query", map[string]any{"sql": "SELECT note FROM canary"})); len(rows) != 1 || rows[0]["note"] != "alive" {
		t.Errorf("rows = %v", rows)
	}
}

// TestLatestProtocol checks the 2026-07-28 (no initialize) era still works (SPEC R3).
func TestLatestProtocol(t *testing.T) {
	cs := connect(t, roURL, serverOpts{latestProtocol: true})
	if rows := rowsOf(t, call(t, cs, "query", sql("SELECT 1 AS one"))); len(rows) != 1 {
		t.Errorf("rows = %v", rows)
	}
}

func TestSuperuserRefused(t *testing.T) {
	member, err := url.Parse(roURL)
	if err != nil {
		t.Fatal(err)
	}
	member.User = url.UserPassword("mcp_member", "mcp_member")
	for _, dbURL := range []string{superURL, member.String()} {
		out := call(t, connect(t, dbURL, serverOpts{}), "query", sql("SELECT 1"))
		if !out.isError || !strings.Contains(out.texts[0], "refusing to run as a superuser") {
			t.Errorf("%s: got %s", dbURL, out)
		}
	}
}

func TestExplainAnalyzeAllowed(t *testing.T) {
	out := call(t, connect(t, roURL, serverOpts{allowAnalyze: true}), "explain", map[string]any{"sql": "SELECT 1", "analyze": true})
	if out.isError || !strings.Contains(out.texts[0], "actual time") {
		t.Errorf("got %s", out)
	}
}

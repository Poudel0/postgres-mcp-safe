package main

import (
	"context"
	"encoding/json"
	"flag"
	"maps"
	"os"
	"os/exec"
	"slices"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var update = flag.Bool("update", false, "record testdata/golden from @modelcontextprotocol/server-postgres@0.6.2 (needs npx)")

const goldenPath = "testdata/golden/server-postgres-0.6.2.json"

// Queries whose types have no documented formatting difference (SPEC §5.4)
// must match the old server byte for byte; the rest only in shape.
var (
	exactQueries = []string{
		"SELECT id, note FROM canary",
		"SELECT 1 AS a, 'x<&>' AS b, true AS c, NULL AS d",
		"SELECT * FROM canary WHERE false",
	}
	shapeQueries = []string{"SELECT * FROM users ORDER BY id", "SELECT 'NaN'::float8 AS e"}
)

type golden struct {
	QueryTool    *mcp.Tool
	Resources    []*mcp.Resource
	ReadUsers    string
	QueryOutputs map[string]string
}

func record(t *testing.T, cs *mcp.ClientSession) golden {
	t.Helper()
	ctx := context.Background()
	var g golden
	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	for _, tool := range tools.Tools {
		if tool.Name == "query" {
			g.QueryTool = tool
		}
	}
	res, err := cs.ListResources(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	g.Resources = res.Resources
	for _, r := range g.Resources {
		r.URI = strings.Replace(r.URI, baseURI(t), "postgres://BASE", 1)
	}
	slices.SortFunc(g.Resources, func(a, b *mcp.Resource) int { return strings.Compare(a.URI, b.URI) })
	read, err := cs.ReadResource(ctx, &mcp.ReadResourceParams{URI: baseURI(t) + "/users/schema"})
	if err != nil {
		t.Fatal(err)
	}
	g.ReadUsers = read.Contents[0].Text
	g.QueryOutputs = map[string]string{}
	for _, q := range append(exactQueries, shapeQueries...) {
		g.QueryOutputs[q] = call(t, cs, "query", sql(q)).texts[0]
	}
	return g
}

// baseURI is the resource URI prefix for the test DB, e.g. postgres://postgres@127.0.0.1:5434.
func baseURI(t *testing.T) string {
	cfg, err := pgx.ParseConfig(superURL)
	if err != nil {
		t.Fatal(err)
	}
	u := resourceBase(superURL, cfg)
	u.Path, u.RawQuery = "", ""
	return u.String()
}

func TestCompat(t *testing.T) {
	if *update {
		ctx := context.Background()
		cmd := exec.Command("npx", "-y", "@modelcontextprotocol/server-postgres@0.6.2", superURL)
		old, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "0"}, nil).
			Connect(ctx, &mcp.CommandTransport{Command: cmd}, &mcp.ClientSessionOptions{ProtocolVersion: "2024-11-05"})
		if err != nil {
			t.Fatal(err)
		}
		defer old.Close()
		b, err := json.MarshalIndent(record(t, old), "", "  ")
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(goldenPath, append(b, '\n'), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	b, err := os.ReadFile(goldenPath)
	if err != nil {
		t.Fatalf("%v (record it with: go test -run TestCompat -update)", err)
	}
	var want golden
	if err := json.Unmarshal(b, &want); err != nil {
		t.Fatal(err)
	}
	got := record(t, connect(t, superURL, serverOpts{allowSuperuser: true}))

	if g, w := got.QueryTool, want.QueryTool; g.Name != w.Name || g.Description != w.Description || compact(t, g.InputSchema) != compact(t, w.InputSchema) {
		t.Errorf("query tool:\n got %+v\nwant %+v", g, w)
	}
	if g, w := compact(t, got.Resources), compact(t, want.Resources); g != w {
		t.Errorf("resources/list:\n got %s\nwant %s", g, w)
	}
	// SPEC §5.2: the old read also returned other.users's columns, unordered.
	otherUsers := []map[string]any{{"column_name": "id", "data_type": "integer"}, {"column_name": "secret", "data_type": "text"}}
	if g, w := sortedRows(t, got.ReadUsers, otherUsers), sortedRows(t, want.ReadUsers, nil); g != w {
		t.Errorf("resources/read (plus other.users):\n got %s\nwant %s", g, w)
	}
	for _, q := range exactQueries {
		if got.QueryOutputs[q] != want.QueryOutputs[q] {
			t.Errorf("%s:\n got %s\nwant %s", q, got.QueryOutputs[q], want.QueryOutputs[q])
		}
	}
	for _, q := range shapeQueries {
		g, w := shape(t, got.QueryOutputs[q]), shape(t, want.QueryOutputs[q])
		if !slices.Equal(g, w) {
			t.Errorf("%s: shape\n got %v\nwant %v", q, g, w)
		}
	}
}

func compact(t *testing.T, v any) string {
	t.Helper()
	b, err := json.Marshal(v)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// sortedRows parses a JSON array of rows, adds extra, and returns them in a
// stable order for comparing as a multiset.
func sortedRows(t *testing.T, text string, extra []map[string]any) string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatal(err)
	}
	var keys []string
	for _, r := range append(rows, extra...) {
		keys = append(keys, compact(t, r))
	}
	slices.Sort(keys)
	return strings.Join(keys, "\n")
}

// shape is each row's column names. Order is checked by exactQueries.
func shape(t *testing.T, text string) []string {
	t.Helper()
	var rows []map[string]any
	if err := json.Unmarshal([]byte(text), &rows); err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, r := range rows {
		out = append(out, strings.Join(slices.Sorted(maps.Keys(r)), ","))
	}
	return out
}

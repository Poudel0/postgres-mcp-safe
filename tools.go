package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Byte-identical to @modelcontextprotocol/server-postgres@0.6.2 (SPEC F3).
const querySchema = `{"type":"object","properties":{"sql":{"type":"string"}}}`

const listTablesSQL = `
SELECT table_schema AS schema, table_name AS name, table_type AS type
FROM information_schema.tables
WHERE table_schema NOT IN ('pg_catalog', 'information_schema') AND ($1 = '' OR table_schema = $1)
ORDER BY 1, 2`

const describeTableSQL = `
SELECT
  COALESCE((SELECT json_agg(json_build_object(
      'name', a.attname,
      'type', format_type(a.atttypid, a.atttypmod),
      'nullable', NOT a.attnotnull,
      'default', pg_get_expr(d.adbin, d.adrelid)) ORDER BY a.attnum)
    FROM pg_attribute a
    LEFT JOIN pg_attrdef d ON d.adrelid = a.attrelid AND d.adnum = a.attnum
    WHERE a.attrelid = c.oid AND a.attnum > 0 AND NOT a.attisdropped), '[]') AS columns,
  COALESCE((SELECT json_agg(json_build_object(
      'name', k.conname,
      'definition', pg_get_constraintdef(k.oid)) ORDER BY k.conname)
    FROM pg_constraint k WHERE k.conrelid = c.oid), '[]') AS constraints,
  COALESCE((SELECT json_agg(json_build_object(
      'name', i.indexrelid::regclass::text,
      'definition', pg_get_indexdef(i.indexrelid)) ORDER BY i.indexrelid::regclass::text)
    FROM pg_index i WHERE i.indrelid = c.oid), '[]') AS indexes
FROM (SELECT to_regclass(format('%I.%I', $1::text, $2::text))::oid AS oid) c
WHERE c.oid IS NOT NULL`

func addTools(s *mcp.Server, d *db, allowAnalyze bool) {
	s.AddTool(&mcp.Tool{
		Name:        "query",
		Description: "Run a read-only SQL query",
		InputSchema: json.RawMessage(querySchema),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			SQL string `json:"sql"`
		}
		if err := decodeArgs(req, &in); err != nil {
			return errorResult(err), nil
		}
		return toolResult(d.userQuery(ctx, in.SQL))
	})

	s.AddTool(&mcp.Tool{
		Name:        "list_tables",
		Description: "List tables and views, with their schema and type.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"schema":{"type":"string","description":"Only list this schema. Default: all non-system schemas."}}}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			Schema string `json:"schema"`
		}
		if err := decodeArgs(req, &in); err != nil {
			return errorResult(err), nil
		}
		return toolResult(d.fixedQuery(ctx, listTablesSQL, in.Schema))
	})

	s.AddTool(&mcp.Tool{
		Name:        "describe_table",
		Description: "Describe a table or view: columns (name, type, nullable, default), constraints (primary, foreign and unique keys, checks) and indexes.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"table":{"type":"string"},"schema":{"type":"string","default":"public"}},"required":["table"]}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		in := struct {
			Table  string `json:"table"`
			Schema string `json:"schema"`
		}{Schema: "public"}
		if err := decodeArgs(req, &in); err != nil {
			return errorResult(err), nil
		}
		if in.Table == "" {
			return errorResult(errors.New("table is required")), nil
		}
		r, err := d.fixedQuery(ctx, describeTableSQL, in.Schema, in.Table)
		if err == nil && r.json == "[]" {
			err = fmt.Errorf("table %q not found in schema %q", in.Table, in.Schema)
		}
		return toolResult(r, err)
	})

	s.AddTool(&mcp.Tool{
		Name:        "explain",
		Description: "Show the query plan for one SQL statement. analyze: true runs the statement (inside a read-only transaction) and is only allowed if the server was started with --allow-explain-analyze.",
		InputSchema: json.RawMessage(`{"type":"object","properties":{"sql":{"type":"string"},"analyze":{"type":"boolean","default":false}},"required":["sql"]}`),
	}, func(ctx context.Context, req *mcp.CallToolRequest) (*mcp.CallToolResult, error) {
		var in struct {
			SQL     string `json:"sql"`
			Analyze bool   `json:"analyze"`
		}
		if err := decodeArgs(req, &in); err != nil {
			return errorResult(err), nil
		}
		if in.Analyze && !allowAnalyze {
			return errorResult(errors.New("analyze is off; start the server with --allow-explain-analyze to enable it")), nil
		}
		// The option list is closed before the user's SQL, so a smuggled
		// ANALYZE is a syntax error, not an option.
		return toolResult(d.userQuery(ctx, fmt.Sprintf("EXPLAIN (ANALYZE %t) %s", in.Analyze, in.SQL)))
	})
}

func decodeArgs(req *mcp.CallToolRequest, v any) error {
	if len(req.Params.Arguments) == 0 {
		return nil
	}
	if err := json.Unmarshal(req.Params.Arguments, v); err != nil {
		return fmt.Errorf("invalid arguments: %w", err)
	}
	return nil
}

func toolResult(r result, err error) (*mcp.CallToolResult, error) {
	if err != nil {
		return errorResult(err), nil
	}
	content := []mcp.Content{&mcp.TextContent{Text: r.json}}
	if r.truncated != "" {
		content = append(content, &mcp.TextContent{Text: r.truncated})
	}
	return &mcp.CallToolResult{Content: content}, nil
}

func errorResult(err error) *mcp.CallToolResult {
	return &mcp.CallToolResult{IsError: true, Content: []mcp.Content{&mcp.TextContent{Text: err.Error()}}}
}

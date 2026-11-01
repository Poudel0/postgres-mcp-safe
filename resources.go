package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/url"
	"strconv"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// resourcesMiddleware answers resources/list and resources/read itself,
// because the table list is read from the database on every call (SPEC F6-F8).
func resourcesMiddleware(d *db, base *url.URL) mcp.Middleware {
	return func(next mcp.MethodHandler) mcp.MethodHandler {
		return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
			switch method {
			case "resources/list":
				return listResources(ctx, d, base)
			case "resources/read":
				return readResource(ctx, d, req.(*mcp.ReadResourceRequest).Params.URI)
			}
			return next(ctx, method, req)
		}
	}
}

func listResources(ctx context.Context, d *db, base *url.URL) (*mcp.ListResourcesResult, error) {
	var tables []string
	err := d.readOnly(ctx, func(ctx context.Context, tx pgx.Tx) (err error) {
		rows, _ := tx.Query(ctx, "SELECT table_name FROM information_schema.tables WHERE table_schema = 'public'")
		tables, err = pgx.CollectRows(rows, pgx.RowTo[string])
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("list tables: %w", err)
	}
	resources := make([]*mcp.Resource, 0, len(tables))
	for _, t := range tables {
		resources = append(resources, &mcp.Resource{
			URI:      base.ResolveReference(&url.URL{Path: t + "/schema"}).String(),
			MIMEType: "application/json",
			Name:     `"` + t + `" database schema`,
		})
	}
	return &mcp.ListResourcesResult{Resources: resources}, nil
}

func readResource(ctx context.Context, d *db, uri string) (*mcp.ReadResourceResult, error) {
	u, err := url.Parse(uri)
	if err != nil {
		return nil, fmt.Errorf("invalid resource URI: %w", err)
	}
	parts := strings.Split(u.Path, "/")
	if len(parts) < 2 || parts[len(parts)-1] != "schema" {
		return nil, errors.New("Invalid resource URI")
	}
	r, err := d.fixedQuery(ctx,
		"SELECT column_name, data_type FROM information_schema.columns WHERE table_schema = 'public' AND table_name = $1 ORDER BY ordinal_position",
		parts[len(parts)-2])
	if err != nil {
		return nil, err
	}
	return &mcp.ReadResourceResult{Contents: []*mcp.ResourceContents{{URI: uri, MIMEType: "application/json", Text: r.json}}}, nil
}

// resourceBase mirrors the old server: the connection URL with scheme
// "postgres" and no password. Relative resolution later drops the database
// name and query string, as JS `new URL(path, base)` did (SPEC F7).
func resourceBase(raw string, cfg *pgx.ConnConfig) *url.URL {
	u, err := url.Parse(raw)
	if err != nil || u.Scheme == "" { // keyword DSN, e.g. "host=db dbname=app"
		u = &url.URL{Host: net.JoinHostPort(cfg.Host, strconv.Itoa(int(cfg.Port))), User: url.User(cfg.User)}
	} else if u.User != nil {
		u.User = url.User(u.User.Username())
	}
	u.Scheme = "postgres"
	return u
}

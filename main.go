package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"net/url"
	"os"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

var version = "dev" // set by -ldflags at release

func main() {
	timeout := flag.Duration("timeout", 30*time.Second, "time limit per call")
	maxRows := flag.Int("max-rows", 1000, "rows returned per call")
	maxBytes := flag.Int("max-bytes", 256<<10, "JSON bytes returned per call, and max size of one row")
	allowAnalyze := flag.Bool("allow-explain-analyze", false, "let the explain tool run EXPLAIN ANALYZE")
	allowSuperuser := flag.Bool("allow-superuser", false, "run as a superuser role (read-only is then not guaranteed)")
	flag.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: postgres-mcp-safe [flags] <databaseUrl>")
		flag.PrintDefaults()
	}
	flag.Parse()

	if flag.NArg() == 0 {
		fmt.Fprintln(os.Stderr, "Please provide a database URL as a command-line argument")
		os.Exit(1)
	}
	if *timeout <= 0 || *maxRows <= 0 || *maxBytes <= 0 {
		fmt.Fprintln(os.Stderr, "--timeout, --max-rows and --max-bytes must be greater than zero")
		os.Exit(1)
	}
	raw := flag.Arg(0)
	cfg, err := pgx.ParseConfig(raw)
	if err != nil {
		fmt.Fprintln(os.Stderr, err) // pgx redacts the password
		os.Exit(1)
	}

	d := newDB(cfg, limits{timeout: *timeout, maxRows: *maxRows, maxBytes: *maxBytes}, *allowSuperuser)
	if err := newServer(d, resourceBase(raw, cfg), *allowAnalyze).Run(context.Background(), &mcp.StdioTransport{}); err != nil {
		slog.Error("server stopped", "err", err)
		os.Exit(1)
	}
}

func newServer(d *db, base *url.URL, allowAnalyze bool) *mcp.Server {
	s := mcp.NewServer(&mcp.Implementation{Name: "postgres-mcp-safe", Version: version}, &mcp.ServerOptions{
		// Resources are served by middleware, so the SDK can't infer this.
		Capabilities: &mcp.ServerCapabilities{Resources: &mcp.ResourceCapabilities{}, Tools: &mcp.ToolCapabilities{}},
	})
	addTools(s, d, allowAnalyze)
	s.AddReceivingMiddleware(resourcesMiddleware(d, base))
	return s
}

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxConns          = 2
	traceFlushTimeout = 2 * time.Second
)

func main() {
	slog.SetDefault(slog.New(slog.NewJSONHandler(os.Stderr, nil)))

	if err := run(); err != nil {
		slog.Error("safedb-mcp-exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		return errors.New("DATABASE_URL is not set")
	}

	// Claude Code and Codex don't keep a server's stderr, so the audit trail needs its own file.
	if path := os.Getenv("AUDIT_LOG"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
		if err != nil {
			return fmt.Errorf("open AUDIT_LOG: %w", err)
		}
		slog.SetDefault(slog.New(slog.NewJSONHandler(io.MultiWriter(f, os.Stderr), nil)))
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	shutDownTracing, err := setupTracing(ctx)
	if err != nil {
		return fmt.Errorf("set up tracing: %w", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), traceFlushTimeout)
		defer cancel()
		if err := shutDownTracing(ctx); err != nil {
			slog.Warn("flush traces", "err", err)
		}
	}()

	db, err := newPool(ctx, dsn)
	if err != nil {
		return err
	}
	defer db.Close()

	slog.Info("starting", "version", version())
	err = newServer(db).Run(ctx, &mcp.StdioTransport{})
	if ctx.Err() != nil {
		return nil
	}
	return err

}

func newServer(db *pgxpool.Pool) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "safedb", Version: version()},
		nil,
	)
	s.AddReceivingMiddleware(traceToolCalls, auditLog)
	t := tools{db: db}
	readOnly := &mcp.ToolAnnotations{ReadOnlyHint: true}

	mcp.AddTool(
		s,
		&mcp.Tool{
			Name:        "list_tables",
			Description: "List the tables, views and materialized views this database user can read, with their schema. Call this first to find out what data exists.",
			Annotations: readOnly,
		},
		t.listTables,
	)

	mcp.AddTool(s, &mcp.Tool{
		Name:        "describe_table",
		Description: "List the columns of one table or view: name, PostgreSQL type, and whether it can be NULL.",
		Annotations: readOnly,
	}, t.describeTable)

	mcp.AddTool(
		s,
		&mcp.Tool{
			Name:        "query",
			Description: queryDescription,
			Annotations: readOnly,
		},
		t.query,
	)
	return s
}

func newPool(ctx context.Context, dsn string) (*pgxpool.Pool, error) {
	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return nil, errors.New("DATABASE_URL is not a valid Postgres connection string")
	}
	cfg.MaxConns = maxConns
	cfg.ConnConfig.Tracer = sqlTracer{}
	return pgxpool.NewWithConfig(ctx, cfg)
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}

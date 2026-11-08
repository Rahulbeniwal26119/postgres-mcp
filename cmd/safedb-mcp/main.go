package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const maxConns = 2

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

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

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	cfg, err := pgxpool.ParseConfig(dsn)
	if err != nil {
		return fmt.Errorf("parse DATABASE_URL: %w", err)
	}
	cfg.MaxConns = maxConns

	db, err := pgxpool.NewWithConfig(ctx, cfg)
	if err != nil {
		return fmt.Errorf("create pool: %w", err)
	}
	defer db.Close()

	slog.Info("starting", "version", version())
	return newServer(db).Run(ctx, &mcp.StdioTransport{})
}

func newServer(db *pgxpool.Pool) *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "safedb", Version: version()},
		&mcp.ServerOptions{Logger: slog.Default()},
	)
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

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}

package main

import (
	"context"
	"errors"
	"log/slog"
	"os"
	"os/signal"
	"runtime/debug"
	"syscall"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func main() {
	slog.SetDefault(slog.New(slog.NewTextHandler(os.Stderr, nil)))

	if err := run(); err != nil {
		slog.Error("safedb-mcp-exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	if os.Getenv("DATABASE_URL") == "" {
		return errors.New("DATABASE_URL is not set")
	}

	ctx, stop := signal.NotifyContext(
		context.Background(),
		os.Interrupt,
		syscall.SIGTERM,
	)
	defer stop()

	slog.Info("starting", "version", version())
	return newServer().Run(ctx, &mcp.StdioTransport{})
}

func newServer() *mcp.Server {
	s := mcp.NewServer(
		&mcp.Implementation{Name: "safedb", Version: version()},
		&mcp.ServerOptions{Logger: slog.Default()},
	)
	mcp.AddTool(
		s,
		&mcp.Tool{Name: "ping", Description: "Check that the safedb server is alive."},
		ping,
	)
	return s
}

type pingOutput struct {
	Message string `json:"message"`
}

func ping(context.Context, *mcp.CallToolRequest, struct{}) (*mcp.CallToolResult, pingOutput, error) {
	return nil, pingOutput{Message: "pong"}, nil
}

func version() string {
	if info, ok := debug.ReadBuildInfo(); ok {
		return info.Main.Version
	}
	return "unknown"
}

package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/testcontainers/testcontainers-go"
	"github.com/testcontainers/testcontainers-go/modules/postgres"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

const postgresImage = "pgvector/pgvector:0.8.7-pg18-trixie"

var testDSN string

// spans records every span the tests create, in memory; TestTracing reads it.
// Set once here, before any test runs, because the OpenTelemetry tracer provider is global.
var spans = tracetest.NewSpanRecorder()

func TestMain(m *testing.M) {
	otel.SetTracerProvider(sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(spans)))
	flag.Parse()
	if testing.Short() {
		os.Exit(m.Run())
	}

	ctr, dsn, err := startPostgres(context.Background())
	if err != nil {
		testcontainers.TerminateContainer(ctr)
		fmt.Fprintln(os.Stderr, "start test database (is Docker running? -short skips database tests):", err)
		os.Exit(1)
	}
	testDSN = dsn
	code := m.Run()
	if err := testcontainers.TerminateContainer(ctr); err != nil {
		fmt.Fprintln(os.Stderr, "remove test database:", err)
	}
	os.Exit(code)
}

func startPostgres(ctx context.Context) (*postgres.PostgresContainer, string, error) {
	scripts, _ := filepath.Glob("../../db/initdb/*")
	if len(scripts) == 0 {
		return nil, "", errors.New("no init scripts in ../../db/initdb")
	}
	ctr, err := postgres.Run(ctx, postgresImage,
		postgres.WithDatabase("pagila"),
		postgres.WithInitScripts(scripts...),
		testcontainers.WithTmpfs(map[string]string{"/var/lib/postgresql": "rw"}),
		postgres.BasicWaitStrategies(),
	)

	if err != nil {
		return ctr, "", err
	}

	hostPort, err := ctr.Endpoint(ctx, "")
	if err != nil {
		return ctr, "", err
	}

	return ctr, "postgres://readonly:readonly@" + hostPort + "/pagila", nil
}

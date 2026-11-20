package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"log"
	"log/slog"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestAuditLog(t *testing.T) {
	logs := captureLogs(t)
	cs := newTestSession(t)

	callTool(t, cs, "query", map[string]any{"sql": "SELECT 1 AS n FROM pg_sleep(0.05)"}, &queryOutput{})
	callToolErr(t, cs, "query", map[string]any{"sql": "SELECT nope"})
	callTool(t, cs, "list_tables", nil, &listTablesOutput{})

	lines := auditLines(t, logs)
	if len(lines) != 3 {
		t.Fatalf("got %d audit lines, want one per call (3):\n%s", len(lines), logs)
	}
	ok, failed, list := lines[0], lines[1], lines[2]
	if ok["tool"] != "query" || fmt.Sprint(ok["args"]) != "map[sql:SELECT 1 AS n FROM pg_sleep(0.05)]" || ok["rows"] != 1.0 || ok["error"] != nil {
		t.Errorf("successful query: %v", ok)
	}
	if ms, _ := ok["duration_ms"].(float64); ms < 50 {
		t.Errorf("duration_ms = %v, want at least 50: wrong unit?", ok["duration_ms"])
	}

	if !strings.Contains(fmt.Sprint(failed["error"]), `column "nope" does not exist`) {
		t.Errorf("failed query should log the SQL error: %v", failed)
	}
	if list["tool"] != "list_tables" || list["error"] != nil {
		t.Errorf("list_tables: %v", list)
	}
}

// Runs without the compose database: a canary password and a port nothing listens on.
func TestConnectionStringNeverLogged(t *testing.T) {
	const canary = "canary-pw-7f3a"
	logs := captureLogs(t)

	t.Run("invalid DATABASE_URL", func(t *testing.T) {
		_, err := newPool(t.Context(), "postgres://readonly:"+canary+"@localhost:notaport/pagila")
		if err == nil {
			t.Fatal("want an error for an invalid port")
		}
		if strings.Contains(err.Error(), canary) || strings.Contains(err.Error(), "postgres://") {
			t.Errorf("error quotes the connection string: %v", err)
		}
	})

	t.Run("database unreachable", func(t *testing.T) {
		db, err := newPool(t.Context(), "postgres://readonly:"+canary+"@127.0.0.1:1/pagila")
		if err != nil {
			t.Fatal(err)
		}
		t.Cleanup(db.Close)
		cs := connectSession(t, db)
		if msg := callToolErr(t, cs, "list_tables", nil); msg == "" {
			t.Fatal("want a connection error")
		}
	})

	if strings.Contains(logs.String(), canary) || strings.Contains(logs.String(), "postgres://") {
		t.Errorf("logs contain the connection string:\n%s", logs)
	}
	if n := len(auditLines(t, logs)); n != 1 {
		t.Errorf("the failed call should still be audited: got %d audit lines", n)
	}
}

// main logs run's error after run returns, so run must leave AUDIT_LOG open for that line.
func TestStartupErrorReachesAuditLog(t *testing.T) {
	path := filepath.Join(t.TempDir(), "audit.jsonl")
	t.Setenv("AUDIT_LOG", path)
	t.Setenv("DATABASE_URL", "postgres://readonly@localhost:notaport/pagila")
	captureLogs(t) // restores the default logger, which run replaces

	err := run()
	if err == nil {
		t.Fatal("want an error for an invalid DATABASE_URL")
	}
	slog.Error("safedb-mcp-exited", "err", err) // what main does next

	b, _ := os.ReadFile(path)
	if !strings.Contains(string(b), "not a valid Postgres connection string") {
		t.Errorf("startup error missing from AUDIT_LOG:\n%s", b)
	}
}

// captureLogs sends everything logged through slog during the test to the returned buffer.
// slog.SetDefault also redirects the log package, so that is restored too, or later tests
// would keep logging through this test's handler.
func captureLogs(t *testing.T) *bytes.Buffer {
	var buf bytes.Buffer
	prev, prevOut, prevFlags := slog.Default(), log.Writer(), log.Flags()
	slog.SetDefault(slog.New(slog.NewJSONHandler(&buf, nil)))
	t.Cleanup(func() {
		slog.SetDefault(prev)
		log.SetOutput(prevOut)
		log.SetFlags(prevFlags)
	})
	return &buf
}

// auditLines decodes the captured log lines and returns the "tool call" ones.
func auditLines(t *testing.T, logs *bytes.Buffer) []map[string]any {
	t.Helper()
	var lines []map[string]any
	for _, l := range strings.Split(strings.TrimSpace(logs.String()), "\n") {
		var m map[string]any
		if err := json.Unmarshal([]byte(l), &m); err != nil {
			t.Fatalf("log line is not JSON: %q", l)
		}
		if m["msg"] == "tool call" {
			lines = append(lines, m)
		}
	}
	return lines
}

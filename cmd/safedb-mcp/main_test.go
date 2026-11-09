package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestTools(t *testing.T) {
	cs := newTestSession(t)

	var tables listTablesOutput
	callTool(t, cs, "list_tables", nil, &tables)
	if !slices.ContainsFunc(tables.Tables, func(tb table) bool { return tb.Name == "rental" }) {
		t.Errorf("list_tables has no rental: %+v", tables.Tables)
	}
	if slices.ContainsFunc(tables.Tables, func(tb table) bool { return tb.Name == "payment_p2022_01" }) {
		t.Errorf("list_tables should hide partitions: %+v", tables.Tables)
	}

	var cols describeTableOutput
	callTool(t, cs, "describe_table", map[string]any{"table": "rental"}, &cols)
	if !slices.ContainsFunc(cols.Columns, func(c column) bool { return c.Name == "rental_date" }) {
		t.Errorf("describe_table rental has no rental_date: %+v", cols.Columns)
	}

	if msg := callToolErr(t, cs, "describe_table", map[string]any{"table": "no_such_table"}); msg == "" {
		t.Error("describe_table no_such_table: want a tool error")
	}
}

// newTestSession connects an in-memory MCP client to a server backed by the real compose database.
// The SQL is the logic under test, so a fake database would prove nothing.
func newTestSession(t *testing.T) *mcp.ClientSession {
	t.Helper()
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; run `docker compose up -d` and export it")
	}
	db, err := newPool(t.Context(), dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)
	return connectSession(t, db)
}

// connectSession connects an in-memory MCP client to a server backed by db.
func connectSession(t *testing.T, db *pgxpool.Pool) *mcp.ClientSession {
	t.Helper()
	ctx := t.Context()
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := newServer(db).Connect(ctx, serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })

	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil).Connect(ctx, clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })
	return cs
}

// callTool calls a tool, fails the test on any error, and decodes its structured output into out.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args, out any) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		t.Fatalf("%s returned a tool error: %s", name, toolText(res))
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: decode output: %v", name, err)
	}
}

// callToolErr calls a tool that is expected to fail and returns the error text the model would see.
// It returns "" if the call succeeded.
func callToolErr(t *testing.T, cs *mcp.ClientSession, name string, args any) string {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if !res.IsError {
		return ""
	}
	return toolText(res)
}

package main

import (
	"encoding/json"
	"os"
	"slices"
	"testing"

	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// Runs against the real compose database: the SQL is the logic under test, so a fake would prove nothing.
func TestTools(t *testing.T) {
	dsn := os.Getenv("DATABASE_URL")
	if dsn == "" {
		t.Skip("DATABASE_URL not set; run `docker compose up -d` and export it")
	}
	ctx := t.Context()

	db, err := pgxpool.New(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(db.Close)

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

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "describe_table", Arguments: map[string]any{"table": "no_such_table"}})
	if err != nil {
		t.Fatal(err)
	}
	if !res.IsError {
		t.Errorf("describe_table no_such_table: want a tool error, got %+v", res.StructuredContent)
	}
}

// callTool calls a tool, fails the test on any error, and decodes its structured output into out.
func callTool(t *testing.T, cs *mcp.ClientSession, name string, args, out any) {
	t.Helper()
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
	if err != nil {
		t.Fatalf("%s: %v", name, err)
	}
	if res.IsError {
		b, _ := json.Marshal(res.Content)
		t.Fatalf("%s returned a tool error: %s", name, b)
	}
	b, err := json.Marshal(res.StructuredContent)
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(b, out); err != nil {
		t.Fatalf("%s: decode output: %v", name, err)
	}
}

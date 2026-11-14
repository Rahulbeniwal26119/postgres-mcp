package main

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestMCPTools(t *testing.T) {
	server := mcp.NewServer(&mcp.Implementation{Name: "fake", Version: "test"}, nil)
	mcp.AddTool(
		server,
		&mcp.Tool{Name: "rental_count", Description: "Rentals in one month"},
		func(_ context.Context, _ *mcp.CallToolRequest, in struct {
			Month string `json:"month"`
		}) (*mcp.CallToolResult, any, error) {
			if in.Month == "" {
				return nil, nil, errors.New("month is required")
			}
			return &mcp.CallToolResult{Content: []mcp.Content{&mcp.TextContent{Text: "7319"}}}, nil, nil
		})
	serverT, clientT := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), serverT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { ss.Close() })
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "test"}, nil).Connect(t.Context(), clientT, nil)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cs.Close() })

	tools, err := mcpTools(t.Context(), cs)
	if err != nil || len(tools) != 1 {
		t.Fatalf("mcpTools = %v, %v; want one tool", tools, err)
	}

	rc := tools[0]
	if rc.name != "rental_count" || rc.description != "Rentals in one month" || rc.schema["type"] != "object" {
		t.Errorf("tool = %q %q %v, want rental_count with an object schema", rc.name, rc.description, rc.schema)
	}

	if out, err := rc.run(t.Context(), json.RawMessage(`{"month":"2022-05"}`)); out != "7319" || err != nil {
		t.Errorf("run = (%q, %v), want (\"7319\", nil)", out, err)
	}
	if _, err := rc.run(t.Context(), nil); !errMatches(err, "month") {
		t.Errorf("run with no input: err = %v, want an error about month", err)
	}

}

package main

import (
	"context"
	"testing"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

func TestServerListAndCallsPing(t *testing.T) {
	ctx := context.Background()
	serverT, clientT := mcp.NewInMemoryTransports()

	ss, err := newServer().Connect(ctx, serverT, nil)
	
	if err != nil {
		t.Fatal(err)
	}

	defer ss.Close()

	client := mcp.NewClient(&mcp.Implementation{Name: "test", Version: "v0"}, nil)
	cs, err := client.Connect(ctx, clientT, nil)

	if err != nil {
		t.Fatal(err)
	}

	defer cs.Close()

	tools, err := cs.ListTools(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}

	if len(tools.Tools) != 1 || tools.Tools[0].Name != "ping" {
		t.Fatalf("tools = %+v, want just ping", tools.Tools)
	}

	res, err := cs.CallTool(ctx, &mcp.CallToolParams{Name: "ping"})
	if err != nil {
		t.Fatal(err)
	}

	if res.IsError {
		t.Fatalf("ping returned a tool error: %+v", res.Content)
	}
}
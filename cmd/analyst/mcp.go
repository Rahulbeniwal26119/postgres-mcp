package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const toolCalloutTime = 30 * time.Second

func connectMCP(ctx context.Context, command string) (*mcp.ClientSession, error) {
	cmd := exec.Command(command)
	cmd.Stderr = os.Stderr
	client := mcp.NewClient(&mcp.Implementation{Name: "analyst", Version: "V0.1.0"}, nil)
	session, err := client.Connect(ctx, &mcp.CommandTransport{Command: cmd}, nil)
	if err != nil {
		return nil, fmt.Errorf("start MCP server %q: %w", command, err)
	}
	return session, nil
}

func mcpTools(ctx context.Context, s *mcp.ClientSession) ([]tool, error) {
	var tools []tool
	for t, err := range s.Tools(ctx, nil) {
		if err != nil {
			return nil, fmt.Errorf("list MCP tools: %w", err)
		}
		schema, ok := t.InputSchema.(map[string]any)
		if !ok {
			return nil, fmt.Errorf("tool %q: input schema is %T, want a JSON object", t.Name, t.InputSchema)
		}
		tools = append(tools, tool{name: t.Name, description: t.Description, schema: schema, run: callTool(s, t.Name)})
	}
	return tools, nil
}

func callTool(s *mcp.ClientSession, name string) func(context.Context, json.RawMessage) (string, error) {
	return func(ctx context.Context, input json.RawMessage) (string, error) {
		ctx, cancel := context.WithTimeout(ctx, toolCalloutTime)
		defer cancel()

		if len(input) == 0 {
			input = json.RawMessage(`{}`)
		}
		res, err := s.CallTool(ctx, &mcp.CallToolParams{Name: name, Arguments: input})
		if err != nil {
			return "", err
		}
		var text strings.Builder
		for _, c := range res.Content {
			if t, ok := c.(*mcp.TextContent); ok {
				text.WriteString(t.Text)
			}
		}
		if res.IsError {
			return "", errors.New(text.String())
		}
		return text.String(), nil
	}
}

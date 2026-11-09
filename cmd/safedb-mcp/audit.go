package main

import (
	"context"
	"encoding/json"
	"log/slog"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
)

// auditLog writes one JSON line per tool call: the tool, its arguments as the model sent them
// (the SQL, for query), how long it took, how many rows came back, and the error if it failed.
// It is middleware, so every tool is audited, including ones added later.

func auditLog(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}

		start := time.Now()
		res, err := next(ctx, method, req)
		var attrs []any
		if call, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
			attrs = append(attrs, "tool", call.Name, "args", call.Arguments)
		}
		attrs = append(attrs, "duration_ms", float64(time.Since(start).Milliseconds())/1000)
		if r, ok := res.(*mcp.CallToolResult); ok && r != nil {
			attrs = append(attrs, resultAttrs(r)...)
		}
		if err != nil {
			attrs = append(attrs, "error", err.Error())
		}
		slog.InfoContext(ctx, "tool call", attrs...)
		return res, err
	}
}

func resultAttrs(r *mcp.CallToolResult) []any {
	if r.IsError {
		return []any{"error", toolText(r)}
	}
	var out struct {
		Rows     []json.RawMessage `json:"rows"`
		Truncate bool              `json:"truncated"`
	}
	raw, _ := r.StructuredContent.(json.RawMessage)
	if json.Unmarshal(raw, &out) != nil || out.Rows == nil {
		return nil
	}
	return []any{"rows", len(out.Rows), "truncated", out.Truncate}
}

func toolText(r *mcp.CallToolResult) string {
	if len(r.Content) == 0 {
		return ""
	}
	if tc, ok := r.Content[0].(*mcp.TextContent); ok {
		return tc.Text
	}
	return ""
}

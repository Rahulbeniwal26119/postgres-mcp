package main

import (
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
)

func TestTracing(t *testing.T) {
	logs := captureLogs(t)
	cs := newTestSession(t)

	const traceID, callerSpanID = "4bf92f3577b34da6a3ce929d0e0e4733", "00f067aa0ba902b7"
	res, err := cs.CallTool(t.Context(), &mcp.CallToolParams{
		Name:      "query",
		Arguments: map[string]any{"sql": "SELECT 1 AS n"},
		Meta:      mcp.Meta{"traceparent": "00-" + traceID + "-" + callerSpanID + "-01"},
	})
	if err != nil || res.IsError {
		t.Fatalf("query: err=%v result=%v", err, res)
	}

	var tool, sql sdktrace.ReadOnlySpan
	for _, s := range spans.Ended() {
		if s.SpanContext().TraceID().String() != traceID {
			continue
		}
		switch s.Name() {
		case "tools/call query":
			tool = s
		case "SELECT":
			sql = s
		}
	}
	if tool == nil {
		t.Fatal("no tools/call span joined the caller's trace")
	}
	if got := tool.Parent().SpanID().String(); got != callerSpanID {
		t.Errorf("tool span parent = %s, want the caller's span %s", got, callerSpanID)
	}
	if sql == nil || sql.Parent().SpanID() != tool.SpanContext().SpanID() {
		t.Fatal("the model's SELECT should be a child span of the tool call")
	}
	for _, a := range sql.Attributes() {
		if a.Key == "db.query.text" && a.Value.AsString() != "SELECT 1 AS n" {
			t.Errorf("db.query.text = %q", a.Value.AsString())
		}
	}
	if lines := auditLines(t, logs); len(lines) != 1 || lines[0]["trace_id"] != traceID {
		t.Errorf("audit line should carry trace_id %s: %v", traceID, lines)
	}
}

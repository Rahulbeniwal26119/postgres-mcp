package main

import (
	"context"
	"log/slog"
	"os"
	"strings"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const tracerName = "github.com/Rahulbeniwal26119/postgres-mcp/cmd/safedb-mcp"

// setupTracing turns tracing on only when OTEL_EXPORTER_OTLP_ENDPOINT is set, for example
// http://localhost:4318 for the compose Jaeger. Otherwise the global tracer stays a no-op:
// spans cost almost nothing and nothing leaves the process.
// The returned function flushes buffered spans; call it before exiting.
func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return func(context.Context) error { return nil }, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return nil, err
	}

	res, err := resource.New(
		ctx,
		resource.WithAttributes(semconv.ServiceName("safedb-mcp"),
			semconv.ServiceVersion(version())),
		resource.WithFromEnv(), // OTEL_SERVICE_NAME and OTEL_RESOURCE_ATTRIBUTES override the defaults
	)
	if err != nil {
		return nil, err
	}

	tp := sdktrace.NewTracerProvider(sdktrace.WithBatcher(exporter), sdktrace.WithResource(res))
	otel.SetTracerProvider(tp)
	// Export failures (collector down) are warnings: tool calls carry on without tracing.
	otel.SetErrorHandler(otel.ErrorHandlerFunc(func(err error) { slog.Warn("tracing", "err", err) }))
	return tp.Shutdown, nil
}

func traceToolCalls(next mcp.MethodHandler) mcp.MethodHandler {
	return func(ctx context.Context, method string, req mcp.Request) (mcp.Result, error) {
		if method != "tools/call" {
			return next(ctx, method, req)
		}
		var tool string
		if call, ok := req.GetParams().(*mcp.CallToolParamsRaw); ok {
			tool = call.Name
			ctx = propagation.TraceContext{}.Extract(ctx, metaCarrier(call.Meta))
		}
		ctx, span := otel.Tracer(tracerName).Start(
			ctx, method+" "+tool,
			trace.WithSpanKind(trace.SpanKindServer),
			trace.WithAttributes(
				attribute.String("mcp.method.name", method),
				attribute.String("gen_ai.tool.name", tool),
			),
		)
		defer span.End()

		res, err := next(ctx, method, req)
		if err != nil {
			span.SetStatus(codes.Error, err.Error())
		} else if r, ok := res.(*mcp.CallToolResult); ok && r != nil && r.IsError {
			span.SetStatus(codes.Error, toolText(r))
		}
		return res, err
	}
}

// metaCarrier exposes the string values of _meta, where MCP clients put traceparent and tracestate.
func metaCarrier(meta mcp.Meta) propagation.MapCarrier {
	carrier := propagation.MapCarrier{}
	for k, v := range meta {
		if s, ok := v.(string); ok {
			carrier[k] = s
		}
	}
	return carrier
}

type sqlTracer struct{}

func (sqlTracer) TraceQueryStart(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryStartData) context.Context {
	ctx, _ = otel.Tracer(tracerName).Start(ctx, sqlOperation(data.SQL),
		trace.WithSpanKind(trace.SpanKindClient),
		trace.WithAttributes(
			attribute.String("db.system.name", "postgresql"),
			attribute.String("db.query.text", data.SQL),
		),
	)
	return ctx
}

func (sqlTracer) TraceQueryEnd(ctx context.Context, _ *pgx.Conn, data pgx.TraceQueryEndData) {
	span := trace.SpanFromContext(ctx)
	if data.Err != nil {
		span.SetStatus(codes.Error, data.Err.Error())
	}
	span.End()
}

func sqlOperation(sql string) string {
	if fields := strings.Fields(sql); len(fields) > 0 {
		return strings.ToUpper(fields[0])
	}
	return "QUERY"
}

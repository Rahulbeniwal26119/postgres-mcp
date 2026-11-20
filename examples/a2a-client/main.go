package main

import (
	"cmp"
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2aclient/agentcard"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/codes"
	"go.opentelemetry.io/otel/exporters/otlp/otlptrace/otlptracehttp"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/sdk/resource"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	semconv "go.opentelemetry.io/otel/semconv/v1.43.0"
	"go.opentelemetry.io/otel/trace"
)

const (
	defaultAgentURL   = "http://localhost:9000"
	askTimeout        = 2 * time.Minute
	traceFlushTimeout = 2 * time.Second
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New(`usage: a2a-client "question"`)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	ctx, cancel := context.WithTimeout(ctx, askTimeout)
	defer cancel()

	shutdownTracing, err := setupTracing(ctx)
	if err != nil {
		return fmt.Errorf("setup tracing: %w", err)
	}

	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), traceFlushTimeout)
		defer cancel()
		if err := shutdownTracing(ctx); err != nil {
			fmt.Fprintln(os.Stderr, "flush traces: ", err)
		}
	}()

	card, err := agentcard.DefaultResolver.Resolve(ctx, cmp.Or(os.Getenv("ANALYST_A2A_URL"), defaultAgentURL))
	if err != nil {
		return fmt.Errorf("fetch the agent card: %w", err)
	}
	transport := otelhttp.NewTransport(bearerTransport{token: os.Getenv("ANALYST_A2A_TOKEN"), next: http.DefaultTransport})
	client, err := a2aclient.NewFromCard(ctx, card, a2aclient.WithJSONRPCTransport(&http.Client{Transport: transport}))
	if err != nil {
		return err
	}

	ctx, span := otel.Tracer("a2a-client").Start(ctx, "invoke_agent "+card.Name, trace.WithSpanKind(trace.SpanKindClient))
	defer span.End()
	spinCtx, stopSpin := context.WithCancel(ctx)
	go spinner(spinCtx, "thinking")
	res, err := client.SendMessage(ctx, &a2a.SendMessageRequest{
		Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(os.Args[1])),
	})
	stopSpin()
	if err != nil {
		span.SetStatus(codes.Error, err.Error())
		return fmt.Errorf("send message: %w", err)
	}
	task, ok := res.(*a2a.Task)
	if !ok {
		return fmt.Errorf("the agent replied with %T, want a task", res)
	}
	if task.Status.State != a2a.TaskStateCompleted {
		err := fmt.Errorf("task %s: %s", task.Status.State, statusText(task))
		span.SetStatus(codes.Error, err.Error())
		return err
	}
	printArtifact(task)
	return nil
}

func spinner(ctx context.Context, msg string) {
	frames := []rune(`⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏`)
	start := time.Now()
	t := time.NewTicker(100 * time.Millisecond)
	defer t.Stop()

	for i := 0; ; i++ {
		select {
		case <-ctx.Done():
			fmt.Fprint(os.Stderr, "\r\033[K") // erase the spinner
			return
		case <-t.C:
			fmt.Fprintf(os.Stderr, "\r%c %s (%ds)", frames[i%len(frames)], msg, int(time.Since(start).Seconds()))
		}
	}

}
func printArtifact(task *a2a.Task) {
	for _, artifact := range task.Artifacts {
		for _, p := range artifact.Parts {
			if text := p.Text(); text != "" {
				fmt.Println(text)
			}
			data, _ := p.Data().(map[string]any)
			sqls, _ := data["sql"].([]any)
			for _, sql := range sqls {
				fmt.Printf("\n-- SQL used\n%v\n", sql)
			}
		}
	}
}

func setupTracing(ctx context.Context) (func(context.Context) error, error) {
	noop := func(context.Context) error { return nil }
	if os.Getenv("OTEL_EXPORTER_OTLP_ENDPOINT") == "" {
		return noop, nil
	}
	exporter, err := otlptracehttp.New(ctx)
	if err != nil {
		return noop, err
	}
	tp := sdktrace.NewTracerProvider(
		sdktrace.WithBatcher(exporter),
		sdktrace.WithResource(resource.NewSchemaless(semconv.ServiceName("a2a-client"))),
	)
	otel.SetTracerProvider(tp)
	otel.SetTextMapPropagator(propagation.TraceContext{})
	return tp.Shutdown, nil
}

func statusText(task *a2a.Task) string {
	if task.Status.Message == nil || len(task.Status.Message.Parts) == 0 {
		return "no reason given"
	}
	return task.Status.Message.Parts[0].Text()
}

type bearerTransport struct {
	token string
	next  http.RoundTripper
}

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return b.next.RoundTrip(r)
}

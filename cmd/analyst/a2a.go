package main

import (
	"context"
	"crypto/subtle"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"github.com/a2aproject/a2a-go/v2/a2asrv/limiter"
	"github.com/modelcontextprotocol/go-sdk/auth"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
)

const (
	a2aPath            = "/invoke"
	maxConcurrentTasks = 4
	readHeaderTimeout  = 10 * time.Second
	shutDownTimeout    = 5 * time.Second
)

type answerFunc func(ctx context.Context, question string) (answer, error)

func serveA2A(ctx context.Context, addr, token string, answerFn answerFunc) error {
	if token == "" {
		return errors.New("ANALYST_A2A_TOKEN must be set when ANALYST_A2A_ADDR is")
	}
	card := agentCard("http://" + addr + a2aPath)
	hs := &http.Server{Addr: addr, Handler: a2aHandler(card, token, answerFn), ReadTimeout: readHeaderTimeout}

	errc := make(chan error, 1)
	go func() { errc <- hs.ListenAndServe() }()
	fmt.Fprintf(os.Stderr, "A2A agent card at http://%s%s\n", addr, a2asrv.WellKnownAgentCardPath)
	select {
	case err := <-errc:
		return err
	case <-ctx.Done():
	}
	ctx, cancel := context.WithTimeout(context.Background(), shutDownTimeout)
	defer cancel()
	return hs.Shutdown(ctx)

}

func a2aHandler(card *a2a.AgentCard, token string, answerFn answerFunc) http.Handler {
	requests := a2asrv.NewHandler(executor(answerFn), a2asrv.WithConcurrencyConfig(limiter.ConcurrencyConfig{MaxExecutions: maxConcurrentTasks}))
	mux := http.NewServeMux()
	mux.Handle(a2asrv.WellKnownAgentCardPath, a2asrv.NewStaticAgentCardHandler(card))
	mux.Handle(a2aPath, otelhttp.NewHandler(requireToken(token, a2asrv.NewJSONRPCHandler(requests)), "a2a"))
	return mux
}

func agentCard(url string) *a2a.AgentCard {
	return &a2a.AgentCard{
		Name:                "safedb analyst",
		Description:         "Answers questions about a PostgreSQL database by running read-only SQL, and returns the answer with the SQL behind it.",
		Version:             "0.1.0",
		SupportedInterfaces: []*a2a.AgentInterface{a2a.NewAgentInterface(url, a2a.TransportProtocolJSONRPC)},
		DefaultInputModes:   []string{"text/plain"},
		DefaultOutputModes:  []string{"text/plain", "application/json"},
		Skills: []a2a.AgentSkill{{
			ID:          "answer_data_question",
			Name:        "Answer a data question",
			Description: "Explores the schema, writes and run SQL",
			Tags:        []string{"sql", "postgres", "analytics"},
			Examples:    []string{"Which film category brings in the most revenue?", "How many rentals have not been returned yet?"},
		}},
		SecuritySchemes: a2a.NamedSecuritySchemes{
			"bearer": a2a.HTTPAuthSecurityScheme{Scheme: "bearer"}},
	}
}

func messageText(m *a2a.Message) string {
	var b strings.Builder
	for _, p := range m.Parts {
		b.WriteString(p.Text())
	}
	return strings.TrimSpace(b.String())
}

func failed(ec *a2asrv.ExecutorContext, reason string) *a2a.TaskStatusUpdateEvent {
	return a2a.NewStatusUpdateEvent(ec, a2a.TaskStateFailed, a2a.NewMessage(
		a2a.MessageRoleAgent,
		a2a.NewTextPart(reason),
	))
}

func executor(answerFn answerFunc) a2asrv.AgentExecutorFunc {
	return func(ctx context.Context, ec *a2asrv.ExecutorContext) iter.Seq2[a2a.Event, error] {
		return func(yield func(a2a.Event, error) bool) {
			if ec.StoredTask == nil && !yield(a2a.NewSubmittedTask(ec, ec.Message), nil) {
				return
			}
			question := messageText(ec.Message)
			if question == "" {
				yield(failed(ec, "send the question as a text part"), nil)
				return
			}
			a, err := answerFn(ctx, question)
			if err != nil {
				yield(failed(ec, err.Error()), nil)
				return
			}
			sql := a2a.NewDataPart(map[string]any{"sql": sqlOf(a.calls)})
			if !yield(a2a.NewArtifactEvent(ec, a2a.NewTextPart(a.text), sql), nil) {
				return
			}
			yield(a2a.NewStatusUpdateEvent(ec, a2a.TaskStateCompleted, nil), nil)
		}
	}
}

func requireToken(token string, next http.Handler) http.Handler {
	verify := func(_ context.Context, got string, _ *http.Request) (*auth.TokenInfo, error) {
		if subtle.ConstantTimeCompare([]byte(got), []byte(token)) != 1 {
			return nil, auth.ErrInvalidToken
		}
		return &auth.TokenInfo{}, nil
	}
	return auth.RequireBearerToken(verify, &auth.RequireBearerTokenOptions{AllowMissingExpiration: true})(next)
}

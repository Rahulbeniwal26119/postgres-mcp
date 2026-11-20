package main

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/a2aproject/a2a-go/v2/a2a"
	"github.com/a2aproject/a2a-go/v2/a2aclient"
	"github.com/a2aproject/a2a-go/v2/a2asrv"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/propagation"
	"go.opentelemetry.io/otel/trace"
)

func TestA2A(t *testing.T) {
	const token = "test-token"
	query := toolCall{id: "c1", name: "query", input: json.RawMessage(`{"sql":"SELECT count(*) FROM film"}`)}
	answerFn := func(_ context.Context, question string) (answer, error) {
		if question != "How many films are there?" {
			return answer{}, errIterationCap
		}
		return answer{text: "There are 1000 films.", calls: []toolCall{query}}, nil
	}
	srv := httptest.NewServer(a2aHandler(agentCard("http://unused"), token, answerFn))
	t.Cleanup(srv.Close)

	// The card is public; A2A requests without the token are refused.
	if resp, err := http.Get(srv.URL + a2asrv.WellKnownAgentCardPath); err != nil || resp.StatusCode != http.StatusOK {
		t.Fatalf("agent card: %v, %v; want 200", resp, err)
	}
	if resp, err := http.Post(srv.URL+a2aPath, "application/json", strings.NewReader("{}")); err != nil || resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("request without a token: %v, %v; want 401", resp, err)
	}

	client, err := a2aclient.NewFromEndpoints(t.Context(),
		[]*a2a.AgentInterface{a2a.NewAgentInterface(srv.URL+a2aPath, a2a.TransportProtocolJSONRPC)},
		a2aclient.WithJSONRPCTransport(&http.Client{Transport: bearerTransport{token}}),
	)
	if err != nil {
		t.Fatal(err)
	}
	send := func(question string) *a2a.Task {
		t.Helper()
		res, err := client.SendMessage(t.Context(), &a2a.SendMessageRequest{
			Message: a2a.NewMessage(a2a.MessageRoleUser, a2a.NewTextPart(question)),
		})
		task, ok := res.(*a2a.Task)
		if err != nil || !ok {
			t.Fatalf("SendMessage = %#v, %v; want a task", res, err)
		}
		return task
	}

	task := send("How many films are there?")
	if task.Status.State != a2a.TaskStateCompleted || len(task.Artifacts) != 1 {
		t.Fatalf("task = %+v; want completed with one artifact", task)
	}
	parts := task.Artifacts[0].Parts
	wantSQL := map[string]any{"sql": []any{"SELECT count(*) FROM film"}}
	if len(parts) != 2 || parts[0].Text() != "There are 1000 films." || !reflect.DeepEqual(parts[1].Data(), wantSQL) {
		t.Errorf("artifact parts = %v; want the answer, then %v", parts, wantSQL)
	}

	task = send("Something the agent can't answer")
	if task.Status.State != a2a.TaskStateFailed || !strings.Contains(task.Status.Message.Parts[0].Text(), "iteration cap") {
		t.Errorf("task = %+v; want failed with the reason", task.Status)
	}
}

func A2ATraceContext(t *testing.T) {
	otel.SetTextMapPropagator(propagation.TraceContext{})
	var got trace.SpanContext
	answerFn := func(ctx context.Context, _ string) (answer, error) {
		got = trace.SpanContextFromContext(ctx)
		return answer{text: "ok"}, nil
	}
	srv := httptest.NewServer(a2aHandler(agentCard("http://unused"), "test-token", answerFn))
	t.Cleanup(srv.Close)

	const traceID = "4bf92f3577b34da6a3ce929d0e0e4732"
	body := `{"jsonrpc":"2.0","id":1,"method":"SendMessage","params":{"message":{"messageId":"m1","role":"ROLE_USER","parts":[{"text":"hi"}]}}}`
	req, err := http.NewRequest(http.MethodPost, srv.URL+a2aPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Authorization", "Bearer test-token")
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("A2A-Version", "1.0")
	req.Header.Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK || got.TraceID().String() != traceID {
		t.Errorf("status %d, agent ran in trace %s; want 200 and the caller's trace %s", resp.StatusCode, got.TraceID(), traceID)
	}
}

// bearerTransport adds the token to every request, as a configured A2A client would.
type bearerTransport struct{ token string }

func (b bearerTransport) RoundTrip(r *http.Request) (*http.Response, error) {
	r = r.Clone(r.Context())
	r.Header.Set("Authorization", "Bearer "+b.token)
	return http.DefaultTransport.RoundTrip(r)
}

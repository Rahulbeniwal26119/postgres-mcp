package main

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/option"
	"github.com/openai/openai-go/v3"
	oaioption "github.com/openai/openai-go/v3/option"
)

// Each adapter talks to a fake API that asks for one tool call and then answers. That checks
// the translation both ways without a key or a bill.
func TestAdapters(t *testing.T) {
	tests := []struct {
		name       string
		newChat    func(baseURL string) chat
		replies    []string
		wantResult string // how the tool result appears in the second request
	}{
		{
			name: "claude",
			newChat: func(url string) chat {
				client := anthropic.NewClient(option.WithBaseURL(url), option.WithAPIKey("test"), option.WithMaxRetries(0))
				return newClaudeChat(client, defaultClaudeModel, "How many rentals in May 2022?", []tool{rentalCount})
			},
			replies: []string{
				`{"type":"message","role":"assistant","stop_reason":"tool_use","content":[
					{"type":"tool_use","id":"call_1","name":"rental_count","input":{"month":"2022-05"}}]}`,
				`{"type":"message","role":"assistant","stop_reason":"end_turn","content":[{"type":"text","text":"7319 rentals."}],"usage":{"input_tokens":120,"output_tokens":8}}`,
			},
			wantResult: `"tool_use_id":"call_1"`,
		},
		{
			name: "openai",
			newChat: func(url string) chat {
				client := openai.NewClient(oaioption.WithBaseURL(url), oaioption.WithAPIKey("test"), oaioption.WithMaxRetries(0))
				return newOpenAIChat(client, "any-model", "How many rentals in May 2022?", []tool{rentalCount})
			},
			replies: []string{
				`{"choices":[{"finish_reason":"tool_calls","message":{"role":"assistant","tool_calls":[
					{"id":"call_1","type":"function","function":{"name":"rental_count","arguments":"{\"month\":\"2022-05\"}"}}]}}]}`,
				`{"choices":[{"finish_reason":"stop","message":{"role":"assistant","content":"7319 rentals."}}],"usage":{"prompt_tokens":120,"completion_tokens":8}}`,
			},
			wantResult: `"tool_call_id":"call_1"`,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			var bodies []string
			api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				body, _ := io.ReadAll(r.Body)
				bodies = append(bodies, string(body))
				w.Header().Set("Content-Type", "application/json")
				io.WriteString(w, tt.replies[len(bodies)-1])
			}))
			t.Cleanup(api.Close)
			c := tt.newChat(api.URL)

			first, err := c.send(t.Context(), nil)
			wantCalls := []toolCall{{id: "call_1", name: "rental_count", input: json.RawMessage(`{"month":"2022-05"}`)}}
			if err != nil || first.stop != stopToolUse || !reflect.DeepEqual(first.calls, wantCalls) {
				t.Fatalf("first turn = %+v, %v; want a rental_count call", first, err)
			}

			second, err := c.send(t.Context(), []toolResult{{callID: "call_1", output: "7319"}})
			if err != nil || second.stop != stopDone || second.text != "7319 rentals." || second.usage != (usage{input: 120, output: 8}) {
				t.Fatalf("second turn = %+v, %v; want the answer and its token usage", second, err)
			}

			// The second request resends the first one plus the model's tool call and our tool result.
			var req1, req2 struct{ Messages []json.RawMessage }
			if err := json.Unmarshal([]byte(bodies[0]), &req1); err != nil {
				t.Fatal(err)
			}
			if err := json.Unmarshal([]byte(bodies[1]), &req2); err != nil {
				t.Fatal(err)
			}
			if len(req2.Messages) != len(req1.Messages)+2 || !strings.Contains(bodies[1], tt.wantResult) {
				t.Errorf("second request should resend the first plus the tool call and the result %s:\n%s", tt.wantResult, bodies[1])
			}
		})
	}
}

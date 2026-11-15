package main

import (
	"context"
	"encoding/json"
	"fmt"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

// scriptedChat replays fixed turns, the last one repeating, and records what the loop sent.
type scriptedChat struct {
	turns []turn
	sent  [][]toolResult
}

func (c *scriptedChat) send(_ context.Context, results []toolResult) (turn, error) {
	c.sent = append(c.sent, results)
	return c.turns[min(len(c.sent), len(c.turns))-1], nil
}

const fakeRentals = 7319

var rentalCount = tool{
	name:        "rental_count",
	description: "Number of DVD rentals in one month",
	schema: map[string]any{
		"type":       "object",
		"properties": map[string]any{"month": map[string]any{"type": "string", "description": "The month as YYYY-MM, e.g. 2026-11"}},
		"required":   []string{"month"},
	},
	run: func(_ context.Context, input json.RawMessage) (string, error) {
		var in struct {
			Month string `json:"month"`
		}
		if err := json.Unmarshal(input, &in); err != nil {
			return "", err
		}
		if _, err := time.Parse("2006-01", in.Month); err != nil {
			return "", fmt.Errorf("month must be YYYY-MM, got %q", in.Month)
		}
		return fmt.Sprintf(`{"month": %q, "rentals": %d}`, in.Month, fakeRentals), nil
	},
}

func TestAsk(t *testing.T) {
	callTool := turn{stop: stopToolUse, calls: []toolCall{
		{id: "call_1", name: "rental_count", input: json.RawMessage(`{"month":"2022-05"}`)},
	}}
	toolRan := []toolResult{{callID: "call_1", output: `{"month": "2022-05", "rentals": 7319}`}}

	tests := []struct {
		name    string
		turns   []turn
		want    string
		wantErr string
		sends   int
	}{
		{
			name:  "answers using the tool",
			turns: []turn{callTool, {stop: stopDone, text: "7319 rentals."}},
			want:  "7319 rentals.",
			sends: 2,
		},
		{
			name:    "stops at the iteration cap",
			turns:   []turn{callTool},
			wantErr: "iteration cap reached: still calling tools after 3 requests",
			sends:   3,
		},
		{
			name:    "output cut off",
			turns:   []turn{{stop: stopTruncated, text: "There were"}},
			wantErr: "cut off",
			sends:   1,
		},
		{
			name:    "refusal",
			turns:   []turn{{stop: stopRefused, refusal: "cyber"}},
			wantErr: "declined to answer: cyber",
			sends:   1,
		},
		{
			name:    "other stop reason",
			turns:   []turn{{stop: "pause_turn"}},
			wantErr: `unexpected stop reason "pause_turn"`,
			sends:   1,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			c := &scriptedChat{turns: tt.turns}

			a, err := ask(t.Context(), c, []tool{rentalCount}, 3)

			if a.text != tt.want || !errMatches(err, tt.wantErr) {
				t.Errorf("got (%q, %v), want (%q, error containing %q)", a.text, err, tt.want, tt.wantErr)
			}
			if len(c.sent) != tt.sends {
				t.Errorf("sent %d requests, want %d", len(c.sent), tt.sends)
			}
			if len(c.sent) > 1 && !slices.Equal(c.sent[1], toolRan) {
				t.Errorf("second request sent %v, want the tool's result %v", c.sent[1], toolRan)
			}
		})
	}
}

// A bad call goes back to the model as an error result, the model fixes it, and only the
// call that worked counts as used.
func TestAskRetriesAfterToolError(t *testing.T) {
	perRequest := usage{input: 100, output: 10}
	bad := toolCall{id: "call_1", name: "rental_count", input: json.RawMessage(`{"month":"May 2022"}`)}
	good := toolCall{id: "call_2", name: "rental_count", input: json.RawMessage(`{"month":"2022-05"}`)}
	c := &scriptedChat{turns: []turn{
		{stop: stopToolUse, calls: []toolCall{bad}, usage: perRequest},
		{stop: stopToolUse, calls: []toolCall{good}, usage: perRequest},
		{stop: stopDone, text: "7319 rentals.", usage: perRequest},
	}}

	a, err := ask(t.Context(), c, []tool{rentalCount}, maxIterations)

	if a.text != "7319 rentals." || err != nil {
		t.Fatalf("got (%q, %v), want the answer", a.text, err)
	}
	if r := c.sent[1]; len(r) != 1 || !r[0].isError {
		t.Errorf("second request sent %v, want the error result", r)
	}
	if !reflect.DeepEqual(a.calls, []toolCall{good}) {
		t.Errorf("used = %v, want only the call that worked", a.calls)
	}
	if a.usage != (usage{input: 300, output: 30}) {
		t.Errorf("usage: %+v, want the sum of 3 requests", a.usage)
	}
}

func TestRunTools(t *testing.T) {
	got := runTools(t.Context(), []toolCall{
		{id: "1", name: "rental_count", input: json.RawMessage(`{"month":"May 2022"}`)},
		{id: "2", name: "drop_table"},
	}, map[string]tool{"rental_count": rentalCount})

	want := []toolResult{
		{callID: "1", output: `month must be YYYY-MM, got "May 2022"`, isError: true},
		{callID: "2", output: `unknown tool "drop_table"`, isError: true},
	}
	if !slices.Equal(got, want) {
		t.Errorf("got %v\nwant %v", got, want)
	}
}

// errMatches reports whether err contains want, or is nil when want is empty.
func errMatches(err error, want string) bool {
	if err == nil || want == "" {
		return err == nil && want == ""
	}
	return strings.Contains(err.Error(), want)
}

package main

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"testing"
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

			got, err := ask(t.Context(), c, []tool{rentalCount}, 3)

			if got != tt.want || !errMatches(err, tt.wantErr) {
				t.Errorf("got (%q, %v), want (%q, error containing %q)", got, err, tt.want, tt.wantErr)
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

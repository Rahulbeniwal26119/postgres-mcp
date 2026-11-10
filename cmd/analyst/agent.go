package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
)

// A chat is one conversation with a model. Each provider adapter keeps the history in its own
// format (Claude's thinking blocks must go back unchanged), so the loop never sees it.
type chat interface {
	send(ctx context.Context, results []toolResult) (turn, error)
}

type turn struct {
	text    string
	calls   []toolCall
	stop    stopReason
	refusal string
}

type stopReason string

const (
	stopDone      stopReason = "done"
	stopToolUse   stopReason = "tool_use"
	stopTruncated stopReason = "truncated"
	stopRefused   stopReason = "refused"
)

type toolCall struct {
	id, name string
	input    json.RawMessage
}

type toolResult struct {
	callID, output string
	isError        bool
}

// A tool is what the model sees (name, description, JSON Schema of the input) plus the code that runs it.
type tool struct {
	name, description string
	schema            map[string]any
	run               func(ctx context.Context, input json.RawMessage) (string, error)
}

var errIterationCap = errors.New("iteration cap reached")

// ask runs the conversation tuntil the model answers. Every iteration is one model request,
// so maxIterations caps the cost of a model that keep calling tools
func ask(ctx context.Context, c chat, tools []tool, maxIterations int) (string, error) {
	byName := make(map[string]tool, len(tools))
	for _, t := range tools {
		byName[t.name] = t
	}

	var results []toolResult
	for range maxIterations {
		t, err := c.send(ctx, results)
		if err != nil {
			return "", err
		}
		switch t.stop {
		case stopDone:
			return t.text, nil
		case stopToolUse:
			results = runTools(ctx, t.calls, byName)
		case stopTruncated:
			return "", errors.New("answer cut off at the output token limit")
		case stopRefused:
			return "", fmt.Errorf("the model declined to answer: %s", t.refusal)
		default:
			return "", fmt.Errorf("unexpected stop reason %q", t.stop)
		}
	}
	return "", fmt.Errorf("%w: still calling tools after %d requests", errIterationCap, maxIterations)
}

// runTools retuns one result per call. A failed call go back to the model as an error
// result, so it can fix its input instead of whole question failing
func runTools(ctx context.Context, calls []toolCall, tools map[string]tool) []toolResult {
	results := make([]toolResult, len(calls))
	for i, call := range calls {
		out, err := "", fmt.Errorf("unknown tool %q", call.name)
		if t, ok := tools[call.name]; ok {
			out, err = t.run(ctx, call.input)
		}
		if err != nil {
			out = err.Error()
		}
		results[i] = toolResult{callID: call.id, output: out, isError: err != nil}
	}
	return results
}

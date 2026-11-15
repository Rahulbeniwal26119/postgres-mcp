package main

import (
	"context"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/anthropics/anthropic-sdk-go/v2/shared/constant"
)

const claudeMaxTokens = 1600

type claudeChat struct {
	client anthropic.Client
	params anthropic.BetaMessageNewParams
}

func newClaudeChat(client anthropic.Client, model, question string, tools []tool) *claudeChat {
	c := &claudeChat{client: client, params: anthropic.BetaMessageNewParams{
		Model:        model,
		MaxTokens:    claudeMaxTokens,
		System:       []anthropic.BetaTextBlockParam{{Text: systemPrompt}},
		OutputConfig: anthropic.BetaOutputConfigParam{Effort: anthropic.BetaOutputConfigEffortHigh},
		Betas:        []anthropic.AnthropicBeta{anthropic.AnthropicBetaServerSideFallback2026_07_01},
		Fallbacks:    anthropic.BetaFallbacksParamUnion{OfDefault: constant.ValueOf[constant.Default]()},
		Messages:     []anthropic.BetaMessageParam{anthropic.NewBetaUserMessage(anthropic.NewBetaTextBlock(question))},
	}}

	for _, t := range tools {
		c.params.Tools = append(c.params.Tools, anthropic.BetaToolUnionParam{OfTool: &anthropic.BetaToolParam{
			Name:        t.name,
			Description: anthropic.String(t.description),
			InputSchema: anthropic.BetaToolInputSchemaParam{ExtraFields: t.schema},
		}})
	}
	return c
}

func (c *claudeChat) send(ctx context.Context, results []toolResult) (turn, error) {
	if len(results) > 0 {
		blocks := make([]anthropic.BetaContentBlockParamUnion, len(results))
		for i, r := range results {
			blocks[i] = anthropic.NewBetaToolResultBlock(r.callID, r.output, r.isError)
		}
		c.params.Messages = append(c.params.Messages, anthropic.NewBetaUserMessage(blocks...))
	}

	resp, err := c.client.Beta.Messages.New(ctx, c.params)
	if err != nil {
		return turn{}, err
	}
	c.params.Messages = append(c.params.Messages, resp.ToParam())

	t := turn{stop: claudeStop(resp.StopReason)}

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			t.text += block.Text
		case "tool_use":
			t.calls = append(t.calls, toolCall{id: block.ID, name: block.Name, input: block.Input})
		}
	}
	if t.stop == stopRefused {
		t.refusal = string(resp.StopDetails.Category) + " " + resp.StopDetails.Explanation
	}
	return t, nil
}

func claudeStop(r anthropic.BetaStopReason) stopReason {
	switch r {
	case anthropic.BetaStopReasonEndTurn:
		return stopDone
	case anthropic.BetaStopReasonToolUse:
		return stopToolUse
	case anthropic.BetaStopReasonMaxTokens:
		return stopTruncated
	case anthropic.BetaStopReasonRefusal:
		return stopRefused
	}
	return stopReason(r)
}

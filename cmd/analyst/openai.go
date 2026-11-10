package main

import (
	"context"
	"encoding/json"
	"errors"

	"github.com/openai/openai-go/v3"
)

type openaiChat struct {
	client openai.Client
	params openai.ChatCompletionNewParams
}

func newOpenAIChat(client openai.Client, model, question string, tools []tool) *openaiChat {
	c := &openaiChat{client: client, params: openai.ChatCompletionNewParams{
		Model:    model,
		Messages: []openai.ChatCompletionMessageParamUnion{openai.UserMessage(question)},
	}}
	for _, t := range tools {
		c.params.Tools = append(c.params.Tools, openai.ChatCompletionFunctionTool(openai.FunctionDefinitionParam{
			Name:        t.name,
			Description: openai.String(t.description),
			Parameters:  t.schema,
		}))
	}
	return c
}

func (c *openaiChat) send(ctx context.Context, results []toolResult) (turn, error) {
	for _, r := range results {
		out := r.output
		if r.isError {
			out = "error: " + out
		}
		c.params.Messages = append(c.params.Messages, openai.ToolMessage(out, r.callID))
	}

	resp, err := c.client.Chat.Completions.New(ctx, c.params)
	if err != nil {
		return turn{}, err
	}
	if len(resp.Choices) == 0 {
		return turn{}, errors.New("response has no choices")
	}

	choice := resp.Choices[0]
	c.params.Messages = append(c.params.Messages, choice.Message.ToParam())

	t := turn{text: choice.Message.Content, refusal: choice.Message.Refusal, stop: openaiStop(choice)}
	for _, call := range choice.Message.ToolCalls {
		t.calls = append(t.calls, toolCall{id: call.ID, name: call.Function.Name, input: json.RawMessage(call.Function.Arguments)})
	}
	return t, nil
}

func openaiStop(choice openai.ChatCompletionChoice) stopReason {
	switch {
	case choice.Message.Refusal != "" || choice.FinishReason == "content_filter":
		return stopRefused
	case len(choice.Message.ToolCalls) > 0:
		return stopToolUse
	case choice.FinishReason == "stop":
		return stopDone
	case choice.FinishReason == "length":
		return stopTruncated
	}
	return stopReason(choice.FinishReason)
}

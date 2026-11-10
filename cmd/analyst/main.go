package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/openai/openai-go/v3"
)

const (
	defaultClaudeModel = "claude-opus-5-5"
	maxIterations      = 10
	fakeRentals        = 7319
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "error:", err)
		os.Exit(1)
	}
}

func run() error {
	if len(os.Args) != 2 {
		return errors.New(`usage: analyst "question"`)
	}
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tools := []tool{rentalCount}
	c, err := newChat(os.Getenv("ANALYST_PROVIDER"), os.Getenv("ANALYST_MODEL"), os.Args[1], tools)
	if err != nil {
		return err
	}
	answer, err := ask(ctx, c, tools, maxIterations)
	if err != nil {
		return err
	}
	fmt.Println(answer)
	return nil
}

// newChat starts a conversation with the provider named by ANALYST_PROVIDER. Each SDK reads
// its own key and base URL from the environment (ANTHROPIC_*, OPENAI_*).
func newChat(provider, model, question string, tools []tool) (chat, error) {
	switch provider {
	case "", "claude":
		if model == "" {
			model = defaultClaudeModel
		}
		return newClaudeChat(anthropic.NewClient(), model, question, tools), nil
	case "openai":
		if model == "" {
			return nil, errors.New("ANALYST_PROVIDER=openai needs ANALYST_MODEL")
		}
		return newOpenAIChat(openai.NewClient(), model, question, tools), nil
	}
	return nil, fmt.Errorf("ANALYST_PROVIDER is %q, want claude or openai", provider)
}

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

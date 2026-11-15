package main

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/signal"
	"syscall"

	"github.com/anthropics/anthropic-sdk-go/v2"
	"github.com/openai/openai-go/v3"
)

const (
	defaultClaudeModel = "claude-opus-5-5"
	maxIterations      = 10
	systemPrompt       = `You answer questions about a PostgreSQL database using the tools.
Base the answer only on query results. Reply in a few sentences without SQL; the SQL is shown separately.`
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

	session, err := connectMCP(ctx, cmp.Or(os.Getenv("SAFEDB_MCP"), "./safedb-mcp"))
	if err != nil {
		return err
	}
	defer session.Close()

	tools, err := mcpTools(ctx, session)
	if err != nil {
		return err
	}
	c, err := newChat(os.Getenv("ANALYST_PROVIDER"), os.Getenv("ANALYST_MODEL"), os.Args[1], tools)
	if err != nil {
		return err
	}
	answer, calls, err := ask(ctx, c, tools, maxIterations)
	if err != nil {
		return err
	}
	fmt.Println(answer)
	for _, sql := range sqlOf(calls) {
		fmt.Printf("\n-- SQL used\n%s\n", sql)
	}
	return nil
}

func sqlOf(calls []toolCall) []string {
	var sqls []string
	for _, c := range calls {
		var in struct {
			SQL string `json:"sql"`
		}
		if c.name == "query" && json.Unmarshal(c.input, &in) == nil {
			sqls = append(sqls, in.SQL)
		}
	}
	return sqls
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

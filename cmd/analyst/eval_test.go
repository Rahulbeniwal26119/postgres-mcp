package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const minPassing = 8

var evalQuestions = []struct {
	question string
	want     []string
}{
	{"How many films are in the catalog?", []string{"1000"}},
	{"How many rentals have not been returned yet?", []string{"241"}},
	{"Which film category brings in the most revenue?", []string{"action"}},
	{"Which actor appears in the most films?", []string{"gina degeneres"}},
	{"What is the most rented film?", []string{"cat coneheads"}},
	{"How many films are rated PG-13?", []string{"223"}},
	{"What is the average rental rate of a film, to the cent?", []string{"2.98"}},
	{"Which month had the most rentals?", []string{"july", "2022"}},
	{"How many customers are active?", []string{"966"}},
	{"What is the ID of the customer who has spent the most in total?", []string{"526"}},
}

func TestEval(t *testing.T) {
	if os.Getenv("ANALYST_EVAL") == "" {
		t.Skip("calls the model API, which costs money; set ANALYST_EVAL=1 to run it")
	}
	bin := filepath.Join(t.TempDir(), "safedb-mcp")
	if out, err := exec.Command("go", "build", "-o", bin, "../safedb-mcp").CombinedOutput(); err != nil {
		t.Fatalf("build safedb-mcp: %v\n%s", err, out)
	}
	session, err := connectMCP(t.Context(), bin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { session.Close() })
	tools, err := mcpTools(t.Context(), session)
	if err != nil {
		t.Fatal(err)
	}
	passed := 0
	var total usage

	for _, q := range evalQuestions {
		c, err := newChat(os.Getenv("ANALYST_PROVIDER"), os.Getenv("ANALYST_MODEL"), q.question, tools)
		if err != nil {
			t.Fatal(err)
		}
		a, err := ask(t.Context(), c, tools, maxIterations)
		total.input += a.usage.input
		total.output += a.usage.output
		if err == nil && containsAll(a.text, q.want) {
			passed += 1
			t.Logf("pass  %-66s %7d in %6d out", q.question, a.usage.input, a.usage.output)
		} else {
			t.Logf("FAIL  %-66s %7d in %6d out\n      want %q in %q, error: %v", q.question, a.usage.input, a.usage.output, q.want, a.text, err)
		}
	}
	t.Logf("%d/%d passed, %d input and %d output tokens in total", passed, len(evalQuestions), total.input, total.output)
	if passed < minPassing {
		t.Errorf("%d/%d passed, want at least %d", passed, len(evalQuestions), minPassing)
	}
}

func containsAll(answer string, want []string) bool {
	answer = strings.ToLower(strings.ReplaceAll(answer, ",", ""))
	for _, w := range want {
		if !strings.Contains(answer, w) {
			return false
		}
	}
	return true
}

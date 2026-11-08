package main

import (
	"slices"
	"strings"
	"testing"
)

func TestPolicy(t *testing.T) {
	cs := newTestSession(t)
	query := func(sql string) map[string]any { return map[string]any{"sql": sql} }

	t.Run("raw customer table is denied", func(t *testing.T) {
		msg := callToolErr(t, cs, "query", query("SELECT email FROM customer"))
		if !strings.Contains(msg, "permission denied") {
			t.Errorf("got %q, want permission denied", msg)
		}
	})

	t.Run("customer_safe masks email and phone", func(t *testing.T) {
		var out queryOutput
		callTool(t, cs, "query", query(`
			SELECT count(*) AS total,
			       count(*) FILTER (WHERE email NOT LIKE '_***@%' OR phone ~ '[0-9]{5}') AS unmasked
			FROM customer_safe`), &out)
		if len(out.Rows) != 1 || out.Rows[0][0] == "0" || out.Rows[0][1] != "0" {
			t.Errorf("want some rows and none unmasked, got [total unmasked] = %v", out.Rows)
		}
	})

	t.Run("list_tables shows only granted object", func(t *testing.T) {
		var out listTablesOutput
		callTool(t, cs, "list_tables", nil, &out)
		names := make([]string, len(out.Tables))
		for i, tb := range out.Tables {
			names[i] = tb.Name
		}

		for _, denied := range []string{"customer", "address", "staff", "customer_list", "staff_list", "sales_by_store"} {
			if slices.Contains(names, denied) {
				t.Errorf("expected %q to be denied, got %v", denied, names)
			}
		}

		if !slices.Contains(names, "customer_safe") || !slices.Contains(names, "payment") {
			t.Errorf("expected customer_safe and payment to be present, got %v", names)
		}
	})

	t.Run("describe_table hides ungranted tables", func(t *testing.T) {
		if msg := callToolErr(t, cs, "describe_table", map[string]any{"table": "customer"}); msg == "" {
			t.Error("describe_table customer: want a tool error")
		}
	})

	// Partitions are not granted; Postgres checks privileges on the parent only.
	t.Run("payment is readable through its parent", func(t *testing.T) {
		var out queryOutput
		callTool(t, cs, "query", query("SELECT count(*) FROM payment"), &out)
	})
}

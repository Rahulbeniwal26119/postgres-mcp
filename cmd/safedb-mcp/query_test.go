package main

import (
	"fmt"
	"strings"
	"testing"
	"time"
)

func TestQuery(t *testing.T) {
	cs := newTestSession(t)
	query := func(sql string) map[string]any { return map[string]any{"sql": sql} }

	t.Run("returns columns, rows and NULL", func(t *testing.T) {
		var out queryOutput
		callTool(t, cs, "query", query("SELECT 1 AS n, NULL AS missing"), &out)
		if fmt.Sprint(out.Columns) != "[n missing]" || fmt.Sprint(out.Rows) != "[[1 <nil>]]" || out.Truncated {
			t.Errorf("got %+v", out)
		}
	})

	t.Run("caps large results and stops the query", func(t *testing.T) {
		start := time.Now()
		var out queryOutput
		callTool(t, cs, "query", query("SELECT a.rental_id FROM rental a CROSS JOIN rental b"), &out) // ~256M rows
		if len(out.Rows) != maxRows || !out.Truncated {
			t.Errorf("got %d rows, truncated=%v; want %d, true", len(out.Rows), out.Truncated, maxRows)
		}
		if took := time.Since(start); took > time.Second {
			t.Errorf("took %v: the unread rows were drained instead of the query being stopped", took)
		}
	})

	t.Run("exactly maxRows is not truncated", func(t *testing.T) {
		var out queryOutput
		callTool(t, cs, "query", query(fmt.Sprintf("SELECT generate_series(1, %d)", maxRows)), &out)
		if len(out.Rows) != maxRows || out.Truncated {
			t.Errorf("got %d rows, truncated=%v; want %d, false", len(out.Rows), out.Truncated, maxRows)
		}
	})

	// The readonly role cannot INSERT either; the error text proves the transaction layer
	// blocks it on its own, so it still holds if someone points DATABASE_URL at a stronger role.
	t.Run("INSERT fails", func(t *testing.T) {
		msg := callToolErr(t, cs, "query", query("INSERT INTO language (name) VALUES ('Klingon')"))
		if !strings.Contains(msg, "read-only transaction") {
			t.Errorf("got %q, want a read-only transaction error", msg)
		}
	})

	t.Run("only one statement", func(t *testing.T) {
		if msg := callToolErr(t, cs, "query", query("SELECT 1; SELECT 2")); msg == "" {
			t.Error("two statements: want a tool error")
		}
	})

	t.Run("pg_sleep is cancelled by statement_timeout", func(t *testing.T) {
		start := time.Now()
		msg := callToolErr(t, cs, "query", query("SELECT pg_sleep(10)"))
		if !strings.Contains(msg, "statement timeout") {
			t.Errorf("got %q, want a statement timeout error", msg)
		}
		if took := time.Since(start); took > queryTimeout+time.Second {
			t.Errorf("took %v, want about %v", took, queryTimeout)
		}
	})

	t.Run("masks columns named like email, phone or address", func(t *testing.T) {
		var out queryOutput
		callTool(t, cs, "query", query(`SELECT 'MARY.SMITH@sakilacustomer.org' AS email, '6172235589' AS phone,
			       '47 MySakila Drive' AS address, 'MARY.SMITH@sakilacustomer.org' AS x`),
			&out)
		want := "[[M***@sakilacustomer.org ******5589 [redacted] MARY.SMITH@sakilacustomer.org]]"
		if got := fmt.Sprint(out.Rows); got != want {
			t.Errorf("got %s, want %s", got, want)
		}
	})
}

package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const (
	maxRows     = 500
	callTimeOut = queryTimeout + 2*time.Second
)

var queryDescription = fmt.Sprintf(
	"Run one read-only SQL statement (PostgreSQL dialect) and return the rows. "+
		"It runs in a READ ONLY transaction with a %s timeout. At most %d rows come back; "+
		"if truncated is true, aggregate or filter instead of paging. "+
		"Values are returned as text, the way psql prints them; SQL NULL is null. "+
		"Use list_tables and describe_table first to find table and column names.",
	queryTimeout,
	maxRows,
)

type queryInput struct {
	SQL string `json:"sql" jsonschema:"a single SQL statement, e.g. SELECT count(*) FROM rental"`
}

type queryOutput struct {
	Columns   []string `json:"columns"`
	Rows      [][]any  `json:"rows"`
	Truncated bool     `json:"truncated"`
}

func (t tools) query(ctx context.Context, _ *mcp.CallToolRequest, in queryInput) (*mcp.CallToolResult, queryOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, callTimeOut)
	defer cancel()

	tx, err := t.db.BeginTx(ctx, pgx.TxOptions{AccessMode: pgx.ReadOnly})
	if err != nil {
		return nil, queryOutput{}, fmt.Errorf("begin %w", err)
	}

	defer tx.Rollback(ctx)

	if _, err := tx.Exec(ctx, fmt.Sprintf("SET LOCAL statement_timeout = %d", queryTimeout.Milliseconds())); err != nil {
		return nil, queryOutput{}, fmt.Errorf("set local statement_timeout %w", err)
	}

	rows, err := tx.Query(ctx, in.SQL, pgx.QueryResultFormats{pgx.TextFormatCode})
	if err != nil {
		return nil, queryOutput{}, err
	}

	fields := rows.FieldDescriptions()
	out := queryOutput{
		Columns: make([]string, len(fields)),
		Rows:    [][]any{},
	}

	for i, fd := range fields {
		out.Columns[i] = fd.Name
	}

	for rows.Next() {
		if len(out.Rows) == maxRows {
			out.Truncated = true
			break
		}
		raw := rows.RawValues()
		row := make([]any, len(raw))
		for i, v := range raw {
			if v != nil {
				row[i] = string(v)
			}
		}
		out.Rows = append(out.Rows, row)
	}

	if out.Truncated {
		// Without this, Close would read and discard every remaining row until statement_timeout.
		// Cancelling drops the connection instead: Postgres aborts the query and the pool opens a new one.
		cancel()
	}
	rows.Close()
	if err := rows.Err(); err != nil && !out.Truncated {
		return nil, queryOutput{}, err
	}

	return nil, out, nil
}

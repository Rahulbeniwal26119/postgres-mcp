package main

import (
	"context"
	"fmt"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

const queryTimeOut = 5 * time.Second

type tools struct {
	db *pgxpool.Pool
}

type table struct {
	Schema string `json:"schema"`
	Name   string `json:"name"`
	Type   string `json:"type"`
}

type listTablesOutput struct {
	Tables []table `json:"tables"`
}

const listTableSQL = `
SELECT n.nspname AS schema, c.relname AS name,
       CASE c.relkind WHEN 'v' THEN 'view' WHEN 'm' THEN 'materialized view'
                      WHEN 'f' THEN 'foreign table' ELSE 'table' END AS type
FROM pg_class c
JOIN pg_namespace n ON n.oid = c.relnamespace
WHERE c.relkind IN ('r', 'p', 'v', 'm', 'f')
  AND NOT c.relispartition
  AND n.nspname NOT IN ('pg_catalog', 'information_schema')
  AND has_table_privilege(c.oid, 'SELECT')
ORDER BY 1, 2`

func (t tools) listTables(ctx context.Context, _ *mcp.CallToolRequest, _ struct{}) (*mcp.CallToolResult, listTablesOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeOut)
	defer cancel()

	rows, _ := t.db.Query(ctx, listTableSQL)
	tables, err := pgx.CollectRows(rows, pgx.RowToStructByName[table])
	if err != nil {
		return nil, listTablesOutput{}, fmt.Errorf("list tables: %w", err)
	}

	return nil, listTablesOutput{Tables: tables}, nil
}

type describeTableInput struct {
	Table string `json:"table" jsonschema:"table or view name, optionally schema-qualified, e.g. rental or public.rental"`
}

type column struct {
	Name     string `json:"name"`
	Type     string `json:"type"`
	Nullable bool   `json:"nullable"`
}

type describeTableOutput struct {
	Columns []column `json:"columns"`
}

const describeTableSQL = `
SELECT a.attname AS name, format_type(a.atttypid, a.atttypmod) AS type, NOT a.attnotnull AS nullable
FROM pg_attribute a
WHERE a.attrelid = to_regclass($1) AND a.attnum > 0 AND NOT a.attisdropped
ORDER BY a.attnum`

func (t tools) describeTable(ctx context.Context, _ *mcp.CallToolRequest, in describeTableInput) (*mcp.CallToolResult, describeTableOutput, error) {
	ctx, cancel := context.WithTimeout(ctx, queryTimeOut)
	defer cancel()

	rows, _ := t.db.Query(ctx, describeTableSQL, in.Table)
	cols, err := pgx.CollectRows(rows, pgx.RowToStructByName[column])
	if err != nil {
		return nil, describeTableOutput{}, fmt.Errorf("describe %q: %w", in.Table, err)
	}
	if len(cols) == 0 {
		return nil, describeTableOutput{}, fmt.Errorf("no table or view named %q; call list_tables to see what exists", in.Table)
	}

	return nil, describeTableOutput{Columns: cols}, nil
}

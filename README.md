# safedb-mcp

A read-only Postgres [MCP](https://modelcontextprotocol.io) server, written in Go.
Claude Code, Codex or any MCP client can explore a database and run SQL against it, with the
database itself deciding what the model may read. Ships with a small
[analyst agent](#analyst-agent) that uses the server to answer questions from the command line.

> **Status:** working toward v0.1.0. Server, guardrails, tracing, analyst and its
> [A2A](#a2a) interface are done; release is next.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `list_tables` | none | Tables, views and materialized views the role can read (partitions hidden) |
| `describe_table` | `table`, optionally schema-qualified | Each column's name, PostgreSQL type and nullability |
| `query` | `sql`, one statement | `columns`, `rows` (values as text, NULL as `null`), `truncated`. Columns named `email`, `phone` or `address` come back masked. |

All three are read-only (`readOnlyHint`). Errors come back as tool errors the model can read and fix.

## Quickstart

Needs Docker with Compose, Go 1.27+ (1.21+ downloads the right toolchain automatically), and Claude Code or Codex.

```bash
git clone https://github.com/Rahulbeniwal26119/postgres-mcp.git
cd postgres-mcp
docker compose up -d --wait            # Postgres with Pagila; first run takes ~1 min
go build -o safedb-mcp ./cmd/safedb-mcp
```

**Claude Code** (registers for this project only):

```bash
claude mcp add safedb -e DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila -- ./safedb-mcp
claude
```

Add `-e AUDIT_LOG="$PWD/audit.jsonl"` to record every tool call (see [Audit log](#audit-log)).

**Codex** (global config, so use an absolute path):

```bash
codex mcp add safedb --env DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila -- "$PWD/safedb-mcp"
```

Run `/mcp`, check `safedb` is connected with 3 tools, then try:

- *What tables exist?*
- *What columns does rental have?*
- *Which film categories bring in the most revenue?*
- *Show me five customers and their emails.* (Emails come back masked.)

Postgres listens on `127.0.0.1:5433` (to avoid clashing with a local 5432), localhost only.
`readonly`/`readonly` is a dev password — don't reuse it.

## Data access: allowlist and masking

safedb-mcp contains no table names and no access rules. It runs the model's SQL as the Postgres
role in `DATABASE_URL`, and Postgres decides what's visible. The policy lives in the database, so
it also protects every other client that uses the role.

### Guardrails

| Layer | Enforced by | Stops |
|---|---|---|
| Grants on the role | Postgres (`db/initdb/04-readonly-policy.sql`) | Reading any table or view not explicitly granted |
| Masking views | Postgres | Raw PII in results: the role reads the view, never the base table |
| Column-name masking | safedb-mcp | PII columns a grant exposes by mistake, if the name contains `email`, `phone` or `address` (alias defeats it) |
| `temp_file_limit` on the role | Postgres | Queries filling disk with temp files |
| `READ ONLY` transaction, rolled back after every call | safedb-mcp | Writes, `SELECT ... FOR UPDATE`, `SET`s leaking into later calls |
| 5s `statement_timeout` | safedb-mcp | Long-running queries |
| 500-row cap (`truncated: true`) | safedb-mcp | Huge results flooding the model's context |
| One statement per call | pgx (extended protocol) | `COMMIT; ...` escapes from the read-only transaction |

### The demo policy (Pagila)

`db/initdb/04-readonly-policy.sql`:

- Grants `SELECT` on 19 tables/views. Everything else denied, including future tables.
- Does not grant `customer`, `address`, `staff` (emails, phones, addresses, password hashes), or views built on them.
- Adds `customer_safe`: customer rows with email masked to first letter + domain, phone masked to last four digits.

```sql
SELECT email FROM customer;               -- ERROR: permission denied for table customer
SELECT email, phone FROM customer_safe;   -- M***@sakilacustomer.org | *******4290
```

`list_tables` and `describe_table` only show objects the role can read.

### Changing the policy

Edit the file, then apply it (safe to re-run):

```bash
docker compose exec -T postgres psql -U postgres -d pagila -v ON_ERROR_STOP=1 < db/initdb/04-readonly-policy.sql
```

Init scripts only run on an empty volume; this applies changes to a running database. Grants take
effect immediately. `temp_file_limit` applies to new connections, so reconnect the server (`/mcp`).

### Using your own database

Write a policy following the same pattern, as a superuser or table owner:

```sql
CREATE ROLE mcp_readonly LOGIN PASSWORD 'change-me';
GRANT CONNECT ON DATABASE shop TO mcp_readonly;
GRANT USAGE ON SCHEMA public TO mcp_readonly;
ALTER ROLE mcp_readonly SET temp_file_limit = '100MB';

-- Non-sensitive tables: grant directly.
GRANT SELECT ON orders, order_items, products TO mcp_readonly;

-- Tables with personal data: grant a masking view, never the table.
CREATE VIEW users_safe AS
SELECT u.id, u.created_at, u.country,
       left(u.email, 1) || '***@' || split_part(u.email, '@', 2) AS email
FROM users u;
GRANT SELECT ON users_safe TO mcp_readonly;
```

Point the server at that role: `DATABASE_URL=postgres://mcp_readonly@your-host/shop`. Keep the
password in `~/.pgpass`, not in the URL — MCP clients store the URL in plain text.

Rules:

- **Grant the view, not the table under it.** Views read with the owner's rights.
- **Don't use `security_invoker = true` on masking views.** They'd check caller rights on base tables and return `permission denied`.
- **Don't use `ALTER DEFAULT PRIVILEGES ... GRANT SELECT`.** New tables would become readable automatically.
- **Never point `DATABASE_URL` at a superuser or table owner.** They bypass grants.
- **Join lookup tables with `LEFT JOIN` on their PK** inside masking views, so Postgres can skip the join when unused.
- **Filter on unmasked columns** (ids, dates). Filtering on masked text can't use an index.

### What masking covers

- **safedb-mcp also masks result columns by name**, as a second layer. Columns named `email` / `phone` get `customer_safe`-style masks; `address` becomes `[redacted]`. Alias (`SELECT email AS x`) or expression (`SELECT lower(email)`) defeats it. It catches mistakes, not deliberate work-arounds. **The database grants are the real protection.**
- Name matching also hides harmless columns: `store.address_id` → `[redacted]`.
- Results reach your LLM provider. Masking decides which personal data goes with them.
- The role can't probe masked values with `WHERE email LIKE 'A%'` — Postgres requires `SELECT` on every column a query mentions.
- Table and column names are visible via `pg_catalog`. Only the data is protected.
- In Pagila, emails are `FIRST.LAST@sakilacustomer.org` and names aren't masked, so masked emails are easy to guess. Decide column by column for real data.

### Debugging a specific customer

Masking blocks browsing, not targeted work:

- **Start from the id** (ticket, admin tool), then ask about that id.
- **Need real emails for support?** Create a separate role with raw access, register it as a second server, same binary, different `DATABASE_URL`. The name-mask still applies.
  ```bash
  claude mcp add safedb-support -e DATABASE_URL=postgres://support_ro@localhost:5433/pagila -- ./safedb-mcp
  ```
- **Engineers who need raw data** use their own credentials in `psql`, ideally against a read replica.

## Audit log

One JSON line per tool call: the tool, its arguments as the model sent them (the SQL, for `query`),
`duration_ms`, `rows` and `truncated` for queries, and `error` on failure.

```json
{"time":"...","level":"INFO","msg":"tool call","tool":"query","args":{"sql":"SELECT count(*) FROM rental"},"duration_ms":42.4,"rows":1,"truncated":false}
{"time":"...","level":"INFO","msg":"tool call","tool":"query","args":{"sql":"SELECT email FROM customer"},"duration_ms":13.7,"error":"ERROR: permission denied for table customer (SQLSTATE 42501)"}
```

Logs go to stderr. Claude Code and Codex don't keep a server's stderr — set `AUDIT_LOG` to also append to a file:

```bash
claude mcp add safedb -e DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  -e AUDIT_LOG="$PWD/audit.jsonl" -- ./safedb-mcp
```

- **Absolute path, double quotes.** Single quotes save literal `$PWD` and the server fails with `open AUDIT_LOG: no such file or directory`.
- Append-only, mode `0600`. `audit.jsonl` is in `.gitignore`.
- Contains the model's SQL (which may include typed values), never query results.
- **Connection string is never logged.** An invalid `DATABASE_URL` is reported without repeating the URL.

## Analyst agent

`cmd/analyst` answers one question from the command line. Hand-written agent loop for the Claude
or OpenAI API: starts `./safedb-mcp` as a child process, gives the model the server's tools, runs
the tool calls, and prints the answer with the SQL behind it.

```bash
go build -o safedb-mcp ./cmd/safedb-mcp                 # the analyst runs this binary
read -s ANTHROPIC_API_KEY && export ANTHROPIC_API_KEY   # paste the key; stays out of history
DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  go run ./cmd/analyst "Which film category brings in the most revenue?"
```

```
Action brings in the most revenue, $26,505.44, just ahead of Animation at $26,460.31.

-- SQL used
SELECT c.name, sum(p.amount) AS revenue FROM payment p JOIN rental r USING (rental_id) JOIN inventory i USING (inventory_id) JOIN film_category fc USING (film_id) JOIN category c USING (category_id) GROUP BY c.name ORDER BY revenue DESC LIMIT 3
```

The model writes its own SQL, so your wording and query will differ.

- **stdout**: the answer, then every successful query under `-- SQL used`. Failed-and-retried queries aren't listed.
- **stderr**: safedb-mcp's audit log, one JSON line per tool call.
- **Failed tool calls go back to the model** as error results — it reads, fixes, retries.
- **Limits:** 10 model requests per question, then `iteration cap reached`. Each tool call times out after 30s.
- The model is told to answer only from query results. Those go to your LLM provider; [masking](#data-access-allowlist-and-masking) decides which personal data reaches it.

| Variable | Default | |
|---|---|---|
| `ANALYST_PROVIDER` | `claude` | `claude` or `openai` |
| `ANALYST_MODEL` | `claude-opus-5-5` | Required with `openai` |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | | Read by each provider's SDK |
| `OPENAI_BASE_URL` | | Any OpenAI-compatible server (Ollama, OpenRouter, vLLM) |
| `SAFEDB_MCP` | `./safedb-mcp` | Path to the server binary. **Rebuild after changing `cmd/safedb-mcp`** — the analyst runs the binary, not the source. |
| `DATABASE_URL`, `AUDIT_LOG`, `OTEL_*` | | Passed on to safedb-mcp via the analyst's environment |

## A2A

The analyst can also run as an [A2A](https://a2a-protocol.org) agent, so other agents can send it
questions over HTTP. `examples/a2a-client` asks one question and prints the answer + SQL. Use two
terminals, both in the repo directory.

**Terminal 1, the agent** (runs until Ctrl-C):

```bash
docker compose up -d --wait                              # Postgres, if needed
go build -o safedb-mcp ./cmd/safedb-mcp
read -s ANTHROPIC_API_KEY && export ANTHROPIC_API_KEY
openssl rand -hex 32                                     # make a token, copy it
read -s ANALYST_A2A_TOKEN && export ANALYST_A2A_TOKEN
ANALYST_A2A_ADDR=127.0.0.1:9000 \
DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  go run ./cmd/analyst
```

Ready when it prints `A2A agent card at http://127.0.0.1:9000/.well-known/agent-card.json`.

**Terminal 2, the client:**

```bash
read -s ANALYST_A2A_TOKEN && export ANALYST_A2A_TOKEN    # same token
go run ./examples/a2a-client "Which film category brings in the most revenue?"
```

Output matches the command-line analyst's. A spinner runs on stderr while the agent works. The client gives up after 2 minutes.

For OpenAI, export `OPENAI_API_KEY` in terminal 1 and add `ANALYST_PROVIDER=openai ANALYST_MODEL=gpt-5` to the agent's command.

- **Agent card is public** at `/.well-known/agent-card.json`. It names the `/invoke` endpoint and requires a bearer token. Wrong/missing token → `401`. Agent won't start without `ANALYST_A2A_TOKEN`.
- **Reply is a task** whose artifact has two parts: the answer as text, queries as data (`{"sql": [...]}`).
- **At most 4 questions run at once.** Extra ones are rejected, not queued.
- **Plain HTTP**, so keep it on `127.0.0.1`. Put it behind a TLS-terminating proxy for remote access.

| Variable | Used by | Default | |
|---|---|---|---|
| `ANALYST_A2A_ADDR` | agent | | Address to listen on. When set, the analyst serves A2A instead of answering one question. |
| `ANALYST_A2A_TOKEN` | both | | Bearer token. Required. |
| `ANALYST_A2A_URL` | client | `http://localhost:9000` | Agent location; client fetches the agent card from here. |

## Tracing

safedb-mcp, the analyst and the A2A client export OpenTelemetry traces when
`OTEL_EXPORTER_OTLP_ENDPOINT` is set. Without it, nothing is traced. Standard `OTEL_*` variables
choose the backend. `compose.yaml` includes Jaeger behind a profile:

```bash
docker compose --profile tracing up -d --wait   # Jaeger UI at :16686, OTLP on :4318
```

**With the analyst**, each question is one trace across both programs:

```bash
OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 \
DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  go run ./cmd/analyst "Which film category brings in the most revenue?"
```

```
invoke_agent analyst                     analyst      the whole question
├─ anthropic.messages.create             analyst      each model request: model, tokens, stop reason
├─ tools/call query                      safedb-mcp   each tool call
│  └─ BEGIN / SET / SELECT / ROLLBACK    safedb-mcp   each SQL statement, with its text
└─ ...
```

- Analyst sends the trace context to safedb-mcp as `traceparent` in each tool call's `_meta`. Audit lines carry the same `trace_id`.
- With `ANALYST_PROVIDER=openai`, model requests are HTTP spans (OpenAI SDK has no tracing).
- **Don't set `OTEL_SERVICE_NAME`** — safedb-mcp inherits it and would report under the analyst's name.

**With the [A2A client](#a2a)**, export `OTEL_EXPORTER_OTLP_ENDPOINT` in both terminals. In Jaeger, pick service `a2a-client`:

```
invoke_agent safedb analyst              a2a-client   the whole question, as the client sees it
└─ HTTP POST                             a2a-client   the A2A request; carries traceparent
   └─ POST /invoke                       analyst
      └─ invoke_agent analyst            analyst
         ├─ anthropic.messages.create    analyst
         └─ tools/call query             safedb-mcp
            └─ BEGIN / SET / SELECT / ROLLBACK
```

Requests rejected for a wrong token are traced too, with errors on the client's spans and on `POST /invoke`.

**With Claude Code or Codex**, add the variable when registering the server. Unless the client sends a trace context in `_meta`, each tool call is its own trace:

```bash
claude mcp add safedb -e DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  -e OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 -- ./safedb-mcp
```

**Span contents:** SQL text (`db.query.text`), never query results. The Claude SDK leaves prompts
and answers out of spans by default. Don't set `ANTHROPIC_OPEN_TELEMETRY_TRACES_CONTENT_MODE=content`
— it copies prompts, including query results, into your tracing backend. If the backend is down,
safedb-mcp logs one warning and carries on; the analyst prints `flush traces: ...` on exit.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `safedb` shows as failed in `/mcp` | Check `DATABASE_URL` is set in the `claude mcp add` command and `./safedb-mcp` exists (rebuild). With `AUDIT_LOG`, the reason is on the last line. |
| `DATABASE_URL is not a valid Postgres connection string` | Format: `postgres://user:password@host:port/dbname`. |
| `open AUDIT_LOG: no such file or directory` | Directory missing, or `$PWD` was saved as literal (single quotes). Re-add with `-e AUDIT_LOG="$PWD/audit.jsonl"`. |
| `password authentication failed for user "readonly"` | URL password doesn't match `db/initdb/03-readonly.sql`. URL-encode specials (`@` → `%40`). |
| `connect: connection refused` | Postgres isn't running: `docker compose up -d --wait`. Next tool call reconnects. |
| Edited `db/initdb/*.sql` but nothing changed | Init scripts only run on empty volume. Re-apply the policy (above), or reset with `docker compose down -v && docker compose up -d --wait`. |
| `could not write to file ... No space left on device` | Docker's disk is full: `docker system df`. |
| Port 5433 in use | Change host port in `compose.yaml` and `DATABASE_URL`. |
| `go test`: `rootless Docker not found` | Docker isn't running, or on Colima the two vars in [Development](#development) aren't set. `go test -short ./...` skips Docker tests. |
| Analyst: `start MCP server ... no such file or directory` | Build the server and run the analyst from the repo root, or set `SAFEDB_MCP`. |
| Analyst: `start MCP server ... EOF` | Server exited at startup. The JSON line above on stderr says why, usually `DATABASE_URL is not set`. |
| Analyst: `iteration cap reached: still calling tools after 10 requests` | Model kept calling tools without answering. Ask narrower. |
| Analyst: `ANALYST_A2A_TOKEN must be set when ANALYST_A2A_ADDR is` | Export the token in the agent's terminal. |
| A2A client: `fetch the agent card: ... connection refused` | Agent isn't running, or is on another address. Start it or set `ANALYST_A2A_URL`. |
| A2A client: `send message: unexpected HTTP status: 401 Unauthorized` | Token not exported in the client's terminal, or doesn't match the agent's. |
| A2A client: `task TASK_STATE_FAILED: ...` | Agent accepted the question but failed. Reason follows. Provider `401` = API key not exported in agent terminal. |
| No traces in Jaeger | Start Jaeger with `docker compose --profile tracing up -d --wait`, and set `OTEL_EXPORTER_OTLP_ENDPOINT` where the program runs. |

## Development

```bash
gofmt -l . && go vet ./...
go test ./...          # throwaway Postgres in Docker; ~10s
go test -short ./...   # skips database tests
```

`go test` uses [testcontainers-go](https://golang.testcontainers.org) to start a fresh Postgres,
load `db/initdb` and remove it afterwards. Tests check the committed schema and policy, never read
`DATABASE_URL`. Data is in-memory. About 5s of the 10 is one test waiting for the 5s statement
timeout on purpose. Analyst tests use fake model APIs and an in-memory MCP server, no API key needed.

On Colima, point testcontainers at Colima's Docker (Docker Desktop and Linux need nothing extra):

```bash
export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```

Flags: `-v` per-test output, `-count=1` to skip cache, `-cover` for coverage. A `testcontainers/ryuk`
container stays up for ~15s after a run to clean up, then removes itself.

## Project layout

```
cmd/safedb-mcp/   the server: main.go (wiring), tools.go, query.go, redact.go, audit.go, telemetry.go, tests
cmd/analyst/      the agent: main.go (CLI), agent.go (loop), claude.go, openai.go, mcp.go (MCP client), a2a.go (A2A server), telemetry.go, tests
examples/         a2a-client: asks the analyst one question over A2A
db/initdb/        run by Postgres on first start: Pagila schema + data, readonly role (03), access policy (04)
compose.yaml      Postgres 18 with pgvector, Jaeger under the tracing profile; bound to localhost
```

To remove: `claude mcp remove safedb`, then `docker compose down -v`.

## License

MIT, see [LICENSE](LICENSE). Pagila ships under its own license — see [db/PAGILA-LICENSE.txt](db/PAGILA-LICENSE.txt).

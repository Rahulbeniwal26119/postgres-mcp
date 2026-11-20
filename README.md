# safedb-mcp

A read-only Postgres [MCP](https://modelcontextprotocol.io) server, written in Go.
It lets Claude Code, Codex or any MCP client explore a database and run SQL against it,
with the database itself deciding what the model may read. It comes with a small
[analyst agent](#analyst-agent) that uses the server to answer questions from the command line.

> **Status:** working toward v0.1.0. The server, its guardrails, tracing, the analyst and its
> [A2A](#a2a) interface are done; the release is next.

## Tools

| Tool | Input | Returns |
|---|---|---|
| `list_tables` | none | Tables, views and materialized views the role can read (partitions hidden) |
| `describe_table` | `table`, optionally schema-qualified (`rental`, `public.rental`) | Each column's name, PostgreSQL type and whether it can be NULL |
| `query` | `sql`, one statement | `columns`, `rows` (values as text, the way psql prints them; NULL as `null`), `truncated`. Columns named like `email`, `phone` or `address` come back masked. |

All three are marked read-only (`readOnlyHint`). Errors, including SQL errors, come back as
tool errors the model can read and fix.

## Quickstart

You need Docker with Compose, Go 1.27 or newer (Go 1.21+ downloads the right toolchain
automatically), and Claude Code or the Codex CLI.

```bash
git clone https://github.com/Rahulbeniwal26119/postgres-mcp.git
cd postgres-mcp
docker compose up -d --wait            # Postgres with the Pagila sample database
go build -o safedb-mcp ./cmd/safedb-mcp
```

The first `docker compose up` loads Pagila, which takes about a minute. `--wait` returns once
Postgres is ready.

**Claude Code** (run from the repo directory; the server is registered for this project only):

```bash
claude mcp add safedb -e DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila -- ./safedb-mcp
claude
```

To keep a record of every tool call, also pass `-e AUDIT_LOG="$PWD/audit.jsonl"`
(see [Audit log](#audit-log)).

**Codex** (its config is global, so give it an absolute path):

```bash
codex mcp add safedb --env DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila -- "$PWD/safedb-mcp"
```

Run `/mcp` and check that `safedb` is connected with 3 tools. Then try:

- *What tables exist?*
- *What columns does rental have?*
- *Which film categories bring in the most revenue?*
- *Show me five customers and their emails.* The emails come back masked; see below.

Postgres listens on `127.0.0.1:5433` (so it doesn't clash with a local Postgres on 5432),
bound to localhost only.
`readonly`/`readonly` is a local development password. Don't reuse it anywhere else.

## Data access: allowlist and masking

safedb-mcp contains no table names and no access rules. It runs the SQL the model writes,
as the Postgres role in `DATABASE_URL`, and Postgres decides what that role can see.
The policy lives in the database, so it also protects every other client that uses the role.

### Guardrails

| Layer | Enforced by | Stops |
|---|---|---|
| Grants on the role | Postgres (`db/initdb/04-readonly-policy.sql`) | Reading any table or view that isn't explicitly granted |
| Masking views | Postgres | Raw PII in results: the role reads the view, never the base table |
| Column-name masking | safedb-mcp | PII columns a grant exposes by mistake, if the name contains `email`, `phone` or `address` (an alias gets past it) |
| `temp_file_limit` on the role | Postgres | Queries filling the disk with temporary files |
| `READ ONLY` transaction, rolled back after every call | safedb-mcp | Writes, `SELECT ... FOR UPDATE`, and `SET`s leaking into later calls |
| 5s `statement_timeout` | safedb-mcp | Long-running queries |
| 500-row cap (`truncated: true`) | safedb-mcp | Huge results flooding the model's context |
| One statement per call | pgx (extended protocol) | `COMMIT; ...` escapes from the read-only transaction |

### The demo policy (Pagila)

`db/initdb/04-readonly-policy.sql`:

- Grants `SELECT` on 19 tables and views. Everything else is denied, including tables created later.
- Does not grant `customer`, `address` or `staff` (emails, phones, street addresses, password hashes),
  or the views built on them (`customer_list`, `staff_list`, `sales_by_store`).
- Adds `customer_safe`: customer rows with the email masked to its first letter and domain,
  and the phone masked to its last four digits.

```sql
SELECT email FROM customer;               -- ERROR: permission denied for table customer
SELECT email, phone FROM customer_safe;   -- M***@sakilacustomer.org | *******4290
```

`list_tables` and `describe_table` only show objects the role can read.

### Changing the policy

Edit the file, then apply it. It runs in one transaction and is safe to re-run:

```bash
docker compose exec -T postgres psql -U postgres -d pagila -v ON_ERROR_STOP=1 < db/initdb/04-readonly-policy.sql
```

Init scripts only run when the database volume is empty; this command applies changes to a
running database. Grants take effect immediately. `temp_file_limit` applies to new connections,
so reconnect the server (`/mcp` in Claude Code).

### Using your own database

`04-readonly-policy.sql` is Pagila's policy. For your database, write your own following the
same pattern, as a superuser or the owner of the tables:

```sql
CREATE ROLE mcp_readonly LOGIN PASSWORD 'change-me';
GRANT CONNECT ON DATABASE shop TO mcp_readonly;
GRANT USAGE ON SCHEMA public TO mcp_readonly;
ALTER ROLE mcp_readonly SET temp_file_limit = '100MB';

-- Tables with nothing sensitive: grant them directly.
GRANT SELECT ON orders, order_items, products TO mcp_readonly;

-- Tables with personal data: grant a masking view, never the table.
CREATE VIEW users_safe AS
SELECT u.id, u.created_at, u.country,
       left(u.email, 1) || '***@' || split_part(u.email, '@', 2) AS email
FROM users u;
GRANT SELECT ON users_safe TO mcp_readonly;
```

Then point the server at that role: `DATABASE_URL=postgres://mcp_readonly@your-host/shop`.
Keep the password in `~/.pgpass` rather than in the URL, because MCP client configs store
the URL in plain text.

Rules:

- **Grant the view, not the table under it.** A view reads its tables with its owner's rights,
  which is what lets the role see masked columns without access to the raw ones.
- **Don't create masking views with `security_invoker = true`.** The view would then check the
  caller's rights on the base table and return `permission denied`.
- **Don't use `ALTER DEFAULT PRIVILEGES ... GRANT SELECT`.** New tables would become readable
  automatically, and the allowlist would stop being one.
- **Never point `DATABASE_URL` at a superuser or a table owner.** They bypass grants, so nothing
  would be masked or denied.
- **Join lookup tables with `LEFT JOIN` on their primary key** inside masking views, so Postgres
  can skip the join when a query doesn't use those columns.
- **Filter on unmasked columns** (ids, dates). A filter on a masked column compares masked text
  and can't use an index.

### What masking does and doesn't cover

- **safedb-mcp also masks result columns by name, as a second layer.** Columns whose name contains
  `email` or `phone` get the same masks as `customer_safe` (first letter and domain; last four digits),
  and columns whose name contains `address` come back as `[redacted]`. It only sees the result column's
  name, so an alias (`SELECT email AS x`) or an expression (`SELECT lower(email)`) gets past it.
  It catches mistakes, such as an accidental grant on a raw table, not someone deliberately
  working around it. **The database grants are the real protection.**
- Name matching also hides harmless columns: `store.address_id` comes back as `[redacted]`.
- Query results are sent to your LLM provider. Masking decides which personal data reaches it;
  unmasked columns (in the demo, first and last names) are sent as they are.
- The role can't probe masked values with `WHERE email LIKE 'A%'`. Postgres requires `SELECT`
  on every column a query mentions, including columns used only in `WHERE`.
- Table and column names are visible through `pg_catalog`. Only the data is protected.
- In Pagila, emails are `FIRST.LAST@sakilacustomer.org` and names aren't masked, so masked emails
  are easy to guess. Real data is rarely this predictable; decide column by column.

### Debugging a specific customer

Masking blocks browsing, not targeted work:

- **Start from the id.** Get `customer_id` from your admin tool or the ticket, and ask about that id.
- **Need real emails for support work?** Create a separate role that can read the raw table and
  register it as a second server: same binary, different `DATABASE_URL`. Do this only if your
  company allows that data to go to your LLM provider. The second layer still masks columns
  named like `email`, `phone` or `address` in that server's results.
  ```bash
  claude mcp add safedb-support -e DATABASE_URL=postgres://support_ro@localhost:5433/pagila -- ./safedb-mcp
  ```
- **Engineers who need raw data** use their own credentials in `psql`, ideally against a read replica.

## Audit log

Every tool call writes one JSON line: the tool, its arguments as the model sent them (the SQL,
for `query`), `duration_ms`, `rows` and `truncated` for queries, and `error` when the call failed.

```json
{"time":"2026-11-09T14:23:55.416+05:30","level":"INFO","msg":"tool call","tool":"query","args":{"sql":"SELECT count(*) FROM rental"},"duration_ms":42.403125,"rows":1,"truncated":false}
{"time":"2026-11-09T14:23:57.861+05:30","level":"INFO","msg":"tool call","tool":"query","args":{"sql":"SELECT email FROM customer"},"duration_ms":13.731,"error":"ERROR: permission denied for table customer (SQLSTATE 42501)"}
```

Logs go to stderr, but Claude Code and Codex don't keep a server's stderr. Set `AUDIT_LOG`
to also append them to a file:

```bash
claude mcp add safedb -e DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  -e AUDIT_LOG="$PWD/audit.jsonl" -- ./safedb-mcp
```

- **Use an absolute path.** Your shell expands `$PWD` when you run `claude mcp add`. A relative
  path would resolve against whichever directory the client starts the server in.
- **Use double quotes.** With single quotes the literal text `$PWD` is saved, and the server
  fails to start with `open AUDIT_LOG: no such file or directory`.
- The file is only ever appended to, is created with mode `0600`, and `audit.jsonl` is in `.gitignore`.
- It contains the model's SQL, which can include values the model typed (a name in a `WHERE`,
  for example), but never query results.
- **The connection string is never logged.** An invalid `DATABASE_URL` is reported as
  `DATABASE_URL is not a valid Postgres connection string`, without repeating the URL.

## Analyst agent

`cmd/analyst` answers one question about the database from the command line. It's a
hand-written agent loop for the Claude or OpenAI API: it starts `./safedb-mcp` as a child
process, gives the model the server's tools, runs the tool calls the model asks for, and prints
the answer with the SQL behind it.

```bash
go build -o safedb-mcp ./cmd/safedb-mcp                 # the analyst runs this binary
read -s ANTHROPIC_API_KEY && export ANTHROPIC_API_KEY   # paste the key; it stays out of shell history
DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  go run ./cmd/analyst "Which film category brings in the most revenue?"
```

```
Action brings in the most revenue, $26,505.44, just ahead of Animation at $26,460.31.

-- SQL used
SELECT c.name, sum(p.amount) AS revenue FROM payment p JOIN rental r USING (rental_id) JOIN inventory i USING (inventory_id) JOIN film_category fc USING (film_id) JOIN category c USING (category_id) GROUP BY c.name ORDER BY revenue DESC LIMIT 3
```

The model writes its own SQL, so your wording and query will differ.

- **stdout** has the answer, then every query that succeeded, each under `-- SQL used`.
  The SQL comes from the calls that actually ran, not from the model's text. A query that
  failed and was retried isn't listed.
- **stderr** has safedb-mcp's log, one JSON line per tool call (see [Audit log](#audit-log)),
  so you can watch the SQL as it runs.
- **A failed tool call goes back to the model** as an error result: after a wrong column or a
  denied table, it reads the error, fixes the SQL and tries again.
- **Limits:** at most 10 model requests per question, then it stops with
  `iteration cap reached`. Each tool call times out after 30 seconds.
- The model is told to answer only from query results. Those results go to your LLM provider,
  as they do with Claude Code; [masking](#data-access-allowlist-and-masking) decides which
  personal data reaches it.

| Variable | Default | |
|---|---|---|
| `ANALYST_PROVIDER` | `claude` | `claude` or `openai` |
| `ANALYST_MODEL` | `claude-opus-5-5` | Required with `openai` |
| `ANTHROPIC_API_KEY`, `OPENAI_API_KEY` | | Read by each provider's SDK |
| `OPENAI_BASE_URL` | | Any OpenAI-compatible server, such as Ollama, OpenRouter or vLLM |
| `SAFEDB_MCP` | `./safedb-mcp` | Path to the server binary. **Rebuild it after changing `cmd/safedb-mcp`**: the analyst runs the binary, not the source. |
| `DATABASE_URL`, `AUDIT_LOG`, `OTEL_*` | | Passed on to safedb-mcp, which inherits the analyst's environment |

## A2A

The analyst can also run as an [A2A](https://a2a-protocol.org) agent, so other agents can send
it questions over HTTP. `examples/a2a-client` is a small client that asks one question and
prints the answer with the SQL behind it. Use two terminals, both in the repo directory.

**Terminal 1, the agent.** It keeps running until you press Ctrl-C.

```bash
docker compose up -d --wait                              # Postgres, if it isn't running
go build -o safedb-mcp ./cmd/safedb-mcp                  # the analyst runs this binary
read -s ANTHROPIC_API_KEY && export ANTHROPIC_API_KEY    # paste the key
openssl rand -hex 32                                     # make a token, then copy it
read -s ANALYST_A2A_TOKEN && export ANALYST_A2A_TOKEN    # paste the token
ANALYST_A2A_ADDR=127.0.0.1:9000 \
DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  go run ./cmd/analyst
```

It's ready when it prints `A2A agent card at http://127.0.0.1:9000/.well-known/agent-card.json`.

**Terminal 2, the client:**

```bash
read -s ANALYST_A2A_TOKEN && export ANALYST_A2A_TOKEN    # paste the same token
go run ./examples/a2a-client "Which film category brings in the most revenue?"
```

The output looks the same as the command-line analyst's: the answer, then each query under
`-- SQL used`. A spinner runs on stderr while the agent works. The client gives up after 2 minutes.

For OpenAI, export `OPENAI_API_KEY` in terminal 1 instead, and add
`ANALYST_PROVIDER=openai ANALYST_MODEL=gpt-5` to the agent's command.

- **The agent card is public**, at `/.well-known/agent-card.json`. It tells the client where to
  send messages (`/invoke`) and that they need a bearer token. A request to `/invoke` without
  the right token gets `401`. The agent won't start without `ANALYST_A2A_TOKEN`.
- **The reply is a task** whose artifact has two parts: the answer as text, and the queries as
  data, `{"sql": [...]}`.
- **At most 4 questions run at once.** More are rejected, not queued.
- **The agent serves plain HTTP**, so keep it on `127.0.0.1`. To reach it from another machine,
  put it behind a proxy that terminates TLS.

| Variable | Used by | Default | |
|---|---|---|---|
| `ANALYST_A2A_ADDR` | agent | | Address to listen on. When set, the analyst serves A2A instead of answering one question. |
| `ANALYST_A2A_TOKEN` | both | | Bearer token the client sends and the agent checks. Required. |
| `ANALYST_A2A_URL` | client | `http://localhost:9000` | Where the agent runs. The client fetches the agent card from here. |

## Tracing

safedb-mcp, the analyst and the A2A client export OpenTelemetry traces when
`OTEL_EXPORTER_OTLP_ENDPOINT` is set.
Without it, nothing is traced and nothing leaves the process. The standard `OTEL_*` variables
choose the backend. `compose.yaml` includes Jaeger behind a profile:

```bash
docker compose --profile tracing up -d --wait   # Jaeger UI at http://localhost:16686, OTLP on localhost:4318
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

- The analyst sends the trace context to safedb-mcp as `traceparent` in each tool call's `_meta`.
  The server's audit lines carry the same `trace_id`, so you can go from a log line to its trace.
- With `ANALYST_PROVIDER=openai`, model requests are HTTP spans (method, URL, status), because
  the OpenAI SDK has no tracing of its own.
- **Don't set `OTEL_SERVICE_NAME`.** safedb-mcp inherits it and would report under the same
  name as the analyst.

**With the [A2A client](#a2a)**, export `OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318` in
both terminals. Each question is then one trace across all three programs. In Jaeger, pick the
service `a2a-client`:

```
invoke_agent safedb analyst              a2a-client   the whole question, as the client sees it
└─ HTTP POST                             a2a-client   the A2A request; carries traceparent
   └─ POST /invoke                       analyst      the same request, on the agent's side
      └─ invoke_agent analyst            analyst
         ├─ anthropic.messages.create    analyst
         └─ tools/call query             safedb-mcp
            └─ BEGIN / SET / SELECT / ROLLBACK
```

A request rejected for a wrong token is traced too, with errors on the client's spans and on
`POST /invoke`.

**With Claude Code or Codex**, add the variable when you register the server. Unless the client
sends a trace context in `_meta`, each tool call is its own trace.

```bash
claude mcp add safedb -e DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila \
  -e OTEL_EXPORTER_OTLP_ENDPOINT=http://localhost:4318 -- ./safedb-mcp
```

**What spans contain:** the SQL text (`db.query.text`), but never query results. The Claude SDK
leaves prompts and answers out of its spans by default. Don't set
`ANTHROPIC_OPEN_TELEMETRY_TRACES_CONTENT_MODE=content`: that would copy prompts, including query
results, into your tracing backend. If the backend is down, safedb-mcp logs one warning and
carries on, and the analyst prints `flush traces: ...` when it exits.

## Troubleshooting

| Symptom | Fix |
|---|---|
| `safedb` shows as failed in `/mcp` | Check that `DATABASE_URL` is set in the `claude mcp add` command and that `./safedb-mcp` exists (rebuild it). With `AUDIT_LOG` set, the reason is in the last line of the file. |
| `DATABASE_URL is not a valid Postgres connection string` | Check the format: `postgres://user:password@host:port/dbname`. The server doesn't repeat the URL back, so it never ends up in a log. |
| `open AUDIT_LOG: no such file or directory` | The directory doesn't exist, or the path was saved in single quotes so `$PWD` wasn't expanded. Re-add with `-e AUDIT_LOG="$PWD/audit.jsonl"`. |
| `password authentication failed for user "readonly"` | The password in `DATABASE_URL` doesn't match `db/initdb/03-readonly.sql`. URL-encode special characters (`@` becomes `%40`). |
| `connect: connection refused` | Postgres isn't running: `docker compose up -d --wait`. The next tool call reconnects; no restart needed. |
| Edited `db/initdb/*.sql` but nothing changed | Init scripts only run on an empty volume. Re-apply the policy (above), or reset everything with `docker compose down -v && docker compose up -d --wait`. |
| `could not write to file ... No space left on device` | Docker's disk is full. Check with `docker system df`. |
| Port 5433 already in use | Change the host port in `compose.yaml` and in `DATABASE_URL`. |
| `go test`: `rootless Docker not found` / `failed to create Docker provider` | Docker isn't running, or on Colima the two variables in [Development](#development) aren't set. `go test -short ./...` runs the tests that don't need Docker. |
| Analyst: `start MCP server "./safedb-mcp": fork/exec ./safedb-mcp: no such file or directory` | Build the server (`go build -o safedb-mcp ./cmd/safedb-mcp`) and run the analyst from the repo root, or set `SAFEDB_MCP` to the binary's path. |
| Analyst: `start MCP server ... calling "initialize": client is closing: EOF` | The server exited at startup. The JSON line above it on stderr says why, usually `DATABASE_URL is not set`. |
| Analyst: `iteration cap reached: still calling tools after 10 requests` | The model kept calling tools without answering. Ask a narrower question. |
| Analyst: `ANALYST_A2A_TOKEN must be set when ANALYST_A2A_ADDR is` | Export the token in the agent's terminal (see [A2A](#a2a)). |
| A2A client: `fetch the agent card: ... connection refused` | The agent isn't running, or runs on another address. Start it with `ANALYST_A2A_ADDR`, or point the client at it with `ANALYST_A2A_URL`. |
| A2A client: `send message: unexpected HTTP status: 401 Unauthorized` | The token isn't exported in the client's terminal, or doesn't match the agent's. |
| A2A client: `task TASK_STATE_FAILED: ...` | The agent took the question but couldn't answer it. The reason follows. A provider `401` means the API key isn't exported in the agent's terminal. |
| No traces in Jaeger | Start Jaeger with `docker compose --profile tracing up -d --wait`, and check that `OTEL_EXPORTER_OTLP_ENDPOINT` is set where the program runs. For Claude Code, that means in the `claude mcp add` command. |

## Development

```bash
gofmt -l . && go vet ./...
go test ./...          # starts a throwaway Postgres in Docker; about 10 seconds
go test -short ./...   # skips the database tests: no Docker needed
```

`go test` uses [testcontainers-go](https://golang.testcontainers.org) to start a fresh Postgres,
load `db/initdb` (Pagila, the readonly role and the access policy) and remove it afterwards.
The tests check the schema and policy committed in the repo, not your local database, and never
read `DATABASE_URL`. The data lives in memory, so nothing is written to Docker's disk.
About 5 of the 10 seconds is one test waiting for the 5s statement timeout on purpose.
The analyst's tests use fake model APIs and an in-memory MCP server, so they need no API key.

On Colima, point testcontainers at Colima's Docker (for example in `~/.zshrc`).
Docker Desktop and Linux need nothing extra.

```bash
export DOCKER_HOST="unix://$HOME/.colima/default/docker.sock"
export TESTCONTAINERS_DOCKER_SOCKET_OVERRIDE=/var/run/docker.sock
```

To see each test as it runs, add `-v`. To force a fresh run instead of a cached result, add
`-count=1`. For coverage, add `-cover`. For 10–15 seconds after a run, `docker ps` shows a
`testcontainers/ryuk` container. It removes leftover test containers, then itself.

## Project layout

```
cmd/safedb-mcp/   the server: main.go (wiring), tools.go (list_tables, describe_table), query.go, redact.go, audit.go, telemetry.go, tests
cmd/analyst/      the agent: main.go (CLI), agent.go (loop), claude.go and openai.go (providers), mcp.go (MCP client), a2a.go (A2A server), telemetry.go, tests
examples/         a2a-client: asks the analyst one question over A2A
db/initdb/        run by Postgres on first start: Pagila schema and data, readonly role (03), access policy (04)
compose.yaml      Postgres 18 with pgvector (Pagila needs it), and Jaeger under the tracing profile; bound to localhost
```

To remove it: `claude mcp remove safedb`, then `docker compose down -v`.

## License

MIT, see [LICENSE](LICENSE). The Pagila sample database ships under its own license,
see [db/PAGILA-LICENSE.txt](db/PAGILA-LICENSE.txt).

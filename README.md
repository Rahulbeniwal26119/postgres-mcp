# safedb-mcp

A read-only Postgres [MCP](https://modelcontextprotocol.io) server, written in Go.
It lets Claude Code, Codex or any MCP client explore a database and run SQL against it,
with the database itself deciding what the model may read.

> **Status:** working toward v0.1.0. The server and its guardrails are done;
> OpenTelemetry tracing and an analyst agent are next.

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
docker compose up -d --wait            # Postgres with the Pagila sample database, plus Jaeger
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
and the Jaeger UI is at <http://localhost:16686>. Both are bound to localhost only.
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

## Development

```bash
gofmt -l . && go vet ./...
DATABASE_URL=postgres://readonly:readonly@localhost:5433/pagila go test -count=1 ./...
```

The tests run against the compose database (Pagila plus the demo policy). They are skipped
when `DATABASE_URL` is not set, except the column-masking and logging tests (`TestRedactorFor`,
`TestConnectionStringNeverLogged`, `TestStartupErrorReachesAuditLog`), which need no database.
The full run takes about 6 seconds, because one test waits for the 5s statement timeout on purpose.

## Project layout

```
cmd/safedb-mcp/   the server: main.go (wiring), tools.go (list_tables, describe_table), query.go, redact.go, audit.go, tests
db/initdb/        run by Postgres on first start: Pagila schema and data, readonly role (03), access policy (04)
compose.yaml      Postgres 18 with pgvector (Pagila needs it) and Jaeger, bound to localhost
```

To remove it: `claude mcp remove safedb`, then `docker compose down -v`.

## License

MIT, see [LICENSE](LICENSE). The Pagila sample database ships under its own license,
see [db/PAGILA-LICENSE.txt](db/PAGILA-LICENSE.txt).

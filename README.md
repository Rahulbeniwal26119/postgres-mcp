# safedb-mcp

A read-only Postgres MCP server.

## Data access: allowlist and masking

safedb-mcp contains no table names and no access rules. It runs the SQL the model writes,
as the Postgres role in `DATABASE_URL`, and Postgres decides what that role can see.
The policy lives in the database, so it also protects every other client that uses the role.

### Guardrails

| Layer | Enforced by | Stops |
|---|---|---|
| Grants on the role | Postgres (`db/initdb/04-readonly-policy.sql`) | Reading any table or view that isn't explicitly granted |
| Masking views | Postgres | Raw PII in results: the role reads the view, never the base table |
| `temp_file_limit` on the role | Postgres | Queries filling the disk with temporary files |
| `READ ONLY` transaction, rolled back after every call | safedb-mcp | Writes, `SELECT ... FOR UPDATE`, and `SET`s leaking into later calls |
| 5s `statement_timeout` | safedb-mcp | Long-running queries |
| 100-row cap (`truncated: true`) | safedb-mcp | Huge results flooding the model's context |
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
  company allows that data to go to your LLM provider.
  ```bash
  claude mcp add safedb-support -e DATABASE_URL=postgres://support_ro@localhost:5433/pagila -- ./safedb-mcp
  ```
- **Engineers who need raw data** use their own credentials in `psql`, ideally against a read replica.

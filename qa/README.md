# API acceptance tests

`e2e-api.ps1` builds S2AM-GO and the local upstream fixture, starts both on
random loopback ports, and exercises the public HTTP API against a fresh
PostgreSQL database.

The script covers:

- account automation defaults (`30`/`7`/`2`, OpenAI `gpt-5.5`, and a `30`
  second rate-sync interval);
- tenant-wide balance-alert defaults, validation, webhook non-disclosure, and
  encrypted webhook storage;
- consecutive probe failures, managed pause, successful restoration, and
  manual scheduling ownership that is never restored by a successful probe;
- the recent-60-probe Uptime cap, percentage, empty state, range, and compact
  success/failure timeline;
- observed source-URL extraction and export fallback behavior, including
  preservation on an unavailable endpoint, clearing on explicit null, and API
  credential non-disclosure;
- Sub2API/NewAPI automatic source classification, operator locking across
  inventory sync, and separation of observed URLs from user-configured
  overrides;
- Sub2API and NewAPI rate synchronization and recharge-ratio conversion;
- NewAPI `data` and root-level `groups` response shapes;
- the required NewAPI source-group validation;
- NewAPI credential-state transitions from `valid` through explicit session
  expiry to `invalid` and back to `valid`, including check timestamps,
  tenant-scoped invalid counts, error clearing, and credential non-disclosure;
- global rate ordering and `gte` group-rate protection;
- user/site/account tenant isolation, including the same upstream URL for two
  owners;
- concurrent schema startup, legacy PostgreSQL audit export, daily JSONL name
  snapshots, event pagination, and file/API tenant isolation;
- login failure throttling.

Focused Go tests cover the credential classifier boundary: ordinary network
timeouts, non-authentication upstream failures, and missing groups do not mark
a NewAPI credential as `invalid`; explicit `401`/`403` or Session/Token expiry
responses do.

## Prerequisites

- Go matching `go.mod`;
- PowerShell 5.1 or newer;
- PostgreSQL client tools (`psql`);
- a PostgreSQL role that can create and drop databases.

The administrative URL must connect to the `postgres` or `template1` database.
The script refuses any other database, creates a randomly named `s2am_qa_*`
database, and drops it in `finally`, including after an assertion failure. It
does not read the project `.env` file.

## Run

The repository's WSL helper starts a local trust-authenticated PostgreSQL on
`127.0.0.1:55432`:

```powershell
wsl bash ./qa/start-postgres-wsl.sh
./qa/e2e-api.ps1
```

The default administrative URL is:

```text
postgres://s2amtest@127.0.0.1:55432/postgres?sslmode=disable
```

To use another isolated PostgreSQL instance:

```powershell
./qa/e2e-api.ps1 `
  -PostgresAdminUrl 'postgres://qa_user:qa_password@127.0.0.1:5432/postgres?sslmode=disable'
```

The same value can be supplied through `S2AM_QA_POSTGRES_ADMIN_URL`.

The script normally builds and starts `qa/fake-sub2api` on a random port. A
running loopback fixture can be reused; its state is reset before the test:

```powershell
./qa/e2e-api.ps1 -MockBaseUrl 'http://127.0.0.1:33888'
```

`MockBaseUrl` deliberately accepts only plain HTTP loopback addresses because
the fixture exposes unauthenticated test-control endpoints.

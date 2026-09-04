# S2AM-GO

<p align="center">
  <a href="./README.md">简体中文</a> · <strong>English</strong>
</p>

S2AM-GO is a lightweight account scheduling and operations console for Sub2API. The backend is written in Go, while the React/Vite frontend is embedded into the same binary. PostgreSQL is the only runtime dependency, and the default listen port is `33777`.

This edition intentionally removes the old "smart scheduling" behavior. It does not calculate or modify capacity fields such as `concurrency` or `load_factor`. It focuses on account health checks and managed pause/recovery, upstream balance display, rate synchronization, rate-based priority ordering, optional recent cache-rate weighting, group-rate protection, group-rate tracking, and independent account-level controls for those workflows.

> S2AM-GO modifies real Sub2API account fields such as `schedulable`, `rate_multiplier`, and `priority` through the Admin API. Validate the target version, permissions, and rates on test accounts before enabling automation in production.

## Screenshots

### Operations overview

![S2AM-GO operations overview](docs/screenshots/overview.png)

### Account management and balances

![S2AM-GO account management and balances](docs/screenshots/accounts.png)

## Features

### Health checks and managed recovery

- Independent per-account controls for probe interval, timeout, failure threshold, recovery-success threshold, and test model.
- Network failures, upstream failures, and timeouts count toward the configured threshold.
- S2AM-GO pauses an account only when it is still schedulable and records ownership with `managed_hold=true`.
- Managed accounts continue recovery probes and are re-enabled only after the configured number of consecutive successful probes. The default is `1`; any failed probe resets recovery progress.
- Accounts paused manually are never claimed or re-enabled automatically.
- Manual scheduling remains independent from health automation.

Probe failures are classified as `AUTH`, `BALANCE`, `RATE_LIMIT`, `UPSTREAM`, `TIMEOUT`, `CONFIGURATION`, or `UNKNOWN`. The account page displays the stable category and upstream HTTP status when available. Uptime is calculated from the latest 60 probes and includes success counts, sample counts, a compact timeline, and the observed time range.

### Upstream balances

The account page loads balances in bounded batches:

- Sub2API-managed accounts export only the upstream `base_url` and `api_key`, then query `/v1/usage` with Bearer authentication.
- NewAPI accounts use the encrypted source URL, credential, and optional `New-Api-User`, preferring subscription data and falling back to the current-user endpoint.
- Remaining, used, total, plan, unit, status, and endpoint details are normalized for the UI.
- Raw credentials never leave the backend and are never serialized into API responses.
- Balance snapshots are grouped by normalized URL plus access key. The database stores only a one-way key fingerprint, so the same URL with different keys remains isolated.
- Batch size, export concurrency, query concurrency, and timeouts are bounded.

Each user can configure one workspace-wide balance threshold, Enterprise WeChat group-robot webhook, and notification cooldown from the Alerts page. After refreshing balance snapshots, the backend compares the threshold in each upstream's original balance unit and sends one aggregated `markdown` message listing all low-balance accounts. PostgreSQL claims the cooldown window atomically, so multiple S2AM-GO instances cannot send duplicate automatic alerts for the same user during one cooldown. Webhook URLs are encrypted with AES-256-GCM and neither the URL nor robot key is echoed by the API. A separate test action verifies the saved webhook.

### Rate synchronization

Two source types are supported:

- `sub2api`: read `effective_rate_multiplier` from the managed account billing probe.
- `newapi`: read the explicitly selected NewAPI group ratio through authenticated group APIs.

The effective rate written back to Sub2API is:

```text
effective_rate = source_rate / recharge_ratio
```

NewAPI synchronization requires an explicit `source_group`. Credentials are encrypted with AES-256-GCM and are never echoed by the API. Authentication state is tracked as `unknown`, `valid`, or `invalid`, while ordinary network and upstream errors are kept separate from credential failures.

### Group-rate tracking

Each synchronized Sub2API group can bind up to 100 source accounts owned by the same user, including accounts from different managed sites. Supported modes are `first`, `average`, `min`, `max`, and a restricted `custom` expression.

Custom expressions support arithmetic, source variables such as `r0`, aggregate variables such as `avg` and `current`, and bounded functions including `min`, `max`, `sum`, `abs`, `floor`, `ceil`, `round`, and `clamp`. Expressions are parsed by the backend and are not JavaScript.

### Global rate ordering and group protection

- Rate ordering runs independently for each Sub2API site.
- Lower `rate_multiplier` values receive lower priority numbers.
- Equal rates receive equal priorities.
- Accounts without a valid rate or with ordering disabled retain their manual priority.
- Optional `gt` or `gte` group protection moves accounts to a configured guard priority when their rate reaches or exceeds a group rate.
- Sites can also enable cache-rate ordering. S2AM-GO samples Sub2API `/api/v1/admin/usage/stats` and computes `cache_rate = cache_read / (input + cache_creation + cache_read)` over a configurable window (default 1 hour). Combined score is `rate_weight * rate + cache_weight * (1 - cache_rate)`; lower scores get higher scheduling priority. With default weights of `1`, a `1.0` rate gap is comparable to a 100% cache-rate gap. Cache-rate weight `0` restores rate-only ordering.

### Multi-user isolation and file audit logs

- The first user created in an empty database is the administrator.
- Administrators manage users but do not automatically gain access to other users' sites.
- Sites, accounts, groups, probe history, and API-visible audit events are owner-scoped.
- Audit events are appended to daily UTC JSONL files under `S2AM_LOG_DIR`.
- PostgreSQL leases and `FOR UPDATE SKIP LOCKED` coordinate scheduler work across instances.

### GitHub and update notifications

The floating navigation contains a GitHub link. The account menu displays the current version, commit, and build time. The backend checks the redirect target of GitHub `releases/latest`, caches successful checks for six hours, and displays a red dot when a newer semantic version is available. GitHub failures are degraded and never block the console; development or non-semantic versions do not produce false update notices.

## Architecture

```text
Browser
  |
  | HTTP/HTTPS
  v
S2AM-GO (single binary, :33777)
  |-- embedded React + Geist UI
  |-- JSON API, sessions, and CSRF protection
  |-- scheduler and PostgreSQL leases
  |-- Sub2API and NewAPI protocol clients
  |
  +---- PostgreSQL
  +---- daily audit JSONL files
  +---- managed Sub2API Admin APIs
  +---- optional NewAPI sources
```

Main directories:

```text
cmd/s2am-go/            process entry point
internal/app/           HTTP API, scheduler, and account logic
internal/auditlog/      tenant-isolated daily JSONL audit log
internal/config/        environment configuration
internal/database/      PostgreSQL connection and migrations
internal/secret/        credential encryption
internal/upstream/      Sub2API/NewAPI adapters and SSRF controls
internal/web/           embedded frontend build output
web/                    React/Vite source
deploy/                 systemd unit example
scripts/                release build scripts
```

## Requirements

- Linux amd64 for the published binary
- PostgreSQL 14 or newer
- Network access to managed Sub2API Admin APIs and optional NewAPI sources
- Go 1.24+, Node.js 20+, and npm when building from source

Node.js is not required at runtime because the frontend is embedded into the Go binary.

## Configuration

The application does not load `.env` automatically. Export variables before direct execution or use `EnvironmentFile` with systemd.

| Variable | Required | Default | Description |
| --- | --- | --- | --- |
| `S2AM_DATABASE_URL` | Yes | none | PostgreSQL connection URL |
| `S2AM_LOG_DIR` | No | `./logs` | Daily audit JSONL directory |
| `S2AM_MASTER_KEY` | Yes | none | Exactly 32 bytes, encoded as Base64 or hex |
| `S2AM_LISTEN_ADDR` | No | `:33777` | HTTP listen address |
| `S2AM_PUBLIC_URL` | No | `http://127.0.0.1:33777` | Public origin without a trailing slash |
| `S2AM_COOKIE_SECURE` | No | `false` | Must be `true` behind production HTTPS |
| `S2AM_AUTO_MIGRATE` | No | `true` | Apply embedded migrations at startup |
| `S2AM_WORKERS` | No | `8` | Scheduler workers, from 1 to 128 |
| `S2AM_ALLOW_PRIVATE_UPSTREAMS` | No | `false` | Allow loopback and private upstream targets |

Generate a master key:

```bash
openssl rand -base64 32
```

Keep the master key unchanged and back it up with the database. Losing it makes stored Sub2API and NewAPI credentials unrecoverable.

Binary options:

| Option | Behavior |
| --- | --- |
| `--version` | Print version, commit, and build time without connecting to PostgreSQL |
| `--migrate-only` | Apply pending migrations and exit |

## Local development

1. Create a PostgreSQL database.
2. Export variables based on `.env.example`.
3. Build the frontend and run the Go service.

```bash
npm --prefix web ci
npm --prefix web run build
go test ./...
go run ./cmd/s2am-go
```

Open `http://127.0.0.1:33777`. An empty database opens the setup flow, and the first account becomes the administrator.

For frontend hot reload, run this in another terminal:

```bash
npm --prefix web run dev
```

Vite listens on `5173` and proxies `/api`, `/healthz`, and `/readyz` to `127.0.0.1:33777`.

## Build the Linux amd64 release

Bash, WSL, or Linux:

```bash
chmod +x scripts/build-release.sh
VERSION=vX.Y.Z ./scripts/build-release.sh
```

PowerShell:

```powershell
$env:VERSION = "vX.Y.Z"
./scripts/build-release.ps1
```

The scripts install locked frontend dependencies, build the embedded frontend, run `go test ./...`, and compile with `CGO_ENABLED=0 GOOS=linux GOARCH=amd64`. Only these release files are generated:

```text
dist/s2am-go-linux-amd64
dist/s2am-go-linux-amd64.sha256
```

Verify the artifact on Linux:

```bash
./dist/s2am-go-linux-amd64 --version
(cd dist && sha256sum --check s2am-go-linux-amd64.sha256)
```

## Native PostgreSQL deployment

The project does not require or ship Docker. A typical Debian/Ubuntu installation uses PostgreSQL, a dedicated system user, and the included systemd unit.

```bash
sudo apt update
sudo apt install -y postgresql postgresql-client ca-certificates openssl
sudo -u postgres createuser --pwprompt s2am
sudo -u postgres createdb --owner=s2am --encoding=UTF8 s2am

sudo useradd --system --home-dir /opt/s2am-go --shell /usr/sbin/nologin s2am
sudo install -d -o root -g root -m 0755 /opt/s2am-go
sudo install -o root -g root -m 0755 s2am-go-linux-amd64 /opt/s2am-go/s2am-go
sudo install -d -o root -g s2am -m 0750 /etc/s2am-go
sudo install -d -o s2am -g s2am -m 0700 /var/lib/s2am-go/logs
```

Example `/etc/s2am-go/s2am-go.env`:

```dotenv
S2AM_DATABASE_URL="postgres://s2am:REPLACE_WITH_URI_ENCODED_PASSWORD@127.0.0.1:5432/s2am?sslmode=disable"
S2AM_LOG_DIR="/var/lib/s2am-go/logs"
S2AM_MASTER_KEY="REPLACE_WITH_OPENSSL_OUTPUT"
S2AM_LISTEN_ADDR="127.0.0.1:33777"
S2AM_PUBLIC_URL="https://s2am.example.com"
S2AM_COOKIE_SECURE="true"
S2AM_AUTO_MIGRATE="true"
S2AM_WORKERS="8"
S2AM_ALLOW_PRIVATE_UPSTREAMS="false"
```

Install and start the service:

```bash
sudo install -o root -g root -m 0644 deploy/s2am-go.service /etc/systemd/system/s2am-go.service
sudo systemctl daemon-reload
sudo systemctl enable --now s2am-go
curl --fail http://127.0.0.1:33777/readyz
```

In production, listen on loopback and terminate HTTPS with Nginx or Caddy. Set `S2AM_PUBLIC_URL` to the HTTPS origin and enable `S2AM_COOKIE_SECURE`.

## Initial workflow

1. Open the console and create the first administrator.
2. Add the Sub2API root URL and a dedicated Admin API key.
3. Test the connection and synchronize inventory.
4. Verify accounts, groups, source types, rates, balances, and priorities.
5. Enable health, rate synchronization, ordering, or guard rules on a small test set first.
6. Validate manual actions and inspect both the activity log and the real Sub2API fields.
7. Expand automation only after the observed behavior is correct.

## HTTP API

All application API routes use the `/api/v1` prefix. Successful responses use `{"data": ...}` and errors use `{"error":{"code":"...","message":"..."}}`.

| Method | Path | Description |
| --- | --- | --- |
| `GET` | `/healthz` | Process liveness |
| `GET` | `/readyz` | PostgreSQL readiness |
| `GET/POST` | `/api/v1/setup/status`, `/api/v1/setup` | Initial setup |
| `POST` | `/api/v1/auth/login`, `/api/v1/auth/logout` | Session lifecycle |
| `GET` | `/api/v1/auth/me` | Current user |
| `GET` | `/api/v1/version` | Build version and latest GitHub Release |
| `GET` | `/api/v1/overview` | Site, account, and health summary |
| `GET/POST` | `/api/v1/sites` | List or create sites |
| `PATCH/DELETE` | `/api/v1/sites/{siteID}/` | Update or delete a site |
| `POST` | `/api/v1/sites/{siteID}/test` | Test a site connection |
| `POST` | `/api/v1/sites/{siteID}/sync` | Synchronize inventory now |
| `POST` | `/api/v1/sites/{siteID}/reconcile` | Reconcile ordering and guards now |
| `GET` | `/api/v1/accounts` | Filtered account list |
| `POST` | `/api/v1/accounts/balances` | Batch upstream balance lookup |
| `GET/PUT` | `/api/v1/settings/balance-alert` | Read or update workspace-wide balance alert settings |
| `POST` | `/api/v1/settings/balance-alert/test` | Send a test message to the saved Enterprise WeChat webhook |
| `PUT` | `/api/v1/accounts/{accountID}/settings` | Replace account automation settings |
| `PUT` | `/api/v1/accounts/{accountID}/scheduling` | Set manual scheduling state |
| `POST` | `/api/v1/accounts/{accountID}/probe` | Probe an account now |
| `POST` | `/api/v1/accounts/{accountID}/rate-sync` | Synchronize a rate now |
| `GET/POST` | `/api/v1/accounts/{accountID}/source-groups` | Read or preview NewAPI groups |
| `GET/PUT/POST` | `/api/v1/groups/...` | Group rules and manual application |
| `GET` | `/api/v1/events` | Paginated activity log |
| `GET/POST/PATCH/DELETE` | `/api/v1/users/...` | Administrator user management |

Authenticated state-changing requests must copy the `s2am_csrf` cookie into the `X-CSRF-Token` header. Sessions are valid for 30 days unless revoked.

## Security notes

- Sub2API and NewAPI credentials are encrypted with AES-256-GCM and resource-bound additional authenticated data.
- Password input is SHA-256-normalized before bcrypt cost 12 hashing, avoiding bcrypt's 72-byte input limit.
- Session tokens are stored only as SHA-256 hashes, and mutations require a separate CSRF token.
- CSP, `X-Frame-Options: DENY`, `nosniff`, referrer policy, and permissions policy are enabled.
- Upstream responses are limited to 2 MiB and redirects are not followed.
- Private, loopback, link-local, CGNAT, documentation, transition, multicast, and non-routable targets are blocked by default.
- DNS answers are fully validated and dialing is pinned to validated addresses to reduce rebinding risk.
- Production deployments must use HTTPS, Secure cookies, strict filesystem permissions, and a dedicated Sub2API Admin key.

Setting `S2AM_ALLOW_PRIVATE_UPSTREAMS=true` relaxes instance-wide SSRF protection. Use it only when all users are trusted and restrict outbound access with the host firewall.

## Backup and upgrade

Back up PostgreSQL, `S2AM_LOG_DIR`, the environment file, and the master key together. A database backup without the matching master key cannot decrypt stored credentials, and PostgreSQL alone does not contain the JSONL activity history.

Before upgrading:

1. Read the release notes and take a complete backup.
2. Verify the SHA-256 file and run the new binary with `--version`.
3. Stop the service and atomically replace the binary.
4. Apply migrations or start with `S2AM_AUTO_MIGRATE=true`.
5. Check systemd, `/readyz`, one manual probe, and one rate synchronization.

Migrations are forward migrations. If a rollback is required, restore the matching database, audit logs, environment file, master key, and previous binary together.

## Compatibility and limitations

- Managed sites must expose compatible Sub2API Admin account, group, scheduling, probe, and billing APIs.
- Different Sub2API and NewAPI forks may change envelopes or group structures; validate after upstream upgrades.
- Upstream redirects are not followed. Configure final HTTP(S) root URLs.
- Priority ordering is local to each Sub2API instance.
- S2AM-GO is not a model traffic proxy. It only calls management and probe APIs.

## References

- [langrenjh-alt/S2A-Manager](https://github.com/langrenjh-alt/S2A-Manager)
- [Wei-Shaw/sub2api](https://github.com/Wei-Shaw/sub2api)
- [QuantumNous/new-api](https://github.com/QuantumNous/new-api)
- [Vercel Geist](https://vercel.com/geist/introduction)

## License

[MIT](LICENSE)

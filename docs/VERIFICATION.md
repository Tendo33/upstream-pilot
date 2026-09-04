# Verification — 2026-09-04

## Scope

A local, runnable Sub2API upstream quality manager based on the MIT S2AM-GO import. Verification used an isolated local PostgreSQL database and loopback supplier fixtures. No production Sub2API instance was changed.

## Automated evidence

Command: `SUB2UPSTREAM_TEST_DATABASE_URL=<isolated PostgreSQL URL> go test -race -json ./...`.

Result: 152 top-level tests / 252 passing test events including subtests; zero failures and zero skipped tests. Raw local output: `output/verification/go-tests.jsonl` (ignored runtime artifact).

The quality suite proves:

- Observe mode makes no priority or scheduling writes.
- Sustained failure demotes; repeated evaluation cannot reuse one sample as multiple recovery votes.
- Consecutive fresh healthy samples restore priority in steps.
- Manual remote changes stop automated overwrite; release preserves manual changes.
- Cost collection stores distinct price history without changing remote rates.
- Interrupted priority writes reconcile from remote readback.
- Notification delivery is recorded and delivered events are not resent; changing failure counters does not notify repeatedly, while a new low-balance risk does.
- Scheduled probe collection runs independently from policy writes.
- Policy API payloads, model-based group aggregates and per-owner access checks work against PostgreSQL.
- Pausing is separately enabled, retains the last verified same-model backup and recovers only owned pauses.
- Admin-interface authentication failure is excluded from supplier-failure evidence.
- Stream probes measure first-content separately, reject errors before or after completion, reject truncation/timeout, and enforce a size bound.
- Real-traffic samples filter account, model and time; unsupported interfaces and absent latency remain unknown.

## Browser evidence

Used Playwright CLI against `127.0.0.1:33777` and the simulator at `127.0.0.1:33888`.

Verified through the actual UI: administrator creation; site creation; authentication-error display and key correction; inventory synchronization; policy configuration; enabling scheduled probes and cost collection; explicit automatic-priority mode; simulated supplier failures; a visible 20 → 40 priority write in history; manual probing of another account; notification configuration and successful test delivery; automatic queued notification showing delivered; release back to the manual baseline.

Desktop 1440×1000 and mobile 390×844 captures were inspected. A missing page container was found in the first pass and corrected. The final quality views have page gutters and no document overflow; mobile policy fields remain usable. The browser's final console check reported zero errors and zero warnings.

Local artifacts: `output/playwright/quality-desktop.png`, `quality-mobile.png`, `policy-mobile.png`.

Visual review was performed in this task, without subagents, following the user's explicit restriction. The existing neutral S2AM visual language was retained.

## Build and operations

- `npm --prefix web run build` succeeds and produces embedded assets.
- `go build -trimpath -o bin/upstream-manager ./cmd/upstream-manager` succeeds.
- `scripts/run-local.sh .local/dev.env` starts the application with its scheduler.
- `/healthz` and `/readyz` return healthy/ready; the browser exercises real authenticated application routes.
- `make release` produces a Linux amd64 executable and SHA-256 file. This is cross-build validation, not Linux production deployment acceptance.
- `bash -n` validates the local-run and release scripts; `git diff --check` is clean.

## Intentional boundary

One configured probe model per account; history retains requested and reported model. Real traffic interfaces vary by Sub2API version and may not expose TTFT, which remains unknown. Group probe aggregates are not user-request SLA, independent supplier capacity, or proof that every model is supported. Request-level retry, session affinity and failover remain Sub2API's responsibility.

Runtime databases, generated credentials, test artifacts and binaries are ignored by Git. The local preview is synthetic; production credentials, remote publication and production deployment are not part of this acceptance.

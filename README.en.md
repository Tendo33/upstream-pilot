# Upstream Pilot

A Sub2API operations console for upstream quality, procurement costs and group scheduling.

Upstream Pilot measures first-content latency, failures, balances and cost multipliers; explains risks; and applies explicitly enabled priority, scheduling and capacity controls. Observation is the default. Sub2API remains responsible for request forwarding, affinity and retries.

## Run

Requirements: Go 1.26+, Node.js 20+, PostgreSQL 14+.

```bash
git clone https://github.com/Tendo33/upstream-pilot.git
cd upstream-pilot
cp .env.example .env
openssl rand -base64 32
# Set PILOT_DATABASE_URL and PILOT_MASTER_KEY in .env.
make build
./scripts/run-local.sh .env
```

The default listener is `127.0.0.1:33777`. Create the administrator locally, connect a Sub2API admin endpoint and start in observation mode. Back up the database and encryption master key together.

```bash
make test
PILOT_TEST_DATABASE_URL='<isolated PostgreSQL URL>' make integration
RELEASE_VERSION=0.2.0-preview.1 make release
```

This is a preview, not a production SLA guarantee. Read the [repository review](docs/REVIEW.md), [operations guide](docs/OPERATIONS.md) and [deployment guide](docs/DEPLOYMENT.md) before enabling automation.

## Provenance

This is an independently maintained MIT derivative of S2AM-GO, not a clean-room rewrite. Substantial foundation code remains; the quality engine and related extensions are independently implemented. No Guardian source was imported. See [provenance](docs/PROVENANCE.md), [third-party notices](THIRD_PARTY_NOTICES.md) and [LICENSE](LICENSE).

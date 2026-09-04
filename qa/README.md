# Verification

Run `make test` after installing Go and Node.js. For real database coverage, export `SUB2UPSTREAM_TEST_DATABASE_URL` to an isolated PostgreSQL database and run `make integration`.

The integration suite in `internal/app/quality_integration_test.go` exercises observation without writes, failure demotion, gradual recovery, manual overrides, rate history, interrupted writes, notification delivery, scheduler execution, policy HTTP payloads, group/model aggregation, release and model-specific backup protection. Each test creates and removes its own randomly named schema.

`qa/fake-sub2api` is a loopback-only supplier simulator. Start it with `make demo-upstream`. Admin key: `test-admin-key`.

Example scenario controls (local fixture only):

```bash
curl -X POST http://127.0.0.1:33888/control/101 -H 'Content-Type: application/json' -d '{"probe_success":false}'
curl -X POST http://127.0.0.1:33888/control/101 -H 'Content-Type: application/json' -d '{"probe_success":true,"probe_delay_ms":1500,"billing_rate":2}'
```

The original PowerShell acceptance script is preserved in `docs/upstream/e2e-api.ps1.txt` as upstream reference. It validates the original scheduler and is not this fork's acceptance command.

Browser verification uses the real control UI with this local fixture; screenshots and transient browser files belong under ignored `output/playwright/` and `.playwright-cli/` directories.

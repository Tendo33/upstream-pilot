# Implementation and acceptance

Scope: combine the imported MIT S2AM-GO management foundation with independently implemented upstream quality control. A local, runnable application with documentation and verified behavior is the deliverable; production rollout and a GitHub remote are separate.

- [x] Import pinned S2AM-GO source with its license and provenance.
- [x] Stream probes: first-content latency, total duration, requested/actual model, explicit completion, sticky failure, bounded response size and timeout.
- [x] Observe by default; separate probe collection from all automated remote writes.
- [x] Unified priority controller: quality/cost/balance penalties, recovery hysteresis, stale-data handling, manual drift detection, reversible ownership and independent pause control.
- [x] Durable price and probe history; real-traffic sampling with honest unsupported/unknown states.
- [x] Group aggregation, usable backup visibility, model/account filters and decision explanations.
- [x] Fault/recovery/price/low-balance notifications with bounded delivery and visible failures.
- [x] Integrated responsive UI for policy configuration, observation, history, release and notifications.
- [x] Runnable development and deployment instructions, environment example and release build.
- [x] Meaningful Go tests and database/API integration tests, plus desktop/mobile browser verification.

Current imported revision: 78d4aa6. Preserve the source MIT license. Guardian code is not imported.

Acceptance evidence and the production boundary are recorded in `docs/VERIFICATION.md`.

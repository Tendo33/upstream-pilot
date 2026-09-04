# Contributing to Upstream Pilot

Keep changes focused on upstream operations. Preserve default observation, explicit actuator opt-in, manual ownership, evidence identity and freshness. Do not introduce a second automatic writer for scheduling fields.

Use Go 1.26+, Node.js 20+ and a dedicated PostgreSQL test database. Run `make test`, the uncached race/integration suite described in `qa/README.md`, and a real browser check for affected user flows. New tests should assert correct behavior, not merely reproduce a known failure.

Document behavior and limitations in the same change. Never commit credentials, runtime databases or private account exports. Preserve third-party notices when moving or modifying derived code. Subagents require an explicit user request in this workspace.

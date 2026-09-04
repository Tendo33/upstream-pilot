# Deployment

Build with `make release`. Use a dedicated PostgreSQL database and keep its backup plus the master key and audit-log directory. The application needs schema migration privileges in its own database.

The example service uses `/opt/upstream-manager/upstream-manager`, `/etc/upstream-manager.env`, and `/var/lib/upstream-manager/logs`.

On the chosen Linux host, create the service identity and directories, install the executable matching its architecture, and copy `deploy/upstream-manager.service` to the systemd unit directory. Configure the environment file from `.env.example`; restrict its permissions to the service identity. Set `S2AM_LISTEN_ADDR=127.0.0.1:33777` behind an HTTPS reverse proxy and `S2AM_COOKIE_SECURE=true`.

The application applies ordered PostgreSQL migrations on startup. `--migrate-only` applies migrations and exits; `--version` prints build identity. Start the service after the environment, database and executable are ready.

Verify `/healthz` (process), `/readyz` (database), then login and synchronize a test Sub2API site. Both endpoints being healthy do not prove upstream availability. Configure the intended account/model probe and leave policy in observation until its proposed changes match expectations.

Do not operate another automatic writer of the same account priority alongside this service. Review session affinity and retry behavior in the deployed Sub2API version before relying on priority changes for user-visible failover. Stop account takeover from the UI before retiring this service; the two release actions distinguish restoring the owned baseline from preserving a manual remote change.

For rollback, retain the prior binary and take a database backup before upgrade. Do not point an older binary at a newly migrated database without verifying schema compatibility.

This repository supplies deployable artifacts and a service template; running a production rollout is separate from local development acceptance.

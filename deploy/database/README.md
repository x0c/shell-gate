# Optional PostgreSQL

SQLite requires no database service and is the default. For shared storage, create an operator-owned PostgreSQL database and role, then set `SHELL_DATABASE_URL`. Use [init-shell-gate-db.sql](init-shell-gate-db.sql) with psql; replace the role names if needed and supply the password through psql's private interactive prompt. No password is committed.

Use verified TLS outside a private service network. Back up before changing providers. Existing SQLite imports require explicit `SHELL_LEGACY_SQLITE_PATH` and an empty destination; import covers workspace presets and tabs, not passkeys or session keys. See [deployment](../../docs/DEPLOYMENT.md).

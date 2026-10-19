# MySQL Session Fork Deployment Prerequisite

Session forks use database triggers to serialize message changes with topic
revision checks and reject writes to unfinished copies. Do not remove or skip
these triggers to bypass a startup error. PostgreSQL is unaffected by the MySQL
setting described here.

## Administrator Decision

With MySQL binary logging enabled, ordinary schema accounts need the server
setting `log_bin_trust_function_creators=1` to create these triggers without
`SUPER`. This is a server-wide relaxation for accounts already allowed to create
stored functions/triggers, not a per-feature permission. It does not grant those
permissions to chat users, Bot API keys, or local XiaoBa installations.

Use this option only on an instance whose function/trigger creators are trusted.
Before enabling it, the DBA must inspect grants and inherited roles for all such
accounts, not just the CatsCompany login. Do not enable it blindly on a shared
instance with untrusted schema owners; use a dedicated instance or review an
alternative deployment with the DBA instead.

Keep the application account scoped to its own application database, without
global `SUPER`, `SYSTEM_VARIABLES_ADMIN`, `SET_USER_ID`, `FILE`, or `GRANT OPTION`
privileges and without privileged inherited roles. Schema initialization already
requires database DDL and `TRIGGER`; never switch the application DSN to root to
work around error 1419. Protect the DSN as a server-only secret. Users and bots
must not receive it or an arbitrary SQL execution interface.

The application does not change global MySQL settings, grant privileges, or
silently downgrade the fork guarantees. Its trigger definitions have no explicit
privileged `DEFINER` and operate only on the current application database.
An omitted `DEFINER` still uses the creator's account: inspect existing objects,
especially those installed manually as root. A username-only deployment guard
does not prove the effective grants or inherited roles are limited.

```sql
SELECT TRIGGER_NAME, DEFINER, ACTION_STATEMENT
FROM information_schema.TRIGGERS
WHERE TRIGGER_SCHEMA = DATABASE()
  AND TRIGGER_NAME IN ('session_fork_message_insert',
                       'session_fork_message_update',
                       'session_fork_message_delete');
```

## Persistent Configuration

After the administrator has accepted the instance-wide policy, configure the
MySQL option file (or the managed database parameter group):

```ini
[mysqld]
log_bin_trust_function_creators=1
```

Keep binary logging enabled. A temporary `SET GLOBAL` alone is not a durable
deployment configuration. Check the effective values after applying the database
configuration and after any database restart/replacement:

```sql
SELECT @@GLOBAL.log_bin, @@GLOBAL.log_bin_trust_function_creators;
SHOW GRANTS FOR CURRENT_USER;
```

The application still attempts schema initialization on startup. Retain the
approved configuration across ordinary application CD releases; do not assume it
can be reset immediately after first installation. Database replacement requires
reapplying/checking the policy. Application-only deployments do not need to
repeat a manual database change.

`deploy/test/docker-compose.yml` supplies the option for its dedicated MySQL
service. Applying that Compose change to an existing test stack may recreate and
briefly restart the MySQL container, retaining its data volume. The production
Compose uses an external database and deliberately does not modify its settings.
Production documentation currently selects PostgreSQL; actual runtime grants and
configuration must be verified by its operator, not inferred from templates.

## CI Evidence

CI first checks that binary logging is on and trust is off, then proves that an
ordinary database-scoped account receives an explicit schema permission failure.
Only the disposable service's administrator enables trust. The same ordinary
account then runs schema recovery/idempotency and real fork transaction tests.
Tests must not be replaced by root execution, disabled binary logging, swallowed
DDL errors, or a test selection that silently skips the fork integration cases.

The companion PostgreSQL fork integration suite is also explicitly selected with
its dedicated DSN. Mock tests alone do not certify either engine's concurrency.

References: [MySQL stored program binary logging](https://dev.mysql.com/doc/refman/8.0/en/stored-programs-logging.html)
and [trigger privileges](https://dev.mysql.com/doc/refman/8.0/en/create-trigger.html).

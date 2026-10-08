# Change an administrator password from the command line

Use the matching `admin-api` binary on a trusted administration host with access
to the management database. This is a privileged local recovery command: it does
not require the old password, a logged-in session, email confirmation or a second
account. Database credentials provide the authority; it is not an HTTP endpoint.

```sh
export ADMIN_MYSQL_DSN_FILE=/etc/skygo-admin/private/mysql-dsn
admin-api -change-password admin
```

The terminal prompts for the new password twice with echo disabled. Passwords
must contain at least 12 Unicode characters and at most 1024 bytes. Spaces are
preserved; line breaks and NUL are not accepted. There is no plaintext password
argument or password environment variable.

With an existing Compose installation, run from its deployment directory:

```sh
docker compose exec admin-api admin-api -change-password admin
```

Retain the deployment's Compose file, environment and profile options. For the
repository's local example, use `docker compose -f deploy/compose.yaml`; for a
custom file, use its actual `-f` path. Keep an interactive terminal allocated for
the prompts (do not use `exec -T` without a password file).

If the API container is stopped, use a one-off container with the same private
DSN mount; the management database must be reachable. This does not start other
services:

```sh
docker compose run --rm --no-deps admin-api -change-password admin
```

For non-interactive execution, mount a private regular file readable by the API
container user, then pass `-password-file /run/private/new-admin-password`.
The file must have permissions `0600` or stricter; symlinks and group/world access
are rejected. One trailing LF or CRLF is removed. Remove the temporary password
file after successful use. Never put its contents in shell arguments or logs.

With the file already mounted at the following container path:

```sh
docker compose exec -T admin-api admin-api -change-password admin \
  -password-file /run/private/new-admin-password
```

For a stopped API, replace `exec -T admin-api admin-api` with
`run --rm --no-deps -T admin-api`. For a local binary, append the same
`-password-file` option using a host path. The option reads an existing file; it
does not create a file or mount one into a running container.

Only `ADMIN_MYSQL_DSN_FILE` is needed: SMTP, signing keys, bootstrap configuration
and application startup are not required. The command exits after completion and
cannot be combined with `-migrate`. It never initializes or migrates a database.
The current binary requires schema v6; this feature adds no schema changes.

A successful change atomically writes the Argon2id hash, revokes all sessions of
that account, and appends an `admin.password.change.cli` audit entry with system
operator ID 0 and the target account ID. The audit contains no password or hash.
Audit failure rolls back both the password change and session revocation. Logins
that verified the old password before the change cannot create a new session
after it commits. The administrator must sign in again with the new password.

Do not rerun bootstrap or change the bootstrap token to reset an existing
administrator. The command changes existing accounts only. It preserves role, enabled/disabled
state, TOTP secrets, recovery codes and login protections. It does not reset an
IP lock or bypass MFA; those remain relevant on the next login.

# Verification

Use the commands below against a new checkout with `GOWORK=off`. Tests use synthetic
identities, generated keys and disposable data; no external application is needed.

| Check | Command |
|---|---|
| Go unit tests | `go test ./...` |
| Race detection | `go test -race ./internal/...` |
| MySQL workflows and SMTP confirmation | `python3 scripts/integration.py` |
| Frontend helpers | `node --test admin-web/*.test.cjs` |
| Static analysis | `go vet ./...` |
| Business-content and secret-pattern policy | `python3 scripts/scan.py` |
| Secret scanner | `gitleaks dir . --redact` |
| Source archive and Linux binaries | `python3 scripts/release.py` |
| Container and Skygo example smoke test | `python3 scripts/docker-smoke.py` |

The MySQL suite checks bootstrap exclusion, required captcha, login, role denial,
CSRF, independent approval, signed task delivery, controller restart, duplicate
result handling, configuration activation and audit chain integrity. Agent tests
check duplicate execution, persisted receipts, expiry, tampering, restart and
uncertain-outcome reconciliation. Confirmation tests cover throttling, binding,
replay and SMTP transport requirements. GitHub tests use a synthetic provider.

Live GitHub dispatch and production SMTP are not exercised without deployer-owned
integration credentials. Local tests cannot establish production readiness or
prove that a specific installation's runtime logs/configuration contain no secrets.

## Initial release verification record — 2026-09-29

Passed locally:

- Full Go tests and `go vet`; race-enabled isolated MySQL workflow and confirmation tests.
- Frontend helper tests and successful HTTP serving of the actual static page.
- Standard Docker builds of API, Web and Agent from this project and public dependencies.
- Disposable container exercise: standalone API/MySQL; two Skygo nodes resolving
  each other; Agent stop/start/restart/health, configuration replacement, immutable
  image deployment to two revisions, rollback and default log-access denial.
- Linux amd64/arm64 API and Agent builds; allowlisted source archive and checksums.
- Custom source/archive policy scan and redacted Gitleaks source scan. Generated
  checksum-manifest findings were verified against archive hashes and the manifest
  format changed so hashes are not misidentified as API credentials.

Not verified: interactive browser rendering (no browser connection available),
live production SMTP, live GitHub dispatch, production deployment, or Linux amd64
container execution. The local container run used Linux arm64. These results are
local evidence, not a claim that CI or a production deployment has passed.

The initial Docker base-image fetch timed out through the daemon. Public base
images were fetched anonymously and loaded locally; the standard Dockerfiles then
built successfully. No private base image or application checkout was used.

## Release workflow synchronization — schema v2

Local verification for the release-management update includes persistent
per-attempt registration, immutable duplicate/conflict handling, single success
audit under concurrent requests, unknown state on read failure, missing/mixed
architecture pairs, ambiguous host identity, role denial, partial preparation
retry, stale/previous-boot receipts and explicit v1-to-v2 schema migration.

The Docker fixture additionally prepares both image revisions twice and verifies
that container IDs and configuration bytes remain unchanged, then exercises
cached deployment and rollback. Artifact tests verify provenance and ZIP digest
binding, reject unapproved download origins, and ensure provider authorization
is not forwarded to signed storage URLs. Frontend tests cover permission-aware
registration, persistent bookmarks and host selection without silent retargeting.

The Browser connection was unavailable, so interactive browser verification is
not claimed for this update. Live GitHub publication/registration and production
Registry credentials were not used; provider transport tests and the disposable
registry provide local evidence only. See [migration and release workflow](releases.md).

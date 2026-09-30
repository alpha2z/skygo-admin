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

## Selective publications — schema v3, 2026-09-30

Passed locally:

- `make test`: full Go suite, internal race suite and nine frontend tests.
- `go vet ./...` and `git diff --check`.
- Disposable MySQL race suite: physical v2 table/column migration preserving a
  legacy receipt; concurrent request identity; independent approval; expired
  preparation; configuration drift at approval and first dispatch; rejected and
  expired publications; strict client payload; controller restart; duplicate
  results; one execution task; locks retained for uncertain execution and released
  only after both member proofs. Automatic registration tests cover default-off,
  three-artifact budget, bounded exponential retry, system audit and unknown reads.
- Real Docker, Linux arm64: cold-cache preparation leaves containers/configuration
  unchanged; API-only replacement restarts the unchanged-image companion; pair
  replacement; HTTP health failure restores both original images; durable startup
  recovery with an unreachable controller; terminal replay does not restart again.
  Recovery-unit services are synthetic nodes, with no production data or topology.
- Standard API/Web/Agent image builds using the independent Docker context and
  pinned public module. The final API/Web images served every HTML JS/CSS reference;
  absent JS/CSS returned 404. The fixture changes only the API process UID to read
  its generated private files; it does not mount source assets into final images.

Browser discovery returned no connected browser. Desktop/mobile rendering, live
browser disconnect/re-login interactions and runtime module-failure presentation
remain **unverified**. Frontend helper and static-asset tests do not replace that
visual acceptance. Live GitHub signing/registration, production SMTP and Registry
credentials, actual production self-upgrade, and amd64 container execution were
not exercised. Linux amd64 binaries are cross-built, not runtime-tested here.

The local release outputs are an allowlisted source archive, per-file source
manifest, four Linux service binaries, three arm64 image archives and checksums.
See `dist/source-manifest.json` for the exact source list and
`dist/image-manifest.json` for exported image identities. Run `make release` before
`python3 scripts/export-images.py v3-review` to regenerate the complete set.

Source/history policy scans and redacted Gitleaks checks complement review of the
new API contract, local-only configuration and synthetic test fixtures. No private
source history, deployment credential, business protocol or gameplay data was
copied. Scans report locations/categories and never matching secret values;
passing scans is not a mathematical guarantee that all sensitive content is absent.

## Image distribution synchronization — schema v4, 2026-09-30

Local checks for this update include:

- Physical v3-to-v4 MySQL migration with a retained legacy task receipt; optional
  central preparation; task-scoped archive authorization and HTTP Range; separate
  controller/Agent attempts; stale progress rejection; independent cleanup
  approval; reference changes blocking dispatch; successful central cleanup.
- Registry transport tests for platform/config/layer identity, corrupt layers,
  immutable caching, bounded capacity, resumption and shared/unknown blob indexes.
  Agent archive tests cover Range resume/fallback, full archive hashes, structural
  validation and protected stopped-container references.
- Real Agent archive preparation in an isolated Linux container using a synthetic
  authenticated HTTP server: partial-file resume, verified Docker import, receipt
  replay without another transfer, deployment identity lookup and managed cleanup.
  Docker storage is mounted read-only for capacity checks; no production config is
  used. Run this test with `python3 scripts/archive-smoke.py`.
- Real Docker service fixtures: direct cache preparation changes neither containers
  nor configuration; API-only and paired recovery-unit publication; selected image
  variable persistence; health-failure and interrupted-update restoration of
  original images/environment; ordinary-service environment persistence, replay
  and rollback following a simulated lost verification result.
- Full Go suite, internal race suite, 12 frontend tests, static analysis and release
  builds. UI tests cover explicit refresh, preserved bookmarks, unknown counts,
  JST rendering, module availability and all entrypoint asset references.

The browser runtime reported no connected browser. Desktop/mobile rendering and
interactive disconnect/re-login flows remain unverified. Live GHCR/GitHub/SMTP
credentials were not used; Registry HTTP fixtures and local Docker establish local
transport/runtime evidence only. CI execution and production deployment are not
claimed. Local container execution is arm64; amd64 service binaries are cross-built.

The new transport helpers were extracted by a reviewed file allowlist and adapted
to public image contracts. Tests use synthetic components and credentials generated
at runtime. Source history and private deployment files were not copied. Custom
policy scans, redacted secret scans and owned-image-file inspection accompany the
source archive, four Linux binaries, three local review images and their manifests.

## 2026-09-30 management self-update entry

Passed in this implementation:

- `make test`: all Go packages, internal race suite, and frontend tests; the initial
  sandboxed race run could not read a Go build-cache file and was rerun successfully
  with normal cache access. The final frontend suite has 17 passing tests.
- `python3 scripts/integration.py`: isolated MySQL with race detection, including
  inventory without a selected release, unauthenticated rejection, invalid release
  rejection, existing migration, scope/receipt checks and independent approval.
- `go vet ./...` with `GOWORK=off`; frontend syntax checks; `git diff --check`.
- `python3 scripts/docker-smoke.py`: disposable database/API and synthetic nodes,
  direct preparation, selected/paired publication, environment persistence,
  health-failure rollback and interrupted Agent recovery without duplicate execution.
  Also run using the locally built API/Web images to check packaged assets and 404s.
- `python3 scripts/archive-smoke.py`: real Docker archive preparation and replay.
- Three local `self-update-review` images built using the repository Dockerfiles;
  `scripts/inspect-images.py` checked metadata and owned runtime files. No image
  was pushed. Go module resolution used the pinned public dependency.
- `make scan` and `make release`: policy scan, source archive, four Linux binaries
  (API/Agent, amd64/arm64), manifest and checksums. No matching private markers or
  credential patterns were reported. This is not proof that every possible secret
  format can be detected.
- The control Compose template parsed with synthetic settings. This is a template
  check, not deployment validation of a user's external database or SMTP service.

Not verified: desktop/mobile browser appearance or interactive re-login. The
browser tool could not connect because its existing browser profile was already
in use; no unrelated browser was stopped. DOM-based tests cover inventory rendering,
read-only update checks, request bookmarks, bounded polling, disconnection and
session-expiry behavior. Real registry/SMTP credentials and production deployment
were not exercised. The Linux systemd example was not started on this macOS host.
Signed release manifests do not declare schema compatibility; operator review of
migration notes is still required, and API startup continues to reject an
incompatible database. Existing rollback tests do not imply database rollback.

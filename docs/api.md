# API v1

Single confirmation is the default. Separate approval below applies when
`ADMIN_INDEPENDENT_APPROVAL_ENABLED=true`; existing requests retain their saved
policy. See [approval modes and schema v6](approval.md).

JSON management routes are under `/api/v1`. Browser authentication uses
`admin_session` (HttpOnly, SameSite=Strict) and `admin_csrf`. Mutations require
`X-CSRF-Token` equal to both the CSRF cookie and the server-side session value.
Logout deletes the session. Disabled users and expired sessions cannot access APIs.

| Route | Purpose / permission |
|---|---|
| GET `/bootstrap/status`; POST `/bootstrap` | First administrator; `X-Bootstrap-Token` required for POST |
| GET `/auth/settings`; GET `/auth/captcha`; POST `/login` | Login configuration, PNG challenge and session |
| GET `/session`; POST `/logout` | Current identity and session revocation |
| GET/POST `/hosts`; POST `/hosts/:id/revoke` | Host read / `host.manage` |
| GET/POST `/services` | Inventory read / `ops.write` |
| GET/POST `/tasks` | Task read / `ops.write` |
| POST `/tasks/:id/approve`; POST `/tasks/:id/reject` | `ops.approve`; approval must be by another administrator |
| GET/POST `/configs` | `config.read` / `config.write` |
| GET `/audit` | `audit.read` |
| GET/POST `/admins`; POST `/admins/:id/disable` | `admin.manage` |
| GET/POST `/builds` | `build.read` / `build.write`; optional integration |

Lists return the latest 200 entries (GitHub lists 30). This first release uses
bounded lists rather than paginated history export. Payload limit: 512 KiB;
configuration content: 256 KiB. No credentials appear in read APIs.

Service definition:

```json
{"id":"node-a","host_id":"host-a","image":"example/node@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa","depends_on":[],"control_plane":false}
```

The digest above is a placeholder, not a downloadable image. Use an actual digest.

Task request fields: `service`, `action`, optional `image` for deploy/rollback,
and `config_version` for configure. Configuration request fields: `service`,
`kind` (`json` or `skygo`) and JSON `content`. Host creation accepts `id` and
returns `token` once plus `signing_public_key`. Store them out of the repository.

When email confirmation is enabled, a mutation first returns HTTP 428 with
`confirmation_id`. Resubmit the **identical body/path/method** with
`X-Confirmation-ID` and `X-Confirmation-Code`. Challenges are administrator-bound,
expire after ten minutes, throttle issuance and limit code attempts. Completed
responses are encrypted at rest for safe replay. Executing/uncertain challenges
never blindly repeat a mutation. Email confirmation is independent of task approval.

Agent routes under `/agent/v1` require `X-Host-ID` and `Authorization: Bearer …`:

- POST `/heartbeat`: version, boot_id and service observations.
- GET `/commands`: signed envelopes assigned to this host.
- POST `/results`: task ID, status, bounded error code and verified state.

Unknown host identities and revoked tokens return 401. No agent may obtain another
host's tasks. Signature, expiry, service inventory and local image allowlists are
checked by the agent. Results are idempotent; conflicting terminal results are rejected.

The signed release and image-preparation endpoints are documented in
[publication workflow and schema v3](publications.md). A prepared image is not an approved
service upgrade.

See [schema v4 distribution](distribution.md) for delivery attempts, cleanup and optional Agent capabilities.

For the System update view, GET `/publication-candidates` without `release_id`
returns current control-unit inventory and provenance without preparing images.
`available` is false until a trusted version is selected. The usual `ops.read`
and `build.read` permissions still apply. Invalid explicit release IDs are rejected.
Matched provenance includes nullable `build_started_at`; clients must display an
unknown build time when absent. Publication writes and signed commands are unchanged.

### Recoverable task requests

`POST /api/v1/tasks` accepts an optional `request_id` for ordinary operations as
well as the required identity for extension operations. Generate it once per user
intent and retain it before submitting. An identical request by the same operator
returns its original task, including terminal results. Reusing the identity with
a different service, action, image, configuration version or extension payload is
rejected. Configured approval policy and execution-time checks remain mandatory.

`GET /api/v1/auth/settings` reports `task_request_id_supported: true` on supporting
versions. Clients requiring durable retry semantics must disable their new task
submission flow when this capability is absent; do not fall back to new identities
after a lost response. Existing clients omitting `request_id` remain compatible.

Approved extension workflows and schema v5 migration: [workflow contract](workflows.md).

GET `/tasks/:id` reads a durable task by exact ID, including records outside the latest-200 list. It requires `ops.read`, returns 404 for a missing ID, and omits result logs without `ops.logs`; structured receipts remain available. It does not retry or execute a task.

Task lists and exact task reads include `extension` for extension tasks. `extension_payload` is included only when the current role holds that registered action’s execution or approval permission, enabling a complete review of pending intent. The raw signed envelope and configuration payload remain excluded.

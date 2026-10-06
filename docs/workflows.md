# Approved application workflows (schema v5)

Current approval policy: single confirmation by default; independent approval
requires `ADMIN_INDEPENDENT_APPROVAL_ENABLED=true`. Existing records keep their
original policy. See [schema v6 migration and API behavior](approval.md). References
to separate approval below describe the enabled mode.

Applications may register `admin.WorkflowProvider` values in `Config.Workflows`.
A provider resolves a small business intent into a bounded `WorkflowPlan`; the
shared SDK owns request identity, independent approval, resource locks, signed
commands, receipts and restart recovery. Official builds remain independent and
have no application providers by default.

## Explicit upgrade

Stop the API before running the matching `admin-api -migrate`. Schema v5 adds
`workflows`, `workflow_locks` and `tasks.workflow_id`; existing accounts, tasks,
publications, releases and receipts remain. Start the matching API/Web and Agent.
Ordinary image publications do not run this migration. A v4 API refuses a v5
management database; restore a verified pre-migration backup when reverting the
schema. Do not reissue previously signed tasks from a restored database.

The Agent advertises `workflow.scope.v1` only when its locally managed immutable
image and configuration can be determined unambiguously. The stable scope is a
keyed hash of local inventory, Compose and configuration, normalizing only image
variables explicitly managed by that Agent. Their current values and container
image identities are checked separately. Private configuration never leaves the
Agent. An old Agent cannot participate in these workflows.

## Contract

- `POST /api/v1/workflows/preview`: `{request_id, provider, intent}` returns a
  resolved plan and `preview_hash`. This does not create a task or run commands.
- `POST /api/v1/workflows`: the same body plus `preview_hash`; the server resolves
  again and refuses changed scope. Operation confirmation applies as configured.
- `GET /api/v1/workflows`, `GET /api/v1/workflows/:id`: durable records and child
  task receipts. Clients resume these identities after reconnecting.
- `POST /api/v1/workflows/:id/approve` or `/reject`: independent approval and
  provider permissions, plus ordinary operations permissions and confirmation.

The same request identity and intent return the original record; changed intent
or another requester cannot reuse it. Plans freeze every target, original image,
local configuration fingerprint and explicitly approved recovery steps. Group
and service locks prevent overlapping operations and inventory edits. Scope,
identity, permissions and provider policy are checked again before signing.
Each child task has a deterministic ID, uses the common task transport, and is
journaled locally by the Agent. Initial execution also checks the signed local
scope, closing the gap between heartbeat and execution. Reconciliation uses the
existing durable receipt, never a fresh mutation.

A `CacheOnly` plan may contain only `prepare-image` commands. It requires the
provider's write permission but does not approve a later publication. Its image
identity, platform and digest receipt must match the reviewed plan. Recovery
plans cannot add targets or authorize arbitrary commands. Pending and unresolved
workflow image references are protected from cleanup.

Uncertain results, expired execution authority and unverified recovery retain
locks. Inspect actual containers and Agent journals; there is intentionally no
"force success" or unsafe unlock endpoint. A successful publication and a
successful application-level reopening are different operations.

## UI composition

An extension page may replace selected generic navigation entries using
`Page.Replaces` (services, tasks, builds, publications, system-update,
distribution). This changes navigation only, never authorization or server routes.
The browser context exposes shared publication and distribution renderers as
`components`, so an application can embed the existing management update flow
without copying its approval or execution implementation. Publication selections
can be saved in the application's own bookmark through `publicationState` and
`savePublicationState`.

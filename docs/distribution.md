# Image distribution and schema v4

Single confirmation is the default. Separate approval below applies when
`ADMIN_INDEPENDENT_APPROVAL_ENABLED=true`; existing requests retain their saved
policy. See [approval modes and schema v6](approval.md).

Schema v4 adds optional central GHCR distribution, persistent attempt history,
reviewed cleanup and local image-variable persistence. Ordinary startup never
migrates. Image preparation never restarts a container or writes its environment.

## Upgrade and compatibility

Back up the management database and retain Agent state directories and private
configuration. Finish or reconcile existing active tasks before upgrading. Install
the new Agent first; leave new options disabled until the API is upgraded. Stop
API/Web, run `admin-api -migrate`, then start the matching API/Web. Schema v4 keeps
accounts, sessions, signed releases, publications, tasks and receipts; older rows
have unknown attempt/build-time data rather than fabricated history.

New CI builds use signed manifest v2 with `build.started_at`. API v4 verifies both
historical v1 and v2 without rewriting signatures. Upgrade the API before enabling
the new build workflow; old APIs cannot register v2 manifests. All three component
images in a workflow use one UTC build-start instant and its JST display tag
`YYYYMMDD-HHMMSS-<architecture>`. Labels use `io.skygo-admin.build.*`; SHA tags and
immutable digests remain authoritative. A display tag never establishes trust.

The Agent wire protocol remains v1 with explicit optional capabilities:
`image.archive.v1`, `image.cleanup.v1`, and `image.env.v1`. Unsupported commands
are not sent to an old Agent. Default installations continue direct Registry
preparation. Central mode requires the new archive capability and does not silently
fall back to direct Registry access. API/Web recovery remains independently
approved; automatic build registration still cannot initiate deployment.

## Optional central GHCR distribution

Set `ADMIN_DISTRIBUTION_CONFIG` to an operator-owned JSON file outside the checkout.
See [synthetic configuration](../examples/distribution.json). `enabled` defaults
false. When enabled, configure an absolute cache directory, private Registry token
file, username and exact GHCR package allowlist. `max_bytes` defaults to 64 GiB
and permits 8–64 GiB. A single generated archive is limited to 8 GiB. Downloads
reserve capacity conservatively; insufficient space stops the task, never triggers
automatic deletion. The API requires write access to its cache, **not a Docker
Socket**. The optional Compose override is `deploy/compose.distribution.yaml`.

The API verifies Registry metadata, selected platform, configuration digest,
compressed-layer hashes and expanded diff IDs before creating an import archive.
Only a task assigned to an authenticated host can download that archive, and only
while its grant remains active. Archive descriptors are signed with the existing
command key; no request accepts an arbitrary remote URL or destination path.
Registry credentials stay on the API host and are stripped on storage redirects.

The Agent uses its existing HTTPS controller origin and host identity. Partial
files survive interruption, but are imported only after exact length, SHA-256,
archive structure and image identity checks. Range failures are explicitly
recorded; a full response to a Range request restarts that transfer. A new attempt
preserves older attempts and cannot accept stale sequence numbers or results from
another task. Graceful API shutdown leaves its unfinished download resumable.

For central import, the Agent must be able to check the Docker daemon's local
storage filesystem. A containerized Agent on the same Linux host needs the daemon
root (normally `/var/lib/docker`) mounted read-only at the same path, in addition
to its Docker Socket and private state directory. Remote-daemon configurations
without a visible storage filesystem fail closed at the capacity check. Direct
preparation is unchanged. Do not substitute an unrelated path as disk evidence.

The UI uses explicit refresh. Attempt records show phase, start/end time,
processed payload bytes, reused bytes, downloaded payload bytes and safe error or
resume codes. Unknown totals remain unknown; metadata requests are not counted as
image payload bytes. Direct Docker pulls report phase/result, not invented byte
progress. Refresh/re-login preserves delivery and cleanup intent IDs in the URL.

## Cleanup

Cleanup inventory is based on managed history, not a global Docker prune. Preview
one exact central-cache, Agent-archive or local-image resource, then obtain approval
from a different administrator. `ops.write` creates previews; `ops.approve` approves
or rejects. Session, CSRF, enabled email confirmation and audit remain required.

The fixed default protects the newest three component/platform versions, seven
days of recent use, configured/current images, rollback sets, unresolved tasks and
shared references. Offline hosts and unreadable references are protected. Approval
and first dispatch recheck protection; changed scope needs a fresh preview. The
Agent also checks all running **and stopped** containers and its unfinished unit
journals. Deletion never uses `--force` or global prune. Cache cleanup preserves
shared blobs and treats unreadable indexes as unknown. Exact physical reclaimed
space is not inferred from logical image size. Historical releases/audits remain.

## Local image environment persistence

Set `sync_image_env: true` on an individual Agent service to opt in. The existing
`env_file` and `image_variable` must identify a local, regular environment file and
a dedicated variable used by that service's Compose `image` expression. File
owner/mode are retained. The Agent verifies that substituting the variable changes
only the selected service image; ambiguous variables or unrelated template changes
are rejected. Both the file and its parent directory must be writable for atomic
replacement; mount the private directory, not a read-only individual file.

Before service mutation the Agent journals original image assignments and a keyed
configuration fingerprint in its 0700 state directory. Journal files are 0600;
other variable values are never uploaded or printed. Preparation makes no change.
Publication writes only selected image variables. Success survives later Compose
starts; rollback restores original assignments and original running image IDs.
An unselected recovery companion keeps its variable value and still restarts.

Recovery recognizes only its own authorized environment edits. An unrelated file
or Compose change leaves execution uncertain with service locks retained. Restore
and inspect the local configuration before resuming; do not delete the journal or
force-clear a recovery-unit result. Cross-schema rollback remains a separate
backup/restore operation, never an image-task migration.

## Added endpoints

All browser routes use `/api/v1`:

| Route | Purpose |
| --- | --- |
| GET `/image-deliveries`, `/:id`, `/:id/attempts` | Delivery and append-only attempt identity/history; `ops.read` |
| POST `/image-deliveries/:id/retry` | New execution task for the same delivery; `ops.write` + `build.read` |
| GET `/image-cleanup/resources`, `/image-cleanup` | Protected inventory and review records; `ops.read` |
| POST `/image-cleanup` | `{request_id, resource_id}` preview; `ops.write` |
| POST `/image-cleanup/:id/approve`, `/:id/reject` | Independent approval/rejection; `ops.approve` |

Authenticated Agent routes under `/agent/v1/image-deliveries/:id` provide
`GET /archive`, `POST /attempts` (`task_id`) and `POST /progress` (task, attempt and
monotonic sequence). Existing preparation/publication routes select the configured
transport; their client selection and approval contracts remain intact.

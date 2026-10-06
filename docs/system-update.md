# Management system updates

Single confirmation is the default. Separate approval below applies when
`ADMIN_INDEPENDENT_APPROVAL_ENABLED=true`; existing requests retain their saved
policy. See [approval modes and schema v6](approval.md).

System update uses the existing publication API and an independently running host
Agent. It replaces Docker images, not executable files in container writable
layers. API/Web form one recovery unit; the Agent itself is maintained separately.

## Prepare the installation

1. Install `deploy/compose.control.yaml` as `/etc/skygo-admin/compose.yaml`.
   Create `/etc/skygo-admin/private/compose.env` with the two approved immutable
   `ADMIN_API_IMAGE` and `ADMIN_WEB_IMAGE` references. Supply the SMTP and trusted
   proxy settings required by Compose. Keep the private directory restricted and
   files at 0600; set `LOCAL_UID`/`LOCAL_GID` to their owner. Provision the API secret
   files described in [deployment](deployment.md), including GitHub configuration
   and SMTP credentials. Terminate HTTPS in front of the loopback Web listener.
   This template does not provision or reset a database.
2. Perform the explicit schema migration for the installed release, then start:
   `docker compose -p admin --env-file /etc/skygo-admin/private/compose.env -f /etc/skygo-admin/compose.yaml up -d`.
3. Enroll `control-host` and save its one-time token privately. Install
   `examples/control-unit.json` outside the repository. Replace synthetic repository
   names and API URL with your installation's values. Run `ops-agent -config` with
   that absolute inventory path under a host service supervisor, outside the
   API/Web Compose unit. A Linux systemd example is provided in
   `deploy/skygo-admin-agent.service`; install it only after reviewing the private
   inventory. It runs with host Docker administration privileges. Keep
   `/var/lib/skygo-admin/agent` across upgrades. The Agent
   needs Docker/Compose access and permission to atomically replace files in the
   private environment directory. Never put its journal inside an ephemeral container.
4. Add service IDs `api` and `web` for this host in Services, with
   `control_plane=true`, roles `admin-api`/`admin-web`, actual architecture,
   immutable image references, and Web depending on API. Local Compose service
   names are `admin-api`/`admin-web`; do not confuse them with inventory IDs.
5. Verify heartbeat, both immutable image IDs, and `control.unit.v1`. Configure
   independent build trust keys and register a signed release as documented in
   [releases](releases.md). Create a separate administrator with approval permission.

The example enables `sync_image_env` for both services. Preparation never writes
these variables. Publication changes only selected image variables, preserving
other configuration; failure restores their old values. Do not bind-mount a single
environment file into a container: atomic replacement requires directory access.

## Update from the management page

Open **System update**. **Check versions and status** reads registered trusted
versions and current inventory; it does not dispatch builds or publish anything.
Select the host explicitly. It must be the installation you intend to update:
there is no reliable inference from the browser URL to a host identity.

Select a trusted version; changed components with matching architecture images
are selected by default. Missing images remain unavailable. Unknown current
provenance is displayed as unknown/unmatched, never inferred from tags. The
current source time is the build time when recorded, not the registration time.

Click **Sync selected images**, refresh for preparation receipts, then **Submit
for independent approval**. A different administrator approves the same durable
record. Both services participate in ordered restart, health checks and rollback,
even when only one image changes. Preparation alone cannot restart services.

After approval the page checks only that publication every five seconds, for at
most 60 checks. It updates the status card without replacing selections or forms.
Temporary API outages retain the request identity. Session expiry stops polling;
sign in again using the same bookmark. Refresh queries the original record and
restarts bounded observation. No reconnect path automatically submits an update.

An uncertain result retains locks. Inspect the Agent journal and actual containers;
do not delete state or force a second upgrade. A failed health check restores the
original images and selected environment variables. Verify both the reported
outcome and current inventory before another update.

## Compatibility and rollback

Only use ordinary system updates for releases compatible with the current database
schema. Signed manifests currently do not declare schema compatibility; the page
cannot infer it. Consult release migration notes before submission. API startup
rejects an incompatible schema; the Agent can restore the previous containers,
but this is not a database rollback mechanism. Use the separate maintenance and
backup procedure for schema changes. Never initialize or migrate as an automatic
Compose entrypoint. Older signed manifests and existing publication URLs remain
supported; no new database migration is introduced by this page.

The candidate query now accepts an omitted `release_id` for read-only inventory.
It returns no available target images in that mode and retains `ops.read` plus
`build.read` authorization. Supplying an invalid version still fails closed.

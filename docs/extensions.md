# Composition SDK (v0.2)

The `admin`, `agent`, `control` and `web` packages expose supported composition
interfaces. Applications import a pinned module version; they do not copy the
server, frontend or Agent implementations and do not import `internal` packages.
The standalone binaries continue to run with no extensions.

## API and frontend

Load configuration with `admin.LoadConfig`, open a dedicated database, and install
compiled `admin.Extension` descriptors in `Config.Extensions`. An extension owns a
unique lowercase namespace, explicit role/permission policies, routes, and optional
embedded assets and pages. Its private lifecycle uses `Start` and `Stop` callbacks.
Use `web.FS` as `Config.WebFS` to serve the canonical frontend from the module.

Database initialization remains explicit: run `admin.Migrate`, application-owned
schema migrations, and `admin.ExtensionPolicies` before normal startup. The latter
adds declared policies without deleting existing decisions. Normal startup never
runs extension migrations.

Routes mount under `/api/v1/extensions/<id>`. Session authentication, CSRF and the
route's declared permission are mandatory. Mutations use the same durable email
confirmation service and audit chain as core operations. Interrupted external
operations are not automatically replayed. Business approval invariants belong in
the application handlers; `Server.Allowed`, `SessionActive` and `AppendAudit` reuse
core authorization and auditing instead of introducing another identity system.

`Draft` is an explicit POST-only contract for non-activating staging operations,
such as streaming an archive. It remains authenticated, CSRF protected and audited,
but does not copy the body into the email confirmation mechanism. A draft handler
must not deploy, activate or restart anything. Declared body limits are bounded:
confirmed mutations at most 1 MiB, streaming drafts at most 8 GiB; default 512 KiB.
Read/write deadlines default to 30 seconds; composition code may choose a bounded
`RequestTimeout` appropriate for its streaming endpoints.

`PublicRoutes` are explicit GET/HEAD endpoints outside reserved core prefixes,
intended for things such as signed artifact downloads. They cannot replace core
API or Agent routes. Extension assets require a valid session. Page descriptors
returned by authenticated GET `/api/v1/extensions` are filtered by permission.
Each page's ES module exports `render(ctx)`, receiving `root`, `api`, `allowed`,
`notice`, `sessionID`, `isCurrent` and `refresh`. Render untrusted values as text;
check `isCurrent()` after asynchronous work. The API helper supports JSON and Blob
bodies and shares the core CSRF/confirmation behavior.

Extensions are trusted, compiled application code, not sandboxed downloads. There
is no runtime plugin upload or arbitrary code invocation endpoint.

## Agent actions

Register server validators and request/approval permissions in `Config.AgentActions`.
Register matching `agent.ExtensionAction` implementations in `agent.Config.Extensions`
and explicitly allow each name in the affected local service's `extensions` list.
Only then does the Agent advertise `extension.<name>.v1`.

POST `/api/v1/tasks` accepts `action: "extension"`, `extension`, schema-validated
`payload`, and a stable `request_id`, alongside `service`. A different administrator
approves through the ordinary task API. Server registration, current permissions and
observed capability are checked before approval and initial dispatch. The existing
signed command carries the frozen payload; no remote path, environment or shell
input may be accepted by extension validators or executors.

`Execute` performs the admitted business action. `Reconcile` observes or safely
recovers an interrupted action; it must not repeat a forward mutation blindly.
The core owns command identity, signature/expiry checks, serialization, durable
receipts and result delivery. Optional `Observe` returns bounded non-secret status
metadata. Optional `Guard` adds local constraints to ordinary service mutations;
terminal receipts remain replayable even after live policy changes. Configuration
write-back retains its existing journal and recovery semantics.

Protocol v1 gains optional extension name/payload, bounded result data and status
metadata; capability gating prevents old Agents receiving unsupported actions.
No core schema migration is needed: existing task payloads and host observations
already have durable JSON storage.

`Config.TaskPolicy` may impose additional application validation at submission,
approval and initial dispatch. It must not perform service mutations. Rejection
before dispatch records failure and releases the unexecuted task's lock; dispatched
or uncertain operations continue through their original recovery path.

## Composition self-update

An installation with extensions must set `ReleaseRepository` and `ReleaseWorkflow`
to its own build source before using system updates. Trusted signatures alone are
insufficient to replace a composition with an unrelated standalone image. All
candidate, preparation and publication reads reuse this source check.

Keep the API/Web recovery unit and Agent inventory outside the images. Agent
upgrades remain separate. No self-update path performs database migrations. Legacy
signed releases and standalone APIs retain their previous behavior when extensions
are absent. The optional `UnixSocket` listener is local configuration only; normal
standalone HTTP deployment remains unchanged.

Composition build workflows can pass `--workflow composition.yml` to
`scripts/build-manifest.py`; the default remains `build.yml`. The value must match
the workflow configured in the composition trust policy.

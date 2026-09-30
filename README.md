# Skygo Admin

[简体中文](README.zh-CN.md)

An independent Go administration toolkit for Skygo services: **admin-api**,
**admin-web**, and **ops-agent**. It runs without any application-specific
backend, protocol, configuration tables or assets.

- Administrator bootstrap, image captcha, optional TOTP, revocable sessions,
  role permissions, email-bound operation confirmation and hash-linked audit.
- Host enrollment/revocation, heartbeat, service inventory and dependency checks.
- Signed, separately approved service tasks with durable agent receipts.
- Digest-pinned Docker image deployment and rollback using local image allowlists.
- Versioned JSON configuration and Skygo registry publication through approved tasks.
- Optional, allowlisted GitHub workflow dispatch, signed artifact registration,
  persistent per-attempt status and paired Admin image preparation.
- Plain JavaScript browser interface; Go lifecycle managed by Skygo `app`.

## Local start

Requirements: Go matching `go.mod`, Docker with Compose, Python 3; Node.js 24 for
frontend tests. The public Skygo dependency is pinned; no adjacent checkout is used.

```sh
go run ./cmd/init-local
export LOCAL_UID="$(id -u)"
export LOCAL_GID="$(id -g)"
docker compose -f deploy/compose.yaml build
docker compose -f deploy/compose.yaml up -d mysql
docker compose -f deploy/compose.yaml run --rm admin-api -migrate
docker compose -f deploy/compose.yaml up -d admin-api admin-web
```

Open <http://127.0.0.1:18390>. Read the bootstrap token from your **local**
`runtime/secrets/bootstrap` file, initialize an administrator, and save the
returned authenticator URI and recovery codes securely. Create a second
administrator with the `approver` role: the task author cannot approve their own
operation, including when both have the superadmin role.

The local Compose example binds only loopback and explicitly disables email
confirmation and secure cookies. Production requires HTTPS, secure cookies,
SMTP confirmation, restricted proxy trust and private secret files. See
[deployment](docs/deployment.md). Configuration defaults keep confirmation,
TOTP and secure cookies enabled.

Enroll a host in the UI. Save its returned token to a `0600` file on that host;
install the public signing key and an operator-owned inventory. Then run:

```sh
ops-agent -config /absolute/path/to/agent.json
```

An agent inventory is required; it controls Compose files, service names, image
repositories, health probes, configuration paths and log access. The API cannot
supply a shell command, mount path or arbitrary health URL.

See [publication workflow and schema v3 migration](docs/publications.md) before upgrading an existing installation.

## Operations

1. Register a service matching its agent inventory ID.
2. Create a task (`health`, `start`, `stop`, `restart`, `deploy`, `rollback`,
   `configure`, or `logs`). Deployments specify `repository@sha256:digest`.
3. Another administrator approves it. The host must be online and dependencies
   ready. The agent verifies the signature and host binding before execution.
4. Review the persisted result. Lost responses replay receipts, not operations.
   Uncertain results keep the service locked until evidence or operator
   reconciliation resolves them.

Images are distributed from your approved container registry to agents by digest;
registry credentials stay on each host. This version does not upload image
archives through the management API. The GitHub workflow produces downloadable
image artifacts; load/publish them through your own release process.

Configuration versions contain **non-secret JSON**. For `skygo`, submit an array
of `{ "nodeId": "node-a", "address": "node-a:19001" }` entries; the API creates
the validated registry snapshot. Activate a version through a `configure` task.
Rollback uses another approved task referencing an older version. Secret values
belong in locally provisioned files, never configuration releases.

## Development

```sh
make test
python3 scripts/integration.py
make scan
make release
```

Integration tests create and delete their own MySQL container. `make release`
builds Linux amd64/arm64 binaries and an allowlisted source archive, scans the
archive, and writes checksums. Generated runtime files and releases are ignored.

See [API](docs/api.md), [architecture](docs/architecture.md),
[example nodes](examples/nodes/README.md), and [verification](docs/verification.md).
Licensed under Apache-2.0 with retained third-party notices.

镜像分发、续传历史、安全清理和发布配置回写见 [schema v4 说明](docs/distribution.md)。本地构建的默认直拉模式保持可用，新能力须显式配置。

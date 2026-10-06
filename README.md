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

Management self-update / 管理端自更新：[System update](docs/system-update.md).

Composition API / 私有扩展接入：[Extension SDK](docs/extensions.md).

## Public `latest` images

`latest.yml` publishes `ghcr.io/alpha2z/skygo-admin/admin-api:latest`,
`ghcr.io/alpha2z/skygo-admin/admin-web:latest` and
`ghcr.io/alpha2z/skygo-admin/ops-agent:latest` after a push to `main` passes its
checks, or a manual run on `main`. The repository needs its existing
`BUILD_SIGNING_KEY` Actions secret. Consumers of public packages do not need
`docker login`. The workflow must first be pushed and finish successfully before
these tags are available.

Each `latest` is a multi-platform index for Linux amd64 and arm64. Both builds
must succeed and their combined immutable manifest must be signed and uploaded
before promotion. Timestamp and commit tags remain available for rollback. A delayed build of an
older main commit publishes its version tags without moving `latest` backward.
Updating three registry tags is not atomic: if promotion fails partway through,
wait for a successful run before updating the stack.

Keep the three `*_IMAGE` values in your existing Compose `.env` set to the names
above. Run `docker compose pull admin-api admin-web ops-agent`, then
`docker compose up -d --no-deps admin-api admin-web ops-agent` with your deployment's
usual Compose file/profile options. Pulling alone does not replace containers;
`restart` does not load a newer image. Back up state first and perform any required
schema migration explicitly before starting a schema-changing version.

The signed publication UI continues to authorize immutable digests. `latest` is
an operator-facing download alias, not a replacement for signature, approval or
rollback identity checks. A publication with `sync_image_env` enabled persists
its approved digest; manual `latest` deployments and signed UI deployments use
different update procedures. Do not run both procedures concurrently.

Admin API 私有文件格式、参数示例、匿名下载命令和不覆盖旧文件的密钥生成器：
[配置模板下载与说明](deploy/admin-api-templates/README.md)。

各组件的独立模板可用同一脚本分别下载，不覆盖已有目录：

```sh
sh install-templates.sh --component admin-api --output ./admin-api-templates
sh install-templates.sh --component admin-web --output ./admin-web
sh install-templates.sh --component ops-agent --output ./ops-agent
```

Web、Agent目录分别包含compose.yaml、.env.example、start.sh、update.sh和参数说明。
先填配置再启动；安装脚本不初始化服务或写入真实凭据。

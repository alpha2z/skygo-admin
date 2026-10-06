# Skygo Admin

[English](README.md)

独立的 Skygo 管理与运维工具，由 `admin-api`、`admin-web`、`ops-agent` 组成。
使用公共 Skygo 依赖，可独立构建和运行，不依赖其他应用仓库。

## 功能

- 管理员初始化、图片验证码、可选 TOTP、可撤销会话、角色权限与审计。
- 与具体操作绑定的邮件确认；默认单人确认执行，开启双人模式后由另一位管理员审批。
- 主机接入与撤销、心跳、服务清单和显式依赖检查。
- Ed25519 签名指令、执行前持久化、重复回执恢复和不确定结果处理。
- 按不可变摘要分发 Docker 镜像、部署、回滚、启停和健康检查。
- 通用 JSON 配置版本，以及 Skygo Cluster 注册表发布与回滚。
- GitHub 工作流、按轮次持久化的可信版本登记、配对后台镜像准备，以及分步发布页面。

升级已有安装前请阅读 [发布流程与 schema v2 迁移](docs/releases.md)。

## 本地启动

准备 Go（版本见 `go.mod`）、Docker Compose 和 Python 3。前端测试使用 Node.js 24。

```sh
go run ./cmd/init-local
export LOCAL_UID="$(id -u)"
export LOCAL_GID="$(id -g)"
docker compose -f deploy/compose.yaml build
docker compose -f deploy/compose.yaml up -d mysql
docker compose -f deploy/compose.yaml run --rm admin-api -migrate
docker compose -f deploy/compose.yaml up -d admin-api admin-web
```

打开 <http://127.0.0.1:18390>。从本机 `runtime/secrets/bootstrap` 文件取得初始化令牌，
创建首位管理员，妥善保存界面返回的认证器信息和恢复码。
仅在 `ADMIN_INDEPENDENT_APPROVAL_ENABLED=true` 时另建一位 `approver` 管理员审批服务任务；默认一个管理员即可确认执行。迁移与历史任务处理见 [审批模式说明](docs/approval.md)。

初始化会生成全新的凭据，文件权限受限，且拒绝覆盖已有目录；不会在终端打印凭据。
`runtime/`、`.env`、密钥文件、日志及构建产物均不进入源码仓库。

本地 Compose 仅绑定回环地址，并明确关闭邮件确认和安全 Cookie，以便本地体验。
生产部署须启用 HTTPS、安全 Cookie、SMTP 和严格的代理信任配置，不能直接沿用本地设置。
服务正常启动不会自动执行数据库迁移；迁移需要单独运行 `-migrate`。

## 接入主机与服务

1. 在管理页面创建主机，把一次性返回的主机令牌保存到该主机的私有文件。
2. 安装签名公钥，配置本地主机清单。清单决定允许管理的 Compose 文件、服务、镜像仓库、
   健康检查地址和配置文件路径；这些权限不能通过远程任务任意扩大。
3. 执行 `ops-agent -config /绝对路径/agent.json`。
4. 在管理页面登记与清单一致的服务 ID，确认创建任务并执行；双人模式下再由另一位管理员审批。

镜像必须采用 `仓库@sha256:摘要` 格式，从允许的 Registry 拉取；Registry 凭据留在主机。
首版不提供管理 API 上传镜像归档的接口。GitHub 工作流可以生成镜像文件，发布到哪个
Registry 由使用者的发布流程决定。

配置版本只存放非敏感 JSON，密码和密钥使用本地私有文件。选择 `skygo` 格式时提交
`nodeId/address` 数组；服务端生成合法的注册表快照，通过审批后的 `configure` 任务应用。
回滚时创建引用旧版本的新任务。

Agent 会保留任务回执，断线重试不会重复执行同一指令。未下发的任务超时可释放服务锁；
可能已执行的任务保留锁，等待核对。不要删除回执强制重跑。明确核对后，可以停止 Agent，
使用 `-resolve-failed TASK_ID` 将不确定任务标记为失败，再启动 Agent 回传结果；这不会执行
任何服务操作，也不会将失败伪装为成功。

## 验证与文档

```sh
make test
python3 scripts/integration.py
python3 scripts/docker-smoke.py
make scan
make release
```

集成测试只使用自己创建的临时数据库和容器，完成后清理。发布脚本生成 Linux amd64/arm64
二进制、白名单源码归档及校验清单，并扫描源码包和二进制。

- [架构与任务状态](docs/architecture.md)
- [部署、凭据和升级](docs/deployment.md)
- [API v1](docs/api.md)
- [两个 Skygo 示例节点](examples/nodes/README.md)
- [已执行验证与未验证项](docs/verification.md)
- [第三方依赖许可清单](docs/dependencies.md)

采用 Apache-2.0，保留适用的第三方许可证与署名。

API/Web 自升级使用[选择式发布与 schema v3](docs/publications.md)，先显式准备镜像，再按配置确认执行或独立审批；首次 schema 升级使用手动维护流程。

镜像分发、续传历史、安全清理和发布配置回写见 [schema v4 说明](docs/distribution.md)。本地构建的默认直拉模式保持可用，新能力须显式配置。

Management self-update / 管理端自更新：[System update](docs/system-update.md).

Composition API / 私有扩展接入：[Extension SDK](docs/extensions.md).

# Admin 集中分发与 Compose Agent 自升级

GHCR 凭据只保存在 Admin 的私有 `token_file`。Admin 按可信签名发布清单、架构与
包白名单下载镜像，验证后生成归档。Agent 通过现有身份认证从 Admin 获取归档，校验、
导入本地 Docker；准备镜像不重启服务。所有部署使用 `--pull never --no-build`。

## 安装与迁移

1. 保留管理数据库、原 Agent 身份、状态目录和部署配置，完成或核对进行中的任务。
2. 在 Admin 配置 `ADMIN_DISTRIBUTION_CONFIG`，启用集中分发，挂载可写持久化缓存。
   Token 文件限制为 Admin 可读，不复制到 Agent；轮换文件后下一次下载读取新凭据。
3. 首次安装和旧 Agent 过渡升级由运维使用在 Admin 主机集中准备的离线镜像完成。
   Admin 尚未启动时，由该主机的引导流程准备安装镜像；启动脚本不执行 pull/load/build。
4. 部署一个独立常驻 `agent-updater` 容器，使用已导入、固定版本的 Agent 镜像，
   以 `ops-agent -updater -config <本机清单>` 启动。它与 Agent 共享本机绝对部署路径、
   持久化状态、主机身份和 Docker Socket。`OPS_UPDATER_IMAGE` 不随 Agent 版本修改。
5. 清单配置 `self_service` 和私有 `updater_socket`；对应服务须开启 `sync_image_env`，
   指向本机 Agent Compose 服务及专用 `OPS_AGENT_IMAGE`。该服务不能属于 API/Web 恢复单元。
   Unix Socket 为 0600，父目录为私有目录。两容器须使用有权访问它的相同用户。
6. Admin 登记同名服务，设置 `control_plane=true`、`control_kind=ops-agent`、实际架构。
   心跳应报告 `image.archive.v1` 和 `agent.self-update.v1`。缺少能力时先人工安装过渡版本。
7. 容器 Agent 以原路径只读挂载 Docker daemon 的实际存储根目录，供归档导入容量检查。
   不支持无法读取 daemon 存储文件系统的远程 Docker 配置。

本改造复用现有任务/交付表，无新增数据库结构。历史直拉任务不转换为新的执行许可：
未派发项停止派发并持久化失败，已经执行或结果不明的任务保留锁和回执，人工核对后再升级。
主机原 Docker 凭据由运维按实际共享用途清理，系统不自动删除。

## 操作与接口

后台 **Agent 升级** 选择服务及可信版本，先准备镜像，再确认执行或提交独立审批。
页面 URL 保存准备任务及升级任务 ID；刷新或重新登录只查询原任务，不自动重复提交。
Agent 升级与 API/Web 更新独立执行，当前主机的执行中、不明任务或工作流会阻止升级。

- `POST /api/v1/agent-updates/prepare`：`request_id`、`service`、`release_id`。
- `POST /api/v1/agent-updates`：上述字段，加已成功的 `preparation_id`；准备回执须在五分钟内。
- 查询、审批、拒绝和取消复用 `/api/v1/tasks`；仍要求鉴权、CSRF、配置的邮箱确认及审批。
- `upgrade-agent` 命令签名冻结目标镜像 ID、原镜像 ID、配置摘要、原 Boot ID 和发布 ID。
- `/agent/v1/self-update/:id/confirmation` 仅供该主机验证新 Boot ID、镜像身份与近期健康心跳。

## 故障恢复

执行器先验证签名、有效期、本地清单、目标及回滚镜像，再持久化交接记录和环境变量日志。
仅重建清单指定的 Agent 服务。新 Agent 使用原身份恢复，Admin 确认新的 Boot ID 和目标
镜像的健康心跳后，任务才成功。网络中断不被视为成功；超时恢复旧镜像及原环境变量。

执行器状态存放在 Agent 状态目录的 `updater/` 下。重启执行器会从记录恢复，已经开始的
环境变更不重复向前执行。重放同一任务返回原结果；其他升级在旧结果未交付时被拒绝。
回滚失败、配置漂移或日志异常保留不明结果与锁，不删除状态、不强制清锁。

排查时核对日志、实际容器 image ID、环境变量与 Admin 心跳；不要输出令牌或整个环境文件。
修复本机配置后重启执行器恢复。数据库迁移、游戏初始化、宿主机二进制更新和执行器自身更新
不由本流程执行；这些操作需要独立维护。

## 本地验证证据

本轮通过 `make test`（含竞态及前端）、隔离 MySQL `scripts/integration.py`、
源码/历史扫描和 Linux 双架构发布构建。`scripts/archive-smoke.py` 验证真实 Docker
归档导入及续传；`scripts/agent-update-smoke.py` 验证实际 Agent 容器替换及新进程心跳；
`scripts/docker-smoke.py` 验证预先准备镜像后的服务部署/回滚与 API/Web 恢复单元。
自升级 smoke 使用合成控制端和预先验证的本地缓存，归档字节链路单独验证。
Docker Hub 不可用时，可用 `SKYGO_ADMIN_DOCKER_CLI_IMAGE` 指定已安装的 Docker CLI 镜像。
Docker Desktop 的 Unix Socket 测试使用独立 Linux 数据卷，测试结束清理。

以上不代表真实 GHCR 凭据、远程安装或现网流程已验收。

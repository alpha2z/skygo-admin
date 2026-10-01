# 选择式发布与 schema v3

当前版本为 schema v5，见 [流程说明](workflows.md)。此前 schema v4 升级、归档分发与镜像变量持久化见 [分发说明](distribution.md)。

默认「Publish services」页面把镜像缓存准备与服务发布分开：选择主机和一个可信版本，
勾选需要更换镜像的服务，点击 **Sync selected images**，收到新鲜回执后再点击
**Submit for independent approval**。另一位具有 `ops.approve` 权限的管理员审批后才执行。
准备成功、自动登记成功、刷新或重新登录都不会自动批准升级。

## 首次 v3 升级

自动发布只切换镜像，不执行数据库迁移。首次安装 v3 应走独立维护流程：

1. 等待旧任务结束；核对 uncertain 任务，保留 Agent 日志。备份管理数据库及私有配置和密钥。
2. 更新 Agent 二进制并保留 `state_dir`；安装下面的本地恢复单元配置，确认普通心跳正常。
3. 正常停止旧 API/Web，使用 v3 的 `admin-api -migrate` 显式迁移数据库。
4. 启动配套 v3 API，再启动 v3 Web；确认健康检查、静态资源、登录及 Agent 心跳。
5. 在服务清单设置两项 `control_plane=true`、`control_kind=admin-api/admin-web` 与实际架构。
   等待两项都上报 `control.unit.v1`、镜像 ID 和本地配置指纹后，才能使用页面自升级。

v3 新增 `publications` 表和 `tasks.publication_id`，保留账号、会话、历史版本、任务与镜像回执。
迁移不导入任何业务数据库。API 正常启动严格检查 schema 版本，不自动迁移或降级。
跨 schema 回退必须停止 API/Web，按备份恢复兼容数据库并运行对应版本；不能仅改版本标记。
Agent 不在 API/Web 恢复单元内，其升级应单独安排并保留状态目录。

## Agent 本地授权

在私有 `agent.json` 中显式增加：

```json
{
  "control_unit": {"id": "admin", "api": "api", "web": "web"}
}
```

`api`、`web` 是同一 Agent 的本地 `services[].id`，各自必须配置 Compose 文件、服务名、
镜像环境变量、镜像仓库允许列表及健康检查 URL；示例见 `examples/control-unit.json`。
服务清单中的 ID 必须一致。只允许 API/Web 两项，不接受远程路径、环境变量、Shell 或第三项。

两项按服务依赖顺序启动、反向停止；没有内部依赖时先停 Web、后停 API，先启 API、后启 Web。
只勾 API 时，版本只需包含 API 镜像；Web 使用观察到的原始镜像 ID，并参与停止、启动、
健康检查及失败回滚。外部健康依赖必须满足，有正在运行的外部依赖者时拒绝影响它们。

## 授权与恢复边界

客户端只提交 `request_id`、`host_id`、`release_id` 和 `selected` 服务 ID。
服务端解析签名版本、镜像摘要、原镜像与本地配置指纹，冻结并签名执行范围。
同一请求与请求人返回原记录；改变内容复用请求 ID 被拒绝。每条发布仅有一个确定的执行任务 ID。

提交、审批、首次分发都检查构建签名、主机启动身份与在线状态、服务定义、镜像身份、配置指纹、
依赖和五分钟内的准备回执；审批与分发还检查请求人和审批人的当前权限。
范围改变必须重新创建请求，不会静默扩大授权。审批同时锁定 API/Web，两项结果均得到验证才解锁。
后台控制面执行期间禁止批准其他普通任务，保留旧任务与配对准备接口供兼容使用。

Agent 在修改前原子写入签名指令和阶段日志，仅使用本地已验证镜像，不在停机窗口拉取镜像。
API 断线不影响本地继续执行。重启时先读取本地日志，不依赖控制面：已完成且健康的目标可核对成功，
其余已开始执行的单元进入原镜像恢复，不重新开始一次正向升级。
执行或回滚无法确认、配置指纹变化时保留 uncertain 状态和两项锁；恢复本地配置后重新核对。
不要删除日志、手动清锁或使用普通单服务强制完成操作处理恢复单元。

未下发的过期请求可安全终止；已经可能下发的任务即使过期仍保留身份与锁，允许核对和回滚。
回执必须证明两项服务的健康状态和不可变镜像 ID；重复回执不会触发新任务。
浏览器 URL 只保存选择与请求 ID，不保存凭据。刷新、重新登录和恢复连接查询原记录。
已选主机失效不会自动换主机；来源无法按不可变镜像匹配时显示 unmatched/unknown，不猜测标签。

## 自动可信登记

私有 GitHub 配置增加 `auto_register`，默认 false，与 `publish_images` 独立。
启用后每 30 秒查询最近最多 150 个运行，每轮验证最多三个产物；失败以 30 秒至 8 分钟退避。
仅允许已配置仓库、工作流、分支、成功运行、匹配提交及轮次的签名产物。
成功记录 `system.build.register` 审计，操作者为系统 ID 0；不会冒充管理员，不会创建发布任务。
手动登记仍可用，重复登记不重复写入成功审计，数据库查询失败保持 unknown。

## 公共 API

| 接口 | 行为与权限 |
| --- | --- |
| GET `/api/v1/publication-candidates?release_id=…` | Host 清单、目标、镜像来源和准备状态；`ops.read` + `build.read` |
| POST `/api/v1/publication-preparation` | 只缓存选中服务镜像；`ops.write` + `build.read` |
| POST `/api/v1/publications` | 保存冻结范围，等待独立审批；`ops.write` + `build.read` |
| GET `/api/v1/publications`、`/:id` | 查询发布记录与稳定执行 ID；`ops.read` |
| POST `/api/v1/publications/:id/approve` | 重新检查范围并入队；`ops.approve`，必须不同管理员 |
| POST `/api/v1/publications/:id/reject` | 拒绝尚未执行的请求；`ops.approve` |

POST 准备和创建的请求示例：

```json
{"request_id":"pub-example","host_id":"control-host","release_id":"ci-1-1","selected":["api"]}
```

所有浏览器写接口保留会话、CSRF、权限与已启用的邮件确认。Agent 协议仍为 v1，
通过 `control.unit.v1` 协商可选的 `control-unit` 指令。旧 Agent 不会收到恢复单元任务。

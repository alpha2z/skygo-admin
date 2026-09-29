# 构建信任与兼容发布接口

当前版本使用 schema v3，首次升级、选择式发布与 API/Web 恢复单元见 [发布说明](publications.md)。
下文保留构建信任与旧配对准备接口的兼容语义；新发布使用默认 Publish services 页面。

本版将发布管理分为「构建记录 → 可信版本与镜像准备 → 升级任务」，发布设置集中展示
已加载的 GitHub 配置、实际连接检查结果和构建签名公钥。后台仅包含三种公共组件：
`admin-api`、`admin-web`、`ops-agent`。

## 从首版升级

1. 备份管理数据库，保留任务日志、JWT 密钥、任务签名密钥和主机令牌。
2. 正常停止旧 API，在同一数据库上运行新版本 `admin-api -migrate`。
3. 启动配套的新 API/Web，并更新 Agent。正常启动仍不会自动迁移。
4. 在服务清单中为同一主机的 API、Web 设置 `control_plane=true`，分别设置
   `control_kind=admin-api/admin-web` 及相同的 `platform=linux/amd64` 或 `linux/arm64`。
5. 将实际 CI 镜像仓库加入对应服务的 Agent 本地 `allowed_images`，配置必要的 Registry 拉取凭据。
6. 等待 Agent 上报镜像 ID、实际架构与 `image.prepare.v1` 能力，再准备镜像。

schema v2 新增 `build_trust_keys`、`image_releases`、`image_preparations` 三张管理表，以及
`tasks.finished_at` 可空字段。迁移保留既有账号、会话、审计和任务；旧 schema 不会被新 API
静默接受，旧二进制也不应直接运行在 v2 数据库上。需要回退时使用备份和对应旧版本，不能
仅修改版本号。原有普通服务定义可继续使用，不会凭服务名称猜测其控制角色。

## 构建签名与 GitHub

任务签名密钥与构建签名密钥必须独立。先在私有位置创建新的构建密钥目录：

```sh
go run ./cmd/sign-images -generate-dir /absolute/private/build-keys
```

命令拒绝覆盖已有目录，不打印密钥内容。将 `signing.pub` 的内容加入「Release settings」；
将 `signing` 内容保存为 GitHub Actions 的 `BUILD_SIGNING_KEY` Secret。旧公钥保留用于验证
历史发布，不上传私钥到管理 API。新增公钥需要 `admin.manage` 和 `build.write` 权限，以及
已启用时的邮件确认。

`build.yml` 新增可选 `publish` 输入，默认 false，保留原来的镜像归档构建模式。启用时：

- 使用当前仓库的工作流身份向 `ghcr.io/<owner>/<repo>/<component>` 推送镜像。
- 对仓库、工作流、提交、分支、运行 ID、轮次和不可变镜像摘要生成独立签名。
- 输出 `skygo-admin-signed-images-<run_attempt>` artifact，内部仅有 `signed-images.json`。
- 私钥临时文件位于 runner 的临时目录，并在清理步骤中删除。

本地 `ADMIN_GITHUB_CONFIG` JSON 增加 `publish_images: true`，才会让管理界面触发的构建
启用上述发布流程。未配置时仍只构建归档；此类构建没有签名清单，不能登记为可信版本。
GitHub 账号需具备对应包发布权限。新创建的 GHCR 包不保证自动公开；包可见性和 Agent 拉取
凭据由部署者配置。没有使用任何内置 Token 或真实 Registry 账号。

登记时重新读取 GitHub 运行与 artifact 元数据，验证允许的仓库、工作流、分支、成功状态、
轮次、下载来源、归档 SHA-256 和构建签名。登记以 `ci-<run_id>-<attempt>` 为不可变身份，
相同内容重复请求返回 `already_registered=true`，不重复写成功审计；不同内容拒绝覆盖。
新轮次不会继承旧轮次的登记。数据库读取失败或存量内容冲突时返回 `unknown`，不能把错误
解释为「尚未登记」。构建公钥缺失、来源不匹配及下载失败使用固定安全错误码，不返回 Token、
凭据文件路径、签名下载 URL 或原始服务错误。

## 镜像准备与升级边界

选择可信版本和明确的主机后，准备操作只接受同机、同架构、唯一的 API/Web 配对。
单一有效目标可以默认选中；多目标必须选择。已选目标失效后不会偷偷切换主机。

服务端核对预览批次、主机在线状态、控制角色、架构、构建信任及 Agent 能力，生成两条
签名 `prepare-image` 指令。该指令只从本机允许的 Registry 按摘要拉取镜像并 inspect，
不运行 Compose、不创建或替换容器、不修改配置、不执行迁移。Registry 凭据仍保留在
Agent 主机；本版没有迁入中央归档缓存或中央 Registry 凭据体系。

准备操作需要 `ops.write` 与启用时的邮件确认；因为它不修改服务，不要求第二位管理员审批。
实际 `deploy`、`rollback` 等服务任务仍必须独立审批，可信镜像清单不代替升级授权。

任务按主机、目标服务、架构和镜像身份复用；进行中的项不重复创建，部分失败仅重试失败项，
新鲜的成功项保留。回执绑定主机启动身份，五分钟后或主机重启后显示待核对；再次准备会先
检查本地缓存，避免重复下载。离线、旧 Agent、架构不一致、角色歧义和缺少配对都阻止就绪。
网络结果不确定时先读取持久化状态，不直接重复提交。

「镜像就绪」仅表示镜像缓存已准备好。下一步进入选择式发布页面，仍需明确提交与独立审批。
部署会优先验证已缓存的不可变镜像，再按需从 Registry 拉取；不会把本地标签误当作 Registry。
准备回执和部署成功状态分别显示。

## 新接口

均位于 `/api/v1`，沿用会话、CSRF 和权限校验：

| 接口 | 含义 |
|---|---|
| GET `/release-settings` | 配置与连接检查、公钥、管理能力 |
| POST `/release-settings/keys` | 添加独立构建公钥 |
| GET `/builds` | 新增 `run_attempt`、持久化 `registration`、`can_register`、信任状态 |
| POST `/builds/:id/register` | 按请求 `attempt` 验证并登记签名产物 |
| GET `/releases`、`/releases/:id` | 查询可信清单和来源；历史版本不依赖当前列表页 |
| GET `/releases/:id/admin-preparation` | 配对目标、批次、状态与准备能力 |
| POST `/releases/:id/prepare-admin` | 提交 `host_id` 与预览返回的 `batch`，幂等准备两项镜像 |

Agent v1 消息增加可选的镜像 ID、架构与能力字段；只有上报新能力的 Agent 才会收到新的
准备指令。普通服务管理消息保持原有语义。

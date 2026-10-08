# Composition SDK (v0.2)

Current approval policy: single confirmation by default; independent approval
requires `ADMIN_INDEPENDENT_APPROVAL_ENABLED=true`. Existing records keep their
original policy. See [schema v6 migration and API behavior](approval.md). References
to separate approval below describe the enabled mode.

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

## 前端语言接口

通用管理界面支持 `en`、`zh-CN`、`ja`，首次访问默认英语。页头语言选择器将
偏好保存至浏览器的 `skygo-admin.locale`，切换后重新加载当前路由；不向服务端
提交语言设置。浏览器禁止存储时使用英语，并提示无法保存新偏好。

`render(ctx)` 增加两个兼容字段：`locale` 为当前语言，`t(key, params?)` 翻译
通用界面文案并替换命名参数，例如 `t('Execution {id}', {id: taskID})`。
`AdminLocale.text(key, params?)` 继续可用。缺失目标语言回退英语，未知键保持
原文；参数仅作为文本插值，不解释 HTML。扩展应将返回值写入 `textContent`。

扩展拥有自己的业务文案和字典；这些接口不会自动翻译扩展标题、用户输入、
服务标识、日志或业务数据。现有扩展无需修改即可运行。通用数据面板只翻译
自身控件，保留数据提供方的字段标题、标签与记录。

有未提交表单时，切换语言先要求确认；API 请求、邮件确认及结果弹窗期间禁止
切换。切换不自动重放变更请求。协议值、审计原文、内部错误及邮件内容不变。

## 运行时插件自定义查询权限

运营者可在 `ADMIN_PLUGINS_CONFIG` 的单个 provider 中显式声明
`"permissions": ["example.stats.read"]`，然后由该 provider 的页面和路由引用。
最多 64 个合法且不重复的权限名称；未声明且非内置的权限仍拒绝注册。
这是本地受控配置，不接受插件响应动态扩展权限，不影响其他 provider 的权限范围。

声明只允许注册，不在正常启动时修改数据库授权。新权限需先通过显式增量迁移
授予预期角色；其他角色仍默认拒绝。签名包、会话、CSRF 和路由鉴权继续生效。
插件页面必须导出 `render(ctx)`；`mount(ctx)` 不是宿主调用入口。

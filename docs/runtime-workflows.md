# 配置式插件工作流

运行时插件通过本地 Unix Socket 的 `workflow-plan` 和 `workflow-validate` 操作提供结构化计划。核心负责会话、权限、确认、审批策略、数据库任务、锁、签名和执行；插件不获取核心数据库连接。

插件签名清单须声明 `workflow-plan`，安装配置另外明确授权服务、动作和权限。声明能力不会自动授予管理员权限。

```json
{
  "workflows": [{
    "name": "example-environment",
    "permission": "ops.write",
    "approval_permission": "ops.approve",
    "services": ["worker", "entrance"],
    "actions": ["extension", "health", "stop", "start", "prepare-image", "deploy", "rollback"]
  }]
}
```

该字段属于已有 provider 配置。`services` 必须是 provider 本地授权服务的子集，不包含 API/Web 控制组件。完整环境必须进入计划，不能遗漏成员。扩展步骤名称还必须在 provider `actions` 配置中显式授权。

- 规划请求使用 `plugin.WorkflowPlanningRequest`，只含请求身份、用户意图、授权服务定义、Agent 观察及近五分钟内成功的镜像准备回执；不包含主机令牌或数据库对象。
- 返回既有公开 `admin.WorkflowPlan`。目标和指令必须固定插件 revision；计划创建、批准和调度前检查定义、主机、镜像身份、配置指纹及 revision。
- `workflow-validate` 对被冻结的完整计划再次检查业务策略和构建信任。不能通过重新规划扩大已批准范围。
- `observation_only` 服务只接受健康检查；包括缓存准备在内的其他工作流动作均拒绝。
- 主机插件收到的 `observation` 由本机公开 Agent 的 Docker 驱动读取，与本次服务绑定。私有插件不需要 Docker Socket，也不负责执行 Docker 命令。
- 插件暂停、变更或查询失败保持任务锁；执行结果不确定时核对原任务，不因超时自动重试业务变更。

浏览器沿用 `/api/v1/workflows/preview`、`/api/v1/workflows` 和既有详情接口。提交必须带预览 hash 和稳定 request_id。刷新或断线恢复查询原任务。

运行时插件不改变公开 API/Web 镜像组成；安装插件后仍可更新公开镜像。编译进镜像的私有扩展继续要求独立发布来源，不能误装纯公开镜像。

数据库结构未因本次工作流契约增加而改变，当前仍为 schema v6。现有 v5 数据库升级至 v6 必须独立显式迁移。

## v0.5.1 准备回执绑定

运行时插件的每个 deploy 步骤必须在 plan.preparations 中绑定核心发出的准备回执（任务、主机、服务、镜像、实际镜像身份、平台、插件版本和完成时间）。核心在预览、批准及首次调度时重新读取原回执并检查五分钟新鲜度；一旦操作可能已执行，不让自然过期阻断同一冻结计划的回滚，但回执身份与内容仍必须匹配。

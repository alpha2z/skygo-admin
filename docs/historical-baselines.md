# 历史镜像回退基线

历史基线仅为既有、缺少 Registry digest 的运行镜像提供一次性迁移和回退依据；不属于 CI 发布清单，也不能作为新版本部署目标。新版本继续由 GitHub 构建签名，管理端中央下载后分发，Agent 不拉取 Registry。

## 身份与登记

使用镜像内 `baseline` 工具，不在服务器现场编译。`baseline -mode capture -config <agent.json> -service <id>` 只读取现有服务，输出主机、服务、标签、运行 ID、平台和经本机令牌 HMAC 的配置版本。捕获前后应保持容器和配置不变。只使用已登记的本机服务，不接受远程提供的命令或挂载。

按具名标签执行 `docker image save`，保存完整归档；逐份校验归档 SHA256 与大小，复制到管理端受限目录，以 `<sha256>.tar` 命名。在隔离 Docker 缓存加载并核对运行 ID 和归档配置 ID，不启动业务容器。登记记录补充 `archive_sha256`、`archive_size`、`operator` 和 `reason`；`config_id` 由归档读取，与 Docker 运行 ID 分开。对于 manifest 类型运行 ID，必须验证归档内 OCI 根到配置的完整关联。

`baseline -mode sign -record <capture.json> -directory <protected-directory> -key <management-signing-key-file>` 在完整校验归档后，用管理签名密钥和独立签名域产生 `<baseline-id>.json`。同一记录重复执行幂等，不能覆盖不同内容；记录中的创建时间来自 capture。该工具由已有 root 运维入口调用，记录和签名均保留作为登记回执。签名不冒充 GitHub 构建来源，不向聊天或日志输出密钥。

将签名记录和归档通过认证连接复制到对应 Agent 的 `<state_dir>/baselines/`，再次校验摘要。在重新捕获并核对原始身份和配置版本后，原子写入 `<service>.active`，内容为 baseline ID。目录及文件仅允许运维主体和服务进程访问。管理 API 设置 `ADMIN_HISTORICAL_BASELINES_DIR` 指向中央只读挂载目录。API 和 Agent 均验证签名及归档；任何一端缺失或损坏都会阻断相关发布。

## 工作流与回退

节点上报及工作流目标新增可选 `historical_baseline_id`；命令新增 `workflow_baseline_id` 用于当前作用域绑定，以及仅用于回退的 `historical_baseline_id`。使用 `workflow.historical-baseline.v1` 能力识别；旧 Agent 不接受历史标签回退命令。原 Registry digest 路径保持兼容。

配置值仍是原标签，真正授权绑定的是签名基线、运行 ID、归档摘要和稳定配置版本。当前标签改指其他内容、平台不符、配置改变或基线被撤销时拒绝执行。回退归档只允许恢复同一主机和服务的已登记镜像；新版本升级不得使用基线。归档丢失时不得靠本机仍存在镜像来忽略备份缺失。

基线归档位于独立目录；中央和 Agent 清理同时保护登记镜像 ID。撤销需在不存在未完成或仍依赖该记录的回退任务时，由运维人员移除活动指针和记录；默认不自动过期或清理。上线前先升级 API 与 Agent，再启用登记；游戏插件需要使用匹配版本的公开 SDK。

## 验证状态

自动测试包含签名与内容身份、重复签名、损坏/错平台/错标签/错配置、命令用途限制；Docker 隔离回归扩展覆盖历史版本升级、失败恢复、显式回退、重启回执复用和标签漂移。上线验收须单独记录，不以编译或单元测试代替实际部署。

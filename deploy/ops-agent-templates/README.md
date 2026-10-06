# Ops Agent 独立模板

## 本组件专用安装脚本

```sh
curl -fL https://raw.githubusercontent.com/alpha2z/skygo-admin/main/install-ops-agent.sh -o install-ops-agent.sh
sh install-ops-agent.sh
```

默认下载到新目录 `./ops-agent-templates`。支持 `--output` 指定新目录、`--ref` 固定完整提交SHA，
以及 `--help`。无需下载其他组件脚本，也无需传入组件参数。脚本只下载本组件模板，
不覆盖配置、不生成接入身份、不启动容器；其余配置步骤见下文。


每台被管理的物理主机运行自己的Agent，拥有独立主机令牌和持久化执行记录。
本模板不生成假的接入令牌，不注册主机，也不自动授权任何服务。

```sh
sh install-templates.sh --component ops-agent --output ./ops-agent
cd ops-agent
cp -n .env.example .env
cp -n private/agent.json.example private/agent.json
```

先在后台“机器接入”创建主机，将返回的令牌保存为 `private/agent-token`，将对应API的
Base64 Ed25519公钥保存为 `private/signing.pub`，再填写 agent.json。不要将私钥复制给Agent。
现有主机迁移应保留原身份和 state/，不要生成新令牌覆盖，也不同时运行两个相同身份的Agent。

```sh
chmod 700 private
chmod 600 private/agent.json private/agent-token private/signing.pub
```

## .env 参数

| 参数 | 示例与说明 |
| --- | --- |
| COMPOSE_PROJECT_NAME | `skygo-ops-agent`，只管理本目录Agent容器。 |
| OPS_AGENT_IMAGE | `ghcr.io/alpha2z/skygo-admin/ops-agent:latest`，匿名拉取。 |
| MANAGED_DEPLOYMENTS_DIR | `/opt/skygo-admin`，实际被授权服务部署目录的绝对父路径。以相同绝对路径挂载到Agent，供Docker Compose读取配置与回写镜像变量。不要使用 `/` 作为范围。 |

模板使用Linux host网络和Docker Socket；Agent可以操作本机Docker，远端动作仍须通过签名和
本地清单授权。Docker Desktop需支持host网络；不支持时调整网络并使用可达的HTTPS管理地址。
`state/` 持久化任务、回执、缓存；更新不删除它。跨主机部署时，Agent留在被管理服务所在主机。

## agent.json 参数

| 字段 | 示例与说明 |
| --- | --- |
| host_id | `host-example-01`；必须与后台登记主机ID完全一致。 |
| api_url | `https://admin.example.net`；只写origin，不附加 `/api/v1`，代理需转发 `/agent/`。 |
| allow_loopback_http | 生产默认false。仅同机测试可用true并将api_url改为 `http://127.0.0.1:18391`；不会允许任意远端HTTP。 |
| token_file | `/run/private/agent-token`，对应主机令牌，非登录密码、JWT或bootstrap令牌。 |
| public_key_file | `/run/private/signing.pub`，API指令签名公钥，非构建发布公钥。 |
| state_dir | `/var/lib/skygo-admin/agent`，已映射到本机state目录。 |
| services | 默认空数组，能登记心跳但不能操作任何服务。按下文显式添加。 |

### API/Web 授权与恢复单元示例

`private/agent.control-unit.json.example` 展示两项完整服务及同机恢复单元；它只是示例，
不要原样覆盖当前agent.json。复制前填写所有路径、项目名和服务键，并确认两个服务确实由本机管理。

| 服务字段 | 含义与示例 |
| --- | --- |
| id | 后台服务ID，例如admin-api。 |
| compose_file | `/opt/skygo-admin/admin-api/compose.yaml`，本机真实绝对路径，须位于挂载范围。 |
| env_file | `/opt/skygo-admin/admin-api/.env`，本机真实路径。 |
| project | 对应Compose项目名，例如skygo-admin-api；以实际 `docker compose ls` 为准。 |
| compose_service | 对应Compose文件services下的键，例如admin-api；如果文件实际写api就填api。 |
| image_variable | 对应 `.env` 的镜像变量，例如ADMIN_API_IMAGE。 |
| allowed_images | 精确允许的镜像仓库，不附加标签或digest；示例只允许该组件的公开仓库。 |
| health_url | Agent可达的健康地址，例如 `http://127.0.0.1:18391/healthz`，必须与实际端口一致。 |
| sync_image_env | true时签名发布成功后持久化已批准的镜像身份；需要可写部署目录。仅准备镜像不回写。 |
| allow_logs | 示例默认false；明确需要日志访问时才设true。 |

control_unit 的 api/web 引用上述服务ID，不引用容器名；Agent自身不属于恢复单元。
签名发布回写的digest用于保证审批身份；若采用手动latest更新，应明确使用该服务的latest配置，
不要同时运行两种更新流程。不要把业务专用动作当作通用服务操作绕过业务检查。

## 启动和更新

填写并核对后：

```sh
docker compose pull
./start.sh
# Update only Agent, preserving state:
./update.sh
docker compose ps
docker compose logs --tail 50
```

启动后在后台确认主机心跳、能力和服务健康；容器running不代表登记成功。
对首次服务动作使用非破坏性健康检查，并走既有独立审批流程。

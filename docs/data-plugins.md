# 数据插件（契约 v1）

运行时数据插件复用 `resource` 协议和签名安装机制。核心负责鉴权、请求校验、有界查询和通用展示；指标含义、数据来源及历史存储由提供者实现。无需导入核心内部包，也无需在新增数据集时重新编译 Admin。

## 安装

首次使用需要包含数据面板功能的 Admin 版本。之后使用同一公开构建，替换提供者或扩充其目录即可接入不同业务。

1. 插件签名清单声明 `data-read` capability；不因声明自动授予任何管理员权限。
2. 在已有 `ADMIN_PLUGINS_CONFIG` provider 条目中增加：

```json
{"data_panels":[{"id":"overview","title":"Data monitoring","permission":"ops.read"}]}
```

保留原有 `id/socket/revision/manifest_file/public_key_file` 配置。只有数据面板时无需 assets 目录、私有 JavaScript、业务 action 或显式配置 `/data` 路由。`data_panels` 是安装授权；核心验证清单包含 `data-read`。面板 ID 必须与 catalog 一致。变更安装配置后重启 API，页面刷新后生效。

3. 提供者通过受限 Unix Socket 接收 `POST /v1/call`，处理 `operation=resource`。认证身份由核心注入，提供者必须再次校验 `actor.id`、`actor.permission`、方法和配置中的数据范围。
4. 角色需要已有 `ops.read` 权限。启动和目录读取不会修改权限数据库。本版本不增加数据库迁移。

现有静态扩展、业务资源和任务协议保持兼容。撤销面板配置并重启即可移除面板及数据路由；无需清空数据存储。

## HTTP/JSON 契约

浏览器接口前缀为 `/api/v1/plugins/<provider>/data`。ResourceRequest 的 path 从 `/data` 开始，GET query 原样按多值映射传递。接口仅支持读取：

| 路径 | 参数 |
| --- | --- |
| `/catalog` | 无 |
| `/datasets/:id/current` | `filter.<dimension>`，可选 `group` |
| `/datasets/:id/series` | 同上，加必填 `start/end`（RFC3339）、`step`（正整数秒） |
| `/datasets/:id/rows` | 筛选及 `page/limit/sort/order`；页码从 1 开始，order 为 asc/desc |

目录描述数据集的 `id/title/unit/modes/dimensions/fields` 和面板的 `id/title/widgets`。widgets 仅支持 `card/chart/table`，分别绑定 current/series/rows。目录不接受脚本、HTML、SQL 或查询表达式。配置未授权的面板和仅被其引用的数据集不会返回给浏览器。

Go 类型及运行校验见 `plugin/data.go`，机器可读结构见 [data-v1.json](schemas/data-v1.json)。字段 ID 和维度 ID 使用小写字母、数字、连字符；schema 之外的跨字段约束由 Go 校验器执行。

结果包含 `version=1`、`sampled_at`、`state`、`warnings`，按模式返回 `values/series/rows`。状态为 `ok/partial/stale/unavailable`，数值缺失使用 null，真实零值返回 0。表格的 total 是过滤后的总行数。提供者负责业务聚合语义；核心不对不同指标擅自求和。

单次调用含目录和数据查询，合计超时 5 秒，断开请求会取消上游上下文。结果限制为 256 KiB、历史 15 天、每序列 1000 点、10 条序列、每页 100 行。提供者应在序列中显式填入 null 缺口。核心拒绝未知参数、数据集、字段、模式和异常响应形状；错误不包含连接凭据。

## 页面与示例

核心提供英语卡片、折线图、表格、维度筛选及分组。默认每 15 秒刷新，历史范围为 1h/24h/7d/15d；过期与不可用显式显示，缺口断线。切页取消未完成请求并停止轮询。复杂页面仍可使用原有可信 JS 扩展。

`examples/data-provider` 是独立合成数据服务：配置文件包含 endpoint、catalog 和 values（数据集 ID 到数值）。使用真实的签名清单安装它；示例本身不管理签名私钥。只需更改 catalog/values 并重启示例服务，即可在同一个 Admin 构建中验证第二种业务数据。示例的历史为合成常量，不能用作生产采样器。

页面扩展上下文新增 `page`、`onDispose(callback)`；API helper 的第五参数支持 `{signal}`。已有参数与调用方式保持兼容。

可运行示例（目录须不存在；本地合成信任公钥不用于生产）：

```sh
go run ./examples/data-provider -init /tmp/data-plugin-demo
go run ./examples/data-provider -config /tmp/data-plugin-demo/provider.json
```

在另一个终端，用 `/tmp/data-plugin-demo/admin-plugins.json` 作为测试 Admin 的安装配置，保留其正常数据库和身份认证设置。示例初始化只把签名、公钥和配置写入该目录，签名私钥不落盘。已有安装应合并 provider 条目，不能覆盖其他插件。

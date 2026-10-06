# Admin API 私有配置模板

## 本组件专用安装脚本

```sh
curl -fL https://raw.githubusercontent.com/alpha2z/skygo-admin/main/install-admin-api.sh -o install-admin-api.sh
sh install-admin-api.sh
```

默认下载到新目录 `./admin-api-templates`。支持 `--output` 指定新目录、`--ref` 固定完整提交SHA，
以及 `--help`。无需下载其他组件脚本，也无需传入组件参数。脚本只下载本组件模板，
不覆盖配置、不生成接入身份、不启动容器；其余配置步骤见下文。


这些文件是**可公开下载的格式示例**，不含可投入使用的密码、令牌或签名身份。
适用于纯 Skygo Admin API；保留现有文件，先在新目录下载比对，不覆盖现网 private/。
本目录不是完整部署包，不会启动容器、建库、迁移数据库或修改现有配置。

## 使用安装脚本下载（无需登录）

在准备存放模板的目录执行：

```sh
curl -fL https://raw.githubusercontent.com/alpha2z/skygo-admin/main/install-templates.sh -o install-templates.sh
sh install-templates.sh --component admin-api --output ./admin-api-templates
```

只需要 curl 和 tar。脚本下载同一份源码快照中的模板，不启动服务、不生成凭据，
不创建实际 `.env`。目标目录已存在（含符号链接）时直接退出，绝不覆盖现网 private/。
下载失败或模板缺失时不创建目标目录。父目录须提前存在；最终复制若因磁盘等问题失败，
可能留下不完整的新目录，检查后改用新目录重试，不覆盖旧部署。

```sh
sh install-templates.sh --help
# Pin templates to a reviewed full commit SHA:
sh install-templates.sh --ref FULL_40_CHARACTER_COMMIT_SHA --output ./admin-api-templates-pinned
```

`--ref` 支持 main 或完整40位小写提交SHA。示例中的 FULL_40_CHARACTER_COMMIT_SHA
必须替换为实际SHA。main 模板会随源码更新，部署时核对镜像兼容性。
脚本名称采用常见的连字符命名，选项使用 --help/--output/--ref，不提供覆盖旧文件的选项。

### 手动下载备选

在新的临时目录执行：

```sh
curl -fL https://github.com/alpha2z/skygo-admin/archive/refs/heads/main.tar.gz -o source.tar.gz
tar -xzf source.tar.gz skygo-admin-main/deploy/admin-api-templates
```

模板位于 `skygo-admin-main/deploy/admin-api-templates/`，不要覆盖现有部署目录。

## 文件逐项说明

| 文件（去掉 .example 后使用） | 必需性、内容和示例 |
| --- | --- |
| management-dsn | 必需。单行 Go MySQL DSN，例如 `skygo_admin:REPLACE_WITH_DATABASE_PASSWORD@tcp(db.example.net:3306)/skygo_admin?charset=utf8mb4&parseTime=true&loc=UTC`。用户名、密码、主机、端口和库名按实际改。不是 `mysql://` URL，也不加 shell 引号。容器内 localhost 指容器自己。 |
| jwt | 必需。至少32字符的独立随机会话签名密钥，不是某个用户的 JWT。已有部署保留；更换会影响现有登录会话。 |
| bootstrap | 必需配置。至少32字符的另一份随机令牌，用于首次初始化管理员，不是管理员登录密码；保留现有值。已有管理员不应重新初始化。 |
| signing | 必需。Base64编码的64字节 Ed25519 私钥（32字节种子＋32字节公钥），用于签名 Agent 指令；不是 PEM，也不是随意生成64字节随机数。 |
| signing.pub | 配套。Base64编码的32字节 Ed25519 公钥，必须与 signing 成对；登记/配置 Agent 时使用。公钥可以分发，私钥只放 API。 |
| github.json | 可选。后台 Actions 构建集成配置，下面逐字段说明。拉取公开镜像不需要它。 |
| github-read-token | 可选。单行 GitHub 访问令牌；只有启用 Actions 集成才需要，不是 Docker 拉取密码。读取运行/产物需要相应读取权限；触发构建需要相应写权限。不要把令牌放在 JSON 或 URL 中。 |
| build-public | 可选的运维存档名，API不会因文件存在自动加载。内容是可信构建发布者的32字节 Ed25519 公钥的 Base64。通过发布设置登记可信公钥；不要拿 Agent 公钥代替，也不能随机生成公钥去验证已有构建。 |
| smtp-password | 邮件账户密码或应用密码，只有配置 ADMIN_SMTP_PASSWORD_FILE 时读取。文件不能为空；无认证邮件中继应不设置该变量，不能用空文件占位。 |

### github.json 参数

| 参数 | 示例与行为 |
| --- | --- |
| repository | `alpha2z/skygo-admin`，格式为 owner/repository；只能配置你允许的构建来源。 |
| workflow | `build.yml`，与现有后台的 service/platform/publish 构建输入匹配。`latest.yml` 是主线自动多架构流水线，不应直接替换为后台手动分组件构建入口。 |
| allowed_refs | `["main"]`；填写分支名，不写 refs/heads/ 前缀。 |
| token_file | `/run/private/github-read-token`，容器内绝对路径。 |
| publish_images | `false` 不请求发布；`true` 请求兼容工作流发布镜像，构建仓库仍需配置其签名及Registry权限。 |
| auto_register | `false` 默认手动登记；`true` 启用可信产物自动登记。仍需签名校验，不会自动批准部署。 |

**公开镜像拉取无需登录**。GitHub构建集成是另一项可选功能，当前客户端需要令牌；
只希望 `docker pull ...:latest` 更新时，保持 `ADMIN_GITHUB_CONFIG` 未设置即可。

### API 环境变量与挂载

`admin.env.example` 使用容器路径 `/run/private/...`。Compose 应将主机对应目录
只读挂载到 `/run/private`，并将填好的环境文件通过 `env_file` 传入。
文件叫 `management-dsn` 或 `mysql-dsn` 都可以，实际文件名必须与环境变量一致。
数据库 schema 必须已由对应 API 版本显式迁移；这些模板不会替你迁移或初始化数据库。

线上保留 `ADMIN_COOKIE_SECURE=true`、`ADMIN_TOTP_ENABLED=true` 和邮件确认，浏览器通过
HTTPS反向代理访问。`ADMIN_SMTP_ADDRESS` 为 `主机:端口`，例如 `smtp.example.net:587`，
用户名/发件人是对应账户地址。确认服务器支持该服务使用的 SMTP/STARTTLS方式。
仅在配置实际代理时设置 `ADMIN_TRUSTED_PROXY_CIDRS`，不要信任所有地址。

## 生成全新密钥（仅新部署）

现有私有文件能验证有效时直接保留，不要因为迁移容器就轮换。生成器需要 Python3 和支持
Ed25519 的 OpenSSL；仅创建一个不存在的新目录，拒绝覆盖任何已有目录：

```sh
python3 generate-keys.py --out private-new
```

输出只有 jwt、bootstrap、signing、signing.pub，不生成数据库密码、SMTP密码、GitHub令牌，
也不生成构建发布者公钥。数据库连接及第三方凭据由实际系统提供。
若生成失败，新目录可能已创建；先检查，不要改成覆盖原 private/ 重试。

## 文件权限与检查

私有目录使用0700，私有文件0600；文件所有者必须匹配 API 容器运行用户。
公开 API 镜像默认 UID 为10001；GID及 Compose 显式设置的 user 以实际运行身份为准。
`ls` 显示的系统用户名不是跨服务器通用身份，用 `ls -ln private/` 核对数字所有者。
不要通过放开成0644解决读取失败。

```sh
ls -ln private/
find private -maxdepth 1 -type f -size 0 -print
```

零字节必需文件不能用；可选功能未配置时不要设置其 `_FILE` 变量。
文件内容是单行原始值，不写 `变量名=`，不加引号，不粘贴示例占位文本运行。
业务扩展专用文件由对应私有扩展提供，本模板不包含它们。

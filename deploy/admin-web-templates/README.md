# Admin Web 独立模板

这是纯公开管理页面，不需要数据库密码、JWT、Agent令牌或签名私钥。
下载脚本不创建实际 `.env`，不启动容器，不覆盖已有目录。

```sh
sh install-templates.sh --component admin-web --output ./admin-web
cd admin-web
cp -n .env.example .env
# Edit .env before starting.
```

| 参数 | 示例与说明 |
| --- | --- |
| COMPOSE_PROJECT_NAME | `skygo-admin-web`；仅管理本目录的Web服务。 |
| ADMIN_WEB_IMAGE | `ghcr.io/alpha2z/skygo-admin/admin-web:latest`；可改成明确版本以回退。 |
| ADMIN_WEB_BIND | 默认 `127.0.0.1`，由同机HTTPS反向代理访问。容器代理不能用这个回环地址，应加入共享网络访问Web容器8080。 |
| ADMIN_WEB_PORT | 默认 `18390`，映射容器8080。 |
| ADMIN_NETWORK | `skygo-admin`，已存在的Docker外部网络；同机API应接入相同网络。 |
| ADMIN_API_ORIGIN | `http://admin-api:18391`，API在该网络的真实DNS名。也可填写跨机 `https://api.example.net`；只写scheme、host和可选端口，不含路径、凭据、查询参数。 |

确认网络是否存在：`docker network inspect skygo-admin`。首次部署且不存在时由操作员执行
`docker network create skygo-admin`，将API接入同一网络。不要创建一个API未加入的同名隔离网络
然后误认为它可以跨物理服务器互通。

Nginx 模板通过镜像原生启动机制替换 ADMIN_API_ORIGIN，保留 `$host` 等Nginx变量。
Docker DNS 每5秒重新解析上游；API重建无需联动重启Web。HTTPS上游启用证书校验，
不要用关闭校验来适配自签名证书，应配置可信CA。

```sh
# First download and start, anonymously:
docker compose pull
./start.sh
# Later, update only this Web:
./update.sh
# Status and health:
docker compose ps
curl -fsS http://127.0.0.1:18390/healthz
```

`start.sh` 不拉镜像，只启动本机已有镜像；`update.sh` 先拉取成功再重建本服务。
API尚未就绪时Web可启动但healthz不健康，不会自动启动API。公网入口应配置HTTPS；
保留API的Secure Cookie设置。API的鉴权、验证码、审批等逻辑不在Web模板中更改。

目录内容：compose.yaml、.env.example、nginx.conf.template、start.sh、update.sh、本文档。

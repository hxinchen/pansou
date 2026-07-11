# Yanhuo deployment guide

本文档记录当前炎火云部署方式，以及日常更新线上服务的一行命令。

## 当前线上地址

- 前端 UI: `http://103.236.97.248:22348/pansou/`
- 实时监控: `http://103.236.97.248:22348/pansou/report.html`
- API 健康检查: `http://103.236.97.248:22348/api/health`

说明：炎火云当前通过 `22348` 端口同时转发 SSH 和 HTTP，因此浏览器访问也使用 `:22348`。

## 当前部署结构

当前没有直接使用 `ghcr.io/fish2018/pansou-web` 镜像，因为服务器访问 GHCR 超时。实际部署方式等价于前后端集成版：

- 后端：本地交叉编译 Go 二进制，上传到服务器后构建本地 Docker 镜像 `local/pansou-api:latest`。
- 后端容器：`pansou-api`，监听容器内 `8888`，映射到服务器本机 `127.0.0.1:8889`。
- PostgreSQL：`pansou-postgres`，数据保存在 `pansou-postgres` 命名卷；凭据文件为 `/opt/pansou-web/database-secrets.env`（权限 `600`）。
- 备份：`/opt/pansou-web/scripts/backup-postgres.sh` 每日 `03:17` 执行，备份写入 `/opt/pansou-web/backups/` 并保留最近 7 份。
- 代理：服务器上运行 `mihomo`，只监听 Docker 内网 `192.168.0.1:7890`，后端容器通过 `PROXY=socks5h://192.168.0.1:7890` 访问 Telegram 等站点。
- 已启用插件：`labi,zhizhen,shandian,duoduo,muou,qqpd,gying,weibo`。
- 前端：本地构建 `pansou-web`，以 `/pansou/` 为 base path，上传静态文件到服务器。`/pansou/report.html` 是静态监控页，会轮询 `/api/health`。
- Caddy：托管 `/pansou/` 前端页面，并将 `/api/*` 反代到 `127.0.0.1:8889`。

服务器关键路径：

- 部署根目录：`/opt/pansou-web`
- 前端静态文件：`/opt/pansou-web/frontend`
- 后端构建目录：`/opt/pansou-web/build`
- 后端缓存目录：`/opt/pansou-web/cache`
- 插件密钥文件：`/opt/pansou-web/plugin-secrets.env`
- 数据库与管理员密钥：`/opt/pansou-web/database-secrets.env`
- Mihomo 配置：`/etc/mihomo/config.yaml`
- Caddy 配置：`/etc/caddy/Caddyfile`

## 本机前置条件

在 Windows PowerShell 中执行部署脚本。默认配置如下：

- 当前后端仓库：`D:\project\GitHub\pansou`
- 前端仓库：`D:\project\GitHub\pansou-web`
- SSH 私钥：`%USERPROFILE%\.ssh\yanhuo`
- SSH 登录：`root@103.236.97.248 -p 22348`

需要本机已安装：

- Go
- Node.js / npm
- OpenSSH `ssh` 和 `scp`

## 一行更新命令

进入后端仓库目录：

```powershell
cd D:\project\GitHub\pansou
```

只更新后端：

```powershell
.\scripts\deploy\update-backend.ps1
```

只更新前端：

```powershell
.\scripts\deploy\update-frontend.ps1
```

前后端都更新：

```powershell
.\scripts\deploy\update-all.ps1
```

如果 PowerShell 提示禁止运行脚本，使用：

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\deploy\update-all.ps1
```

## 脚本说明

`scripts/deploy/update-backend.ps1` 会执行：

1. 使用 Go 1.24.9 工具链交叉编译 Linux amd64 后端二进制。
2. 上传二进制到 `/opt/pansou-web/build/`。
3. 在服务器上构建 `local/pansou-api:latest` 镜像。
4. 重建并启动 `pansou-api` 容器。
5. 默认读取 `docker-compose.yml` 里的完整 `CHANNELS`，并保留 `PROXY=socks5h://192.168.0.1:7890`。
6. 检查本机和公网 `/api/health`。

`scripts/deploy/update-frontend.ps1` 会执行：

1. 在 `..\pansou-web` 中运行 `npm run build -- --base=/pansou/`。
2. 打包 `dist` 并上传到服务器。
3. 解压到 `/opt/pansou-web/frontend`。
4. reload Caddy。
5. 检查前端首页、前端资源和公网访问。
6. 检查 `/pansou/report.html` 监控页。

`scripts/deploy/update-all.ps1` 会先更新后端，再更新前端。

## 常用参数

指定前端仓库位置：

```powershell
.\scripts\deploy\update-frontend.ps1 -FrontendRoot "D:\project\GitHub\pansou-web"
```

更新时修改启用插件列表：

```powershell
.\scripts\deploy\update-backend.ps1 -EnabledPlugins "labi,zhizhen,shandian,duoduo,muou,qqpd,gying,weibo"
```

`qqpd`、`gying`、`weibo` 启用后还需要进入各自管理页登录或配置账号，才会在搜索中产出结果：

- `http://103.236.97.248:22348/qqpd/你的QQ号`
- `http://103.236.97.248:22348/gying/你的用户名`
- `http://103.236.97.248:22348/weibo/你的微博用户名`

更新时修改 TG 频道列表：

```powershell
.\scripts\deploy\update-backend.ps1 -Channels "yunpanx,Quark_Movies,Aliyun_4K_Movies"
```

指定后端代理地址：

```powershell
.\scripts\deploy\update-backend.ps1 -ProxyUrl "socks5h://192.168.0.1:7890"
```

跳过公网检查：

```powershell
.\scripts\deploy\update-all.ps1 -SkipNpmInstall
```

## 线上排查命令

查看后端容器：

```bash
docker ps --filter name=pansou-api
```

查看后端日志：

```bash
docker logs -f pansou-api
```

查看代理状态：

```bash
systemctl status mihomo --no-pager
journalctl -u mihomo -f
```

重启后端：

```bash
docker restart pansou-api
```

检查 Caddy：

```bash
systemctl status caddy --no-pager
caddy validate --config /etc/caddy/Caddyfile
```

检查服务器本机接口：

```bash
curl http://127.0.0.1:8889/api/health
curl http://127.0.0.1/pansou/
```

检查公网接口：

```bash
curl http://103.236.97.248:22348/api/health
```

## 本地忽略文件

部署脚本可能短暂生成以下本地文件，它们已加入 `.gitignore`：

- `pansou-linux-amd64`
- `pansou-web-dist.tar.gz`

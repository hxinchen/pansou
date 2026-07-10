# PanSou 多用户与 API 管理实施计划

## 实施原则

- 保留现有资源库 V1 的未提交改动，不回退、不覆盖无关代码。
- 后端先完成数据层和纯逻辑测试，再接入路由与 UI。
- `/api/search` 的参数和成功响应保持兼容。
- 多用户功能仅在 PostgreSQL 可用时启用；传统无数据库模式保持现有行为。
- 每个阶段运行局部测试，最终运行全量 Go 测试和前端构建/端到端检查。

## 任务 1：用户、API Key 与调用日志数据层

目标文件：

- `storage/migrations/*.sql` 或现有内置 migration 文件
- `storage/users.go`
- `storage/usage.go`
- `storage/users_test.go`
- `integration/users_postgres_test.go`

步骤：

1. 新增 `users`、`api_keys`、`api_request_logs` 表及索引。
2. 定义用户、API Key、调用日志、统计筛选和统计结果类型。
3. 实现用户创建、查询、分页、更新、启停、软删除、首次改密、密码版本递增。
4. 实现 API Key 创建、哈希查询、重置、吊销和最后使用时间更新。
5. 实现调用日志批量写入、用户隔离列表、管理员列表、趋势与概览查询。
6. 实现 30 天前日志的分批清理。
7. 添加唯一约束、事务与并发测试。

验证：

- `go test ./storage ./integration`

## 任务 2：认证领域服务与 JWT 角色

目标文件：

- `auth/service.go`
- `auth/password.go`
- `auth/api_key.go`
- `auth/service_test.go`
- `util/jwt.go`
- `util/jwt_test.go`
- `config/config.go`

步骤：

1. 增加用户仓储接口，使认证逻辑可单元测试。
2. 使用 bcrypt 保存和验证密码。
3. 使用安全随机数生成带固定前缀的 API Key，数据库仅保存 SHA-256 哈希。
4. JWT 增加用户 ID、角色和 `auth_version`，保留用户名字段兼容现有调用。
5. 实现登录、当前用户验证、首次改密、普通改密、管理员重置密码和初始管理员引导。
6. 定义稳定错误码：凭证错误、停用、到期、首次改密、角色不足和数据库不可用。
7. 保留无数据库传统认证分支，避免破坏现有部署。

验证：

- `go test ./auth ./util ./config`

## 任务 3：共享限流与调用记录

目标文件：

- `usage/limiter.go`
- `usage/limiter_test.go`
- `usage/recorder.go`
- `usage/recorder_test.go`

步骤：

1. 实现以用户 ID 为键的秒级和分钟级双窗口限流器。
2. 支持默认 `3 RPS / 60 RPM`、用户覆盖、不限流和配置热更新。
3. 返回剩余量、重置时间和重试时间。
4. 实现有界缓冲和批量异步写入器，支持优雅关闭和丢弃计数。
5. 实现 30 天日志清理定时任务。

验证：

- `go test ./usage -count=20`

## 任务 4：中间件、搜索计量和用户 API

目标文件：

- `api/middleware.go`
- `api/auth_handler.go`
- `api/user_handler.go`
- `api/handler.go`
- `api/router.go`
- `api/router_test.go`
- `main.go`

步骤：

1. 扩展路由依赖，注入认证服务、限流器和日志记录器。
2. 将公开路径精确匹配，避免 `/admin` 前缀误放行管理 API。
3. 实现 JWT 与 `X-API-Key` 身份解析，并把规范化用户放入 Gin 上下文。
4. `RequireAdminAuth` 改为真正验证 `admin` 角色。
5. 重写登录和校验接口，增加首次改密、修改密码和当前用户接口。
6. 新增用户 API Key、个人概览、趋势和调用明细接口。
7. 搜索前执行首次改密检查、`refresh=true` 权限检查和共享限流。
8. 搜索结束或被拒绝后写入调用事件，增加标准限流响应头。
9. 数据库用户模式故障时受保护接口返回 `503`；传统模式保持原行为。

验证：

- `go test ./api`
- 覆盖 `401/403/429/503`、JWT、API Key、共享限流和搜索兼容。

## 任务 5：管理员用户与调用监控 API

目标文件：

- `api/admin_handler.go`
- `api/admin_users.go`
- `api/admin_usage.go`
- 对应测试文件

步骤：

1. 增加用户分页、创建、编辑、启停、软删除接口。
2. 增加密码重置、API Key 重置和吊销接口。
3. 防止删除或停用最后一个有效管理员。
4. 增加全局调用概览、趋势和明细查询。
5. 统一管理员 API 错误响应和数据库可用性检查。

验证：

- `go test ./api -run 'Admin|User|Usage'`

## 任务 6：管理员后台响应式界面

目标文件：

- `web/index.html`
- `web/assets/app.js`
- `web/assets/styles.css`

步骤：

1. 增加“用户管理”和“API 监控”导航入口。
2. 实现用户列表、筛选、创建、编辑、启停、到期和限额设置。
3. 实现一次性凭证展示、复制和关闭前确认。
4. 实现全局指标、ECharts 趋势、用户排行和调用日志筛选。
5. 将现有 JWT 登录接入角色响应，普通用户登录管理端时显示无权限。
6. 桌面端保持侧边栏，移动端改为抽屉导航；表格在窄屏改为卡片或受控横向滚动。

验证：

- `node --check web/assets/app.js`
- `go test ./web`
- Playwright 桌面和 390px 移动视口检查。

## 任务 7：用户门户登录、API 面板和响应式布局

目标仓库：`D:\project\GitHub\pansou-web`

目标区域：

- 现有登录对话框和认证状态管理
- Axios 请求拦截器与 401 处理
- 主应用导航与搜索页面
- 新增 API 调用面板、账号设置和首次改密页面

步骤：

1. 将未登录状态改为独立登录页，不渲染搜索内容。
2. 登录后加载当前用户和角色，首次改密时进入阻断式页面。
3. 保留搜索为默认首页，增加“API 调用”和“账号设置”入口。
4. 实现个人指标、趋势图、状态分布和最近调用日志。
5. 实现 API Key 前缀、调用示例、重置与一次性明文展示。
6. 实现修改密码、到期信息和退出。
7. 网页搜索继续使用 JWT，并展示 `429` 的重试提示。
8. 桌面端使用顶部导航；手机端使用紧凑顶部栏和底部导航，搜索筛选与结果单列适配。

验证：

- 前端 lint/typecheck/build
- Playwright 覆盖登录、首次改密、搜索、API 面板、Key 重置、退出和移动端布局。

## 任务 8：配置、部署与运维

目标文件：

- `config/config.go`
- `.env.example` 或现有配置示例
- `docker-compose.yml`
- `README.md`
- 部署脚本与运维文档

步骤：

1. 增加初始管理员、默认 RPS/RPM、JWT 和日志保留配置。
2. 明确 `DATABASE_URL` 为多用户模式前提。
3. 更新 Caddy/部署说明，使 `/pansou/` 为用户门户，`/admin/` 为管理员后台。
4. 增加日志清理、数据库健康和账号引导检查。
5. 确保密钥文件权限为 `600`，不把凭证写入仓库。

## 任务 9：全量回归与交付

1. 运行 `gofmt`、`go test ./...` 和相关 `go vet`。
2. 运行管理员前端语法、嵌入资源和 Playwright 检查。
3. 运行用户前端构建和响应式 Playwright 检查。
4. 验证传统无数据库模式未回归。
5. 验证数据库模式的登录、角色、Key、共享限流、日志隔离和管理员监控。
6. 记录本地无法执行的 PostgreSQL、浏览器或部署验证，并提供明确复现命令。

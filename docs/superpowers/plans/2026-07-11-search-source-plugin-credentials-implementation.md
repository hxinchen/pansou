# PanSou 搜索来源与多租户插件凭证实施计划

## 实施原则

- 以 `docs/superpowers/specs/2026-07-11-search-source-plugin-credentials-design.md` 为唯一产品规格。
- 保留当前工作树中的资源库、多用户、调用监控和两套前端改动，不回退、不覆盖无关内容。
- 先建立数据与运行时边界，再改四个账号插件，最后接 API 和 UI，避免页面依赖临时接口。
- 旧 `/api/search` 签名保留兼容包装；新增上下文搜索路径传递用户身份，不把凭证放入通用 `ext`。
- 自动测试不访问真实第三方站点，四个真实登录流程只在本地启动后做手工烟雾验证。
- 每个阶段先运行局部测试，最终执行 Go、PostgreSQL、两套前端和桌面/移动端端到端验证。

## 任务 1：基线与领域契约

目标文件：

- `credential/types.go`
- `credential/errors.go`
- `sourceconfig/types.go`
- `sourceconfig/catalog.go`
- 对应测试文件

步骤：

1. 运行当前 Go 全量测试及两套前端构建，记录基线失败，避免把既有问题误判为本次回归。
2. 定义凭证 scope、健康状态、稳定错误码、搜索身份、候选描述、秘密 lease、登录 flow 和适配器接口。
3. 定义来源配置文档、内置来源描述、插件公开配置 schema、快照健康和版本冲突错误。
4. 在目录中标记 `qqpd`、`gying`、`panlian`、`weibo` 为账号型插件，并声明配置绑定字段。
5. 添加规范化、状态判断和目录验证的纯单元测试。

验证：

- `go test ./credential ./sourceconfig`

## 任务 2：PostgreSQL 来源配置与凭证存储

目标文件：

- `storage/migrations/004_search_source_plugin_credentials.sql`
- `storage/source_config_types.go`
- `storage/source_configs.go`
- `storage/plugin_credential_types.go`
- `storage/plugin_credentials.go`
- `storage/data_migrations.go`
- `integration/credential_repository.go`
- `integration/source_config_repository.go`
- `integration/search_source_postgres_test.go`

步骤：

1. 新增 `search_source_configs` 单例配置、`search_source_config_events`、`plugin_credentials` 和 `data_migrations`。
2. 添加 scope/owner、状态、管理员暂停、过期、配置绑定、唯一指纹和外键约束及查询索引。
3. 实现配置读取、首次插入、基于 `expected_version` 的 compare-and-swap、脱敏事件写入和分页查询。
4. 实现凭证创建、按公开 ID 和所有者读取、管理员/用户分页、用户启停、管理员暂停、删除和健康回写。
5. 管理员私有与公开共享切换使用事务回调，以便上层解密后按新 AAD 重新加密。
6. 实现候选描述查询，只返回当前身份可能使用的密文记录，不在 SQL 层解密。
7. 实现数据迁移完成标记和脱敏数量摘要。
8. 当前用户删除是软删除，因此在现有用户软删事务中显式删除该用户的 `user_private` 凭证；不能依赖 FK cascade。
9. 增加约束、并发配置更新、用户级隔离、软删清理和候选筛选集成测试。

验证：

- `go test ./storage ./integration -run 'Source|Credential|Migration'`

## 任务 3：AES-GCM、指纹、解析器与登录 flow

目标文件：

- `credential/cipher.go`
- `credential/fingerprint.go`
- `credential/resolver.go`
- `credential/health.go`
- `credential/flows.go`
- `credential/*_test.go`
- `config/config.go`

步骤：

1. 解析 Base64 32 字节 `PLUGIN_CREDENTIAL_MASTER_KEY`，实现 AES-256-GCM 随机 nonce 与 AAD 行绑定。
2. 通过派生 HMAC-SHA-256 子密钥计算稳定账号指纹，避免 Cookie/Token 刷新改变指纹。
3. 实现只在实际候选调用前解密的 lease，关闭后清理字节缓冲和引用。
4. 实现普通用户、管理员、自动采集的候选层，过滤账号状态、用户状态、过期、冷却和管理员暂停。
5. 实现同层健康排序、轮换、成功/认证失败/过期/限流/临时故障回写。
6. 实现用户和 flow 绑定的内存登录状态、TTL、并发安全清理、每插件登录限流和二维码轮询限流。
7. 增加密文随机性、防篡改、错误密钥、AAD 交换、选择优先级、零结果成功和回退测试。

验证：

- `go test ./credential -count=20`
- `go test -race ./credential`

## 任务 4：插件工厂与原子运行时来源快照

目标文件：

- `plugin/plugin.go`
- 所有内置插件的 `init()` 注册行
- `sourceconfig/snapshot.go`
- `sourceconfig/manager.go`
- `sourceconfig/bootstrap.go`
- `sourceconfig/*_test.go`
- `service/search_service.go`

步骤：

1. 将全局插件注册表从单例实例升级为工厂目录；保留旧注册函数仅作测试兼容。
2. 机械转换所有内置插件的注册行，使每次候选快照获得独立插件实例。
3. 为可初始化、可关闭插件定义生命周期适配，候选失败时关闭已创建实例。
4. 实现不可变快照，包含版本、TG 渠道、插件总开关、插件管理器、账号适配器和健康信息。
5. 实现 `Acquire/Release` 引用计数、原子发布、retired 延迟关闭和串行更新锁。
6. 实现环境变量到版本 1 的首次配置引导；数据库已有配置后不再由环境变量覆盖。
7. `SearchService` 每次搜索获取一个快照，并用该快照的频道、总开关和插件管理器完成整次实时搜索。
8. 保留静态 `NewSearchService` 与现有 `GetPluginManager` 兼容行为，新增动态构造和健康读取接口。
9. 测试初始化失败回滚、并发获取、发布后新旧请求分流、关闭恰好一次和版本冲突。

验证：

- `gofmt` 涉及的插件注册文件
- `go test ./plugin ./sourceconfig ./service`
- `go test -race ./sourceconfig ./service`

## 任务 5：身份感知实时搜索、混合搜索和采集

目标文件：

- `service/search_context.go`
- `service/search_service.go`
- `service/hybrid_search.go`
- `api/handler.go`
- `integration/collection_adapter.go`
- `collection/interfaces.go`
- `collection/runner.go`
- 对应测试文件

步骤：

1. 新增带 `context.Context` 的搜索接口和类型化搜索身份；旧 `Search` 方法包装为兼容调用。
2. API 搜索把当前 principal 写入类型化上下文，不把用户或凭证塞入 `ext map`。
3. `HybridSearchService` 在同步实时搜索和后台刷新中保留身份值但脱离 HTTP 取消上下文。
4. 插件搜索遇到账号型插件时从解析器获取该身份、该插件的候选集；非账号插件保持旧调用路径。
5. 采集适配器标记 `collector` 身份，并把静态 `SourceProvider` 扩展为按关键词获取/释放来源租约，保证同一关键词始终持有同一个快照。
6. 数据库不可用时让调度器暂停并报告，不把“无来源”误记为关键词技术失败；恢复后继续轮询。
7. 保持每渠道最多重试 2 次、部分失败摘要、成功/空成功冷却和资源入库逻辑不变。
8. 增加用户身份后台刷新、采集管理员层、动态来源和数据库故障恢复测试。

验证：

- `go test ./service ./collection ./integration`
- `go test -race ./service ./collection`

## 任务 6：四个账号型插件多租户适配

目标文件：

- `plugin/qqpd/tenant.go` 及 `plugin/qqpd/qqpd.go`
- `plugin/gying/tenant.go` 及 `plugin/gying/gying.go`
- `plugin/panlian/tenant.go` 及 `plugin/panlian/panlian.go`
- `plugin/weibo/tenant.go` 及 `plugin/weibo/weibo.go`
- 各包测试文件

步骤：

1. 把“获取全局有效用户”与搜索核心拆开，使搜索核心显式接收当前候选层转换出的用户列表。
2. `gying`、`panlian` 实现顺序 failover；`qqpd`、`weibo` 在同层执行受控 fan-out。
3. 私有层至少一次成功或零结果成功时禁止使用共享层；整层失败时才请求下一层。
4. 实现插件专属秘密 payload、稳定身份、公开元数据、错误分类、登录验证和安全刷新。
5. 实现 QQ/微博二维码 flow 与观影/盘链账号密码登录，不复用匿名磁盘管理路由。
6. Base URL 等绑定配置变化时，禁止向新域名发送旧 Cookie；安全重新登录或标记需重新认证。
7. 数据库凭证模式下不加载、不保存 `*_users` 文件；无数据库兼容模式继续保留旧行为。
8. 把插件保活和清理 goroutine 绑定快照上下文，实现关闭接口。
9. 使用模拟上游测试多用户并发隔离、登录成功/失败/过期、刷新、限流、fan-out 部分成功和共享回退。

验证：

- `go test ./plugin/qqpd ./plugin/gying ./plugin/panlian ./plugin/weibo`
- `go test -race ./plugin/qqpd ./plugin/gying ./plugin/panlian ./plugin/weibo`

## 任务 7：旧账号事务迁移

目标文件：

- `credential/migrate.go`
- 四个账号插件的迁移解析器
- `credential/testdata/legacy/*`
- `integration/credential_migration_test.go`
- `main.go`

步骤：

1. 为四种旧 JSON 格式实现只读解析和结构验证，不联网登录。
2. 把密码、Cookie、Token 和必要秘密转换为新版 payload，所有账号 scope 固定为 `admin_private`。
3. 以稳定指纹幂等去重，并在一个事务中写入全部账号和迁移完成标记。
4. 格式损坏、加密或数据库错误时回滚并中止启动，日志只显示脱敏文件名。
5. 成功后不修改、移动或删除旧目录；数据库模式下插件停止读取旧文件。
6. 增加四种 fixture、重复启动、半途失败回滚、空辅助文件和迁移数量测试。

验证：

- `go test ./credential ./integration -run 'Legacy|CredentialMigration'`

## 任务 8：管理员与用户 API、鉴权和健康

目标文件：

- `api/source_handler.go`
- `api/admin_credentials.go`
- `api/user_credentials.go`
- `api/credential_flows.go`
- `api/router.go`
- `api/middleware.go`
- `api/*_test.go`
- `main.go`

步骤：

1. 扩展路由依赖，注入来源管理器、凭证服务和登录 flow 服务。
2. 实现管理员来源目录、当前配置、校验、原子更新和事件分页接口。
3. 实现管理员私有/共享账号 CRUD、scope 重加密切换和用户账号监管接口。
4. 实现用户已启用插件、个人账号 CRUD、重新登录和共享账号聚合状态接口。
5. 注册统一二维码/密码登录 flow；旧插件路由在数据库模式下不再提供匿名管理。
6. 强制网页 JWT；管理接口不解析 API Key，跨用户资源统一返回 `404`。
7. 实现 `400/401/403/404/409/422/429/503` 与规格中的稳定错误码。
8. 扩展健康信息，普通接口只返回概要，管理员接口返回配置版本、密钥、快照和插件状态。
9. 测试权限隔离、密文字段缺失、共享摘要脱敏、管理员暂停不可由用户解除、版本冲突和故障降级。

验证：

- `go test ./api`
- `go test ./api -run 'Source|Credential|Flow|Isolation'`

## 任务 9：管理员后台响应式界面

目标文件：

- `web/index.html`
- `web/assets/app.js`
- `web/assets/styles.css`
- 管理端 Playwright 测试或验证脚本

步骤：

1. 在现有侧边栏增加“搜索来源”，并为账号管理增加“管理员账号”和“用户账号监管”。
2. 实现配置版本、快照健康、TG 渠道增删改排、内置插件启停和公开参数表单。
3. 保存失败保留编辑值并展示旧配置仍在运行；`409` 提供重新加载。
4. 实现管理员二维码/密码登录、私有/共享选择、重新认证、启停、scope 切换和删除。
5. 实现用户账号筛选与监管，只展示允许字段，只允许暂停、恢复和删除。
6. 复用现有 PanSou 样式、ECharts 与 Lucide；桌面使用表格/抽屉，390px 使用卡片/底部操作。
7. 增加 API 错误、加载、空状态和键盘/触控可达性处理。

验证：

- `node --check web/assets/app.js`
- `go test ./web`
- Playwright 桌面与 390px 覆盖来源保存、版本冲突、账号管理和用户监管。

## 任务 10：用户门户搜索账号界面

目标仓库：`D:\project\GitHub\pansou-web`

目标文件：

- `src/api/index.ts`
- `src/api/pluginCredentials.ts`
- `src/components/AccountCenter.vue`
- `src/components/PluginCredentialCard.vue`
- 现有四个插件 manager 与类型文件
- `src/App.vue`
- 响应式样式和 Playwright 测试

步骤：

1. 在已登录导航中增加“搜索账号”，保持现有搜索、API 调用和账号设置入口。
2. 复用现有 QQPD/Gying/Panlian/Weibo 管理交互，但把 API 改为当前用户隔离的统一凭证接口。
3. 展示个人多账号、状态、到期、最近成功和脱敏提示；共享层只展示数量和聚合健康。
4. 实现二维码登录、密码登录、重新认证、用户启停和删除；展示管理员暂停但不提供解除按钮。
5. 插件全局停用时保留账号说明，不允许新登录或搜索使用。
6. 桌面使用列表/详情，移动端使用单列卡片和适配二维码的操作面板。
7. 统一处理 `401/403/404/429/503`，不得把登录秘密写入前端日志或持久化存储。

验证：

- `npm run build`
- 项目已有 lint/typecheck 命令（如存在）
- Playwright 覆盖两个用户隔离、登录、重新认证、管理员暂停和移动端布局。

## 任务 11：配置、部署与文档

目标文件：

- `config/config.go`
- `docker-compose.yml`
- `.gitignore`
- `README.md`
- `docs/yanhuo-deployment.md`
- `scripts/*`

步骤：

1. 增加主密钥读取和校验，数据库凭证启用条件下缺失密钥拒绝启动。
2. 更新 PostgreSQL migration、Docker secret/环境注入和生产 HTTPS 要求。
3. 明确密钥文件 `600` 权限、数据库和密钥共同备份、旧目录手工清理和短期回滚限制。
4. 增加来源配置、账号迁移、插件健康和凭证无明文的上线检查命令。
5. 为本地开发生成不入库的随机密钥，并保持旧无数据库启动兼容。

## 任务 12：全量回归、本地启动与交付

1. 对全部 Go 改动运行 `gofmt`。
2. 运行 `go test ./...` 和 `go vet ./...`。
3. 对 `credential`、`sourceconfig`、`service`、`collection` 和四个账号插件运行 race 测试。
4. 使用本地 PostgreSQL 实际执行 migration、配置 compare-and-swap、密文检查和旧账号迁移集成测试。
5. 构建管理端嵌入资源和用户门户生产包。
6. 运行管理员端和用户端桌面/移动 Playwright 流程。
7. 验证未登录 `401`、普通用户管理接口 `403`、版本冲突 `409`、登录限流 `429`、数据库故障 `503`。
8. 验证 `/api/search` 兼容、网页/API Key 共享 `3 RPS / 60 RPM`、调用日志用户隔离和资源入库。
9. 本地启动新版后端与用户门户，报告访问地址、管理员账号、测试用户和仍需真实第三方账号完成的烟雾步骤。

## 启动顺序与高风险约束

数据库模式严格按以下顺序启动：打开 PostgreSQL 并迁移、构建来源目录、仅在配置表为空时从环境变量写入版本 1、
解析主密钥、事务迁移旧账号、校验所有密文、从数据库构建初始快照、创建实时/混合搜索和采集、注入 API，最后启动
HTTP。关闭时反向执行：先停止 HTTP 和采集，再等待快照租约归零，最后关闭 PostgreSQL。

实施中必须特别防止以下回归：

- 不把新表折回尚未提交的 `003` migration，固定使用 `004`。
- 不直接大段重写当前已包含多用户改动的 `main.go`；优先提取独立 bootstrap 文件并做薄注入。
- 不继续读取运行时全局 `config.AppConfig.AsyncPluginEnabled/EnabledPlugins/DefaultChannels` 作为数据库模式事实来源。
- 不让 `GetPlugins` 暴露可变底层切片，也不让插件创建无法取消的永久 goroutine。
- 不使用会吞掉 JSON 序列化错误的通用 metadata helper 保存来源配置或凭证元数据。

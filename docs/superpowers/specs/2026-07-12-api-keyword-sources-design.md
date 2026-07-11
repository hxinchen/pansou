# API 关键词来源设计

## 目标

管理员可在关键词页面配置外部 HTTP API，现场测试响应并从 JSON 中选择字段路径。系统按独立周期拉取 API，将提取值规范化、去重并同步为普通关键词。生成的关键词继续使用现有冷却、调度、任务和资源关联逻辑。

## 已确认规则

- API 来源独立于普通关键词保存。
- 默认每 60 分钟同步，允许按分钟覆盖并支持立即同步。
- 支持字符串、字符串数组和对象数组固定字段提取。
- 统一路径表示法，例如 `data.keyword`、`data.keywords[]`、`data.items[].name`、`data.items[].meta.keyword`。
- 每次拉取按现有 `NormalizeKeyword` 规则去空值、规范化并全局去重。
- API 内重复值只保留一次；数据库已有关键词不重复插入。
- 后续响应中消失的关键词保持原状态，不停用、不删除。
- 请求头、Cookie、Token 和代理凭据按管理员选择明文保存；普通列表、任务日志和错误日志不返回这些值，管理员详情接口允许读取编辑。
- 第一版不支持 DELETE 请求，也不实现分页脚本或自定义 JavaScript 转换。

## 数据模型

新增 `keyword_api_sources`：

- `id`, `name`, `enabled`
- `request_method`, `request_url`
- `request_headers JSONB`, `query_params JSONB`
- `body_type`: `none/json/form/raw`
- `request_body JSONB/TEXT`
- `proxy_url`, `timeout_seconds`
- `response_path`
- `sync_interval_seconds`, `next_sync_at`, `last_synced_at`
- `last_status`, `last_error`, `last_item_count`
- `created_at`, `updated_at`

新增 `keyword_api_source_items`：

- `source_id`, `keyword_id`
- `external_value`, `normalized_value`
- `first_seen_at`, `last_seen_at`
- `(source_id, normalized_value)` 唯一

`keywords.normalized_keyword` 继续作为全局唯一标识。同步遇到已有关键词时只建立来源关联，不覆盖手动关键词的已有类型、优先级、冷却期或启停状态。新建关键词使用 API 来源配置中的默认关键词类型、优先级、冷却期和启用状态。

## HTTP 与 JSON 提取

- 方法：GET、POST、PUT、PATCH。
- Query 和 Header 使用可增删键值行。
- JSON Body 必须是合法 JSON；Form 使用键值行；Raw 使用文本。
- 代理支持 `http://`、`https://`、`socks5://`、`socks5h://`。
- URL 仅允许 HTTP/HTTPS；测试和同步默认超时 15 秒，可配置 1–60 秒。
- 响应体上限 2 MiB，只接受可解析 JSON。
- JSON 路径使用受限语法：对象字段、数组下标和 `[]` 通配，不执行脚本。
- 最终节点可以是字符串、数字、布尔值或数组；非字符串标量转为文本，对象节点必须继续选择子字段。

## 同步行为

同进程新增 API 来源同步器，每 60 秒领取到期且启用的来源，同一时间只同步一个来源。步骤：请求 API、解析路径、规范化去重、事务内 UPSERT 关键词与来源关系、更新同步状态和下次时间。失败不改变现有关键词，按同步周期计算下次时间并记录脱敏错误。

允许保存未启用的草稿；启用自动同步或执行立即同步前，配置必须通过现场测试并保存有效 `response_path`。立即同步接口复用同一同步服务。

## 管理 API

- `GET /api/admin/keyword-api-sources`
- `GET /api/admin/keyword-api-sources/:id`
- `POST /api/admin/keyword-api-sources`
- `PUT /api/admin/keyword-api-sources/:id`
- `DELETE /api/admin/keyword-api-sources/:id`：只删除来源及关系，不删除关键词
- `POST /api/admin/keyword-api-sources/test`
- `POST /api/admin/keyword-api-sources/:id/sync`

列表接口对 Header、Body、Query 和代理地址脱敏；详情接口管理员可读取完整配置。

## 前端

关键词页面增加“关键词列表 / API 来源”页签。新增关键词弹窗增加“手动关键词 / API 同步”切换：

- 手动模式保持现状。
- API 模式显示来源名称、请求构造器、代理、超时、同步周期和新关键词默认值。
- “测试请求”成功后展示状态、耗时、响应大小、格式化 JSON 树和字段候选。
- 点击树节点生成路径；数组对象字段显示 `items[].name`。
- 路径预览即时显示将提取的值、原始数量和去重后数量。
- API 来源页展示启停、周期、最近状态、提取数量、立即同步、编辑和删除。

桌面使用密集的双栏请求构造器与响应检查器；移动端改为纵向步骤，操作按钮保持可触达。

## 安全与日志

- 仅管理员可配置、测试和同步。
- 不记录请求头、请求体、完整代理地址或完整响应体。
- 错误信息对 Authorization、Cookie、Token、password 等键和值脱敏。
- 限制重定向次数、响应大小和超时，避免测试请求长期占用连接。

## 验证

- 单元测试：路径解析、对象数组字段、标量数组、去重、请求构造、代理解析、日志脱敏。
- PostgreSQL 集成测试：migration、CRUD、并发同步、全局关键词去重、来源关系和消失项保留。
- API 测试：鉴权、测试请求、非法方法/URL/JSON、同步成功失败、详情与列表脱敏差异。
- Playwright：创建来源、构造请求、测试 JSON、选择路径、保存、立即同步、查看生成关键词、移动端布局。

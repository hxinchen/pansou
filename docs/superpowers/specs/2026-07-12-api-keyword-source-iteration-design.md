# API 关键词来源 JSON 配置、迭代与复制设计

## 目标

扩展现有 API 关键词来源，使管理员可以用键值表或 JSON 配置 Query、Header 和 Body；一个来源可以按数值参数顺序迭代请求多页数据；来源配置可以复制，副本默认停用。所有迭代响应中的关键词合并、规范化并全局去重后再写入现有关键词库。

## 已确认规则

- Query、Header 同时支持键值编辑和 JSON 编辑，两种模式共享同一份对象数据。
- JSON Body 和 Form Body 支持 JSON 编辑；Raw Body 继续使用原始文本。
- 一个来源第一版只允许配置一个迭代参数。
- 迭代参数位置支持 Query、Header 和 Body。
- Body 为 JSON 时使用点路径定位嵌套字段，例如 `pagination.offset`；Body 为 Form 时路径是字段名；Raw Body 不支持迭代参数。
- 数值序列公式为 `start + step * index`，其中 `index` 从 0 开始。
- 起始值 0、步长 20、次数 10 时请求值为 `0,20,40,...,180`，共 10 次。
- 第一次请求立即执行；第二次及之后在请求前等待配置的迭代间隔。
- 所有成功迭代使用同一个响应字段路径提取关键词。
- 单轮失败继续执行后续迭代；至少一轮成功则保存成功轮次的数据并标记部分成功；全部失败才标记失败。
- 现场测试默认只执行第一轮，不执行完整迭代，避免长时间阻塞配置流程。
- 立即同步和定时同步执行完整迭代。
- 复制来源时复制全部请求、代理、提取、迭代和关键词默认配置，但新来源名称追加“副本”、默认停用，并清空运行状态和历史统计。

## 数据模型

新增 migration `006_keyword_api_source_iteration.sql`，为 `keyword_api_sources` 增加：

- `iteration_enabled BOOLEAN NOT NULL DEFAULT FALSE`
- `iteration_location TEXT NOT NULL DEFAULT 'query'`，允许 `query/header/body`
- `iteration_path TEXT NOT NULL DEFAULT ''`
- `iteration_start BIGINT NOT NULL DEFAULT 0`
- `iteration_step BIGINT NOT NULL DEFAULT 20`
- `iteration_count INTEGER NOT NULL DEFAULT 1`
- `iteration_delay_seconds INTEGER NOT NULL DEFAULT 0`
- `last_request_count INTEGER NOT NULL DEFAULT 0`
- `last_success_count INTEGER NOT NULL DEFAULT 0`
- `last_failure_count INTEGER NOT NULL DEFAULT 0`

`last_status` 增加 `partial`。约束：

- 启用迭代时路径不能为空。
- 次数范围 1–100，避免单个来源无限占用 Worker。
- 间隔范围 0–3600 秒。
- 步长允许正数、零和负数。
- Header 迭代值以十进制字符串写入。
- Body 迭代仅允许 JSON 或 Form；Raw/None 配置 Body 迭代时拒绝保存。

现有 `request_headers JSONB`、`query_params JSONB` 和 `request_body TEXT` 保持不变。JSON/键值切换属于管理端编辑方式，不新增重复存储字段。

## 请求与迭代执行

`keywordsource` 增加持久化无关的迭代配置与请求派生函数：

- 校验位置、路径、次数和间隔。
- 根据迭代索引克隆基础请求配置。
- Query/Header 直接写入目标键。
- JSON Body 解析对象并按点路径写入整数，路径中的中间对象必须存在；不隐式创建数组或执行脚本。
- Form Body 写入指定字段的十进制字符串。

`keywordsync.Service` 顺序执行迭代：

1. 领取来源并进入 `running`。
2. 为每个索引派生请求配置。
3. 第一轮立即执行，后续轮次等待 `iteration_delay_seconds`。
4. 每轮独立请求、解析 JSON、按 `response_path` 提取。
5. 收集成功轮次值并按规范化值内存去重。
6. 至少一轮成功时事务写入关键词和来源关系。
7. 全部成功记录 `success`；部分成功记录 `partial` 和脱敏错误摘要；全部失败记录 `failed`。

上下文取消时立即停止等待和后续请求。错误摘要只记录轮次编号、状态和脱敏错误，不记录 Header、Body、代理凭据或完整响应。

## 管理 API

现有 CRUD 和测试 DTO 增加：

- `iteration_enabled`
- `iteration_location`
- `iteration_path`
- `iteration_start`
- `iteration_step`
- `iteration_count`
- `iteration_delay_seconds`

列表和详情增加最近请求/成功/失败轮数。列表继续隐藏 Header、Query、Body 和代理配置。

新增：

- `POST /api/admin/keyword-api-sources/:id/copy`

复制接口返回新来源详情，名称使用 `<原名称> 副本`；若冲突不强制唯一，因为来源名称不是业务唯一键。副本 `enabled=false`、`next_sync_at=null`、`last_status=pending`。

测试接口只将起始值写入第一轮请求，并在响应中返回 `iteration_value`，帮助管理员确认参数注入正确。

## 管理端界面

请求构造器中 Query 和 Header 各增加“键值 / JSON”分段切换。JSON 编辑器要求根节点为对象，失焦或切换时校验；错误时保留原文本并阻止测试和保存。

Body 区域：

- JSON：JSON 文本编辑。
- Form：键值编辑和 JSON 对象编辑切换。
- Raw：纯文本。
- None：不显示编辑器。

新增独立“分页迭代”模块：

- 开关
- 参数位置
- 参数路径
- 起始值、步长、次数、请求间隔
- 实时序列预览，最多展示前 6 个值，例如 `0 → 20 → 40 → … → 180`
- 预计最短耗时，按 `(次数 - 1) × 间隔` 计算，不包含网络时间

API 来源列表操作增加“复制”。复制成功后刷新列表并提示副本已停用。桌面保持双栏请求构造器；移动端按“请求配置 → 迭代 → 响应检查器”纵向排列，底部保存按钮保持固定可触达。

## 兼容性

- 旧来源 migration 后 `iteration_enabled=false`，行为完全不变。
- 未启用迭代时仍只请求一次。
- `DATABASE_URL` 未配置时保持现有 PanSou 行为；管理接口仍返回 503。
- 现有 API 请求 JSON 字段保持兼容，新增字段均有默认值。

## 测试

- 单元测试：序列计算、负步长、Query/Header 注入、嵌套 JSON Body 注入、Form 注入、非法路径和次数限制。
- 同步测试：10 轮合并去重、部分失败、全部失败、取消间隔等待、单次模式兼容。
- PostgreSQL 集成测试：migration 默认值、partial 状态、统计字段、复制配置和副本默认停用。
- API 测试：CRUD 新字段、JSON 配置校验、测试首轮、复制、列表脱敏。
- 浏览器验证：键值/JSON 双向切换、序列预览、测试、保存、复制，以及桌面和 390×844 移动端布局。


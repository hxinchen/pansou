# API 关键词来源 JSON 配置、迭代与复制设计

## 目标

扩展现有 API 关键词来源，使管理员可以用键值表或 JSON 配置 Query、Header 和 Body；一个来源可以按数值参数有限或无限迭代请求，并可在连续多轮未提取到关键词后停止。后续轮次支持固定间隔叠加正负随机延迟。来源配置可以复制，副本默认停用。所有迭代响应中的关键词合并、规范化并全局去重后再写入现有关键词库。

## 已确认规则

- Query、Header 同时支持键值编辑和 JSON 编辑，两种模式共享同一份对象数据。
- JSON Body 和 Form Body 支持 JSON 编辑；Raw Body 继续使用原始文本。
- 一个来源只配置一个迭代参数，位置支持 Query、Header 和 Body。
- Body 为 JSON 时使用点路径定位嵌套字段，例如 `pagination.offset`；Body 为 Form 时路径是字段名；Raw Body 不支持迭代参数。
- 数值序列公式为 `start + step * index`，其中 `index` 从 0 开始；有限模式执行 `iteration_count` 轮，无限模式不受该次数限制。
- 有限模式可将连续无关键词阈值设为 0 以关闭提前停止；无限模式必须将阈值设为 1–100。
- 本轮提取到至少一个非空关键词即重置连续计数，即使该关键词与前序轮次或数据库已有值重复；无有效关键词或请求派生、HTTP、JSON、提取失败均使连续计数加一。
- 第一次请求立即执行；第二次及之后在请求前等待 `max(0, 固定间隔 + 本轮随机延迟)`，随机值在配置的整数闭区间内独立采样。
- 单轮失败继续执行后续迭代，除非已经达到连续无关键词阈值或上下文取消。纯空结果停止记为成功；成功轮和错误轮并存记为部分成功；只有失败轮才记为失败。
- 现场测试只执行第一轮，不等待也不采样随机延迟，但仍校验完整迭代配置。
- 立即同步和定时同步执行完整迭代。
- 复制来源时复制全部请求、代理、提取、迭代和关键词默认配置，但新来源名称追加“副本”、默认停用，并清空运行状态和历史统计。

## 数据模型

Migration `006_keyword_api_source_iteration.sql` 已为 `keyword_api_sources` 增加基础迭代与轮次统计字段：

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

Migration `007_keyword_api_source_unlimited_iteration.sql` 继续增加：

- `iteration_unlimited BOOLEAN NOT NULL DEFAULT FALSE`
- `iteration_no_keyword_stop_count INTEGER NOT NULL DEFAULT 0`
- `iteration_random_delay_min_seconds INTEGER NOT NULL DEFAULT 0`
- `iteration_random_delay_max_seconds INTEGER NOT NULL DEFAULT 0`

Migration `008_keyword_api_sync_history.sql` 增加持久化执行队列与历史：

- 来源增加 `sync_config_revision`、`last_applied_config_revision` 和 `result_stale`，修改请求或提取配置后可明确标记旧结果已经过期。
- `keyword_api_sync_runs` 保存一次手动、保存后、定时或升级回填运行的状态、进度、租约、统计和脱敏配置快照。
- `keyword_api_sync_iterations` 保存每轮参数值、HTTP 状态、耗时、响应大小、提取统计、有限样例和脱敏错误。
- 同一来源最多各有一个 `queued` 和一个 `running` 运行；旧同步摘要迁移成 `legacy` 历史记录，遗留 `running` 状态恢复为失败。

`last_status` 支持 `partial`。迭代配置约束如下：

- 启用迭代时路径不能为空。
- `iteration_count` 始终保持 1–100；无限模式忽略该字段但不放宽其存储约束。
- 固定间隔范围为 0–3600 秒。
- 连续无关键词阈值范围为 0–100；无限模式启用时必须为 1–100。
- 随机延迟上下界各为 -3600–3600 秒，且最小值不得大于最大值。
- 步长允许正数、零和负数。
- Header 迭代值以十进制字符串写入。
- Body 迭代仅允许 JSON 或 Form；Raw/None 配置 Body 迭代时拒绝保存。

现有 `request_headers JSONB`、`query_params JSONB` 和 `request_body TEXT` 保持不变。JSON/键值切换属于管理端编辑方式，不新增重复存储字段。

## 请求与迭代执行

`keywordsource` 提供持久化无关的迭代配置与请求派生函数：

- 校验位置、路径、次数、停止阈值和固定/随机间隔。
- 按非负索引计算单个迭代值并克隆基础请求；有限模式额外检查索引小于次数，无限模式允许任意非负索引。
- 有限序列仍可一次物化；无限序列明确返回不可物化错误。
- Query/Header 直接写入目标键。
- JSON Body 解析对象并按点路径写入整数，路径中的中间对象必须存在；不隐式创建数组或执行脚本。
- Form Body 写入指定字段的十进制字符串。

`keywordsync.Service` 使用持久化队列和统一循环顺序执行：

1. 保存并同步、立即同步和定时调度先创建 `queued` 运行；单 Worker 领取后进入 `running`，未启用迭代时只执行索引 0。
2. 有限模式在达到配置次数或连续无关键词阈值时结束；无限模式只在阈值、取消或服务关闭时结束。
3. 第一轮立即执行；后续每轮先独立采样随机整数秒，再执行可取消等待。实际等待小于 0 时归零。
4. 每轮独立派生请求、发起 HTTP 请求、解析 JSON 并按 `response_path` 提取。
5. 在跨轮去重和持久化之前更新连续无关键词计数；达到阈值后立即停止，不再等待下一轮。
6. 收集非空成功轮次值并按规范化值内存去重；只要存在成功轮即事务写入关键词和来源关系。
7. 每轮开始和完成都写入历史；运行使用定期续租防止双重领取，服务重启后将租约过期的运行恢复为 `interrupted`。
8. 最终事务写入关键词、逐轮新增/已存在统计、来源摘要和运行状态，并按成功、部分成功或失败规则收尾。

上下文取消时立即停止等待和后续请求，并将运行标记为 `interrupted`。数据库状态写入均有明确超时，避免数据库异常时阻塞服务关闭。错误摘要只记录轮次编号、状态和脱敏错误；历史配置快照仅保留请求方法、协议与主机、Header/Query 字段名及非敏感执行配置，不保存 URL userinfo/path/query/fragment、Header/Query 值、Body 或代理凭据。持续返回非空结果的无限来源会继续占用现有串行同步通道；本设计不增加并发、分批让出、硬性轮次上限或游标恢复。

## 管理 API

现有 CRUD、详情和测试 DTO 完整透传：

- `iteration_enabled`
- `iteration_location`
- `iteration_path`
- `iteration_start`
- `iteration_step`
- `iteration_count`
- `iteration_delay_seconds`
- `iteration_unlimited`
- `iteration_no_keyword_stop_count`
- `iteration_random_delay_min_seconds`
- `iteration_random_delay_max_seconds`

列表继续隐藏 Header、Query、Body 和代理配置，并增加当前运行、最近运行、配置修订和结果过期摘要；详情返回全部配置与最近请求/成功/失败轮数。

`POST /api/admin/keyword-api-sources/:id/copy` 返回新来源详情，名称使用 `<原名称> 副本`；若冲突不强制唯一，因为来源名称不是业务唯一键。副本 `enabled=false`、`next_sync_at=null`、`last_status=pending`，并保留全部新增迭代字段。

测试接口只将起始值写入第一轮请求，并在响应中返回 `iteration_value`，帮助管理员确认参数注入正确；不等待或采样随机延迟。

新增只读历史接口：

- `GET /api/admin/keyword-api-sync-runs`：按来源、状态、触发方式和本地日期范围分页筛选。
- `GET /api/admin/keyword-api-sync-runs/:id`：返回运行详情与逐轮记录。

同步接口保持旧顶层 `status=running` 兼容语义，并通过 `run_status` 与嵌套 `run.status` 暴露真实的 `queued/running` 状态；重复触发返回已有运行并设置 `already_active=true`。

## 管理端界面

请求构造器中 Query 和 Header 各提供“键值 / JSON”分段切换。JSON 编辑器要求根节点为对象，失焦或切换时校验；错误时保留原文本并阻止测试和保存。

Body 区域：

- JSON：JSON 文本编辑。
- Form：键值编辑和 JSON 对象编辑切换。
- Raw：纯文本。
- None：不显示编辑器。

独立“分页迭代”模块包含启用开关、参数位置与路径、起始值、步长、请求次数、无限开关、连续无关键词阈值、固定间隔和随机延迟上下界。无限模式禁用请求次数输入，但不自动将阈值从 0 改为 1；测试和保存时给出明确校验错误。

有限预览展示前六个值、必要时的省略号与末值，并显示 `(次数 - 1) × 单轮等待范围` 的理论总等待范围。无限预览展示前六个值和省略号，同时显示连续无关键词停止条件与单轮等待范围。两者均按 `max(0, 固定间隔 + 随机边界)` 计算，且不包含网络请求时间。

API 来源列表操作保留“复制”，并增加“保存并同步”、运行进度和结果过期提示。关键词页新增“同步记录”页签，支持筛选、短轮询、无限进度、结果统计和逐轮详情。复制成功后刷新列表并提示副本已停用。桌面保持双栏请求构造器；移动端按“请求配置 → 迭代 → 响应检查器”纵向排列，底部保存按钮保持固定可触达，历史表允许横向滚动。

## 兼容性

- 旧来源在两个 migration 后仍为 `iteration_enabled=false`、`iteration_unlimited=false`，新增数值字段均为 0，行为完全不变。
- 未启用迭代时仍只请求一次。
- 有限来源未配置提前停止和随机延迟时，保持原次数与固定间隔行为。
- `DATABASE_URL` 未配置时保持现有 PanSou 行为；管理接口仍返回 503。
- 现有 API 请求 JSON 字段保持兼容，新增字段均有默认值。
- 新队列仍保持单来源、单 Worker 串行执行；未配置数据库时不会启用队列与历史。

## 测试

- 核心单元测试：有限/无限配置校验、按索引取值、有限越界、无限不可物化、溢出、随机闭区间、负随机值和实际等待 0 秒下限。
- 同步测试：连续空轮停止、非空重置、跨轮或数据库重复值不算空、失败计入阈值、有限提前停止、无限停止、状态统计及等待期间取消。
- PostgreSQL 集成测试：migration 默认值与约束、CRUD 字段往返、partial 状态、实际轮次统计、复制配置和副本默认停用。
- 持久化运行测试：入队与并发去重、领取和租约、逐轮统计、成功/部分成功/失败、过期恢复、来源删除快照、日期上界及配置快照脱敏。
- API 测试：新增字段 CRUD、无限模式缺少停止阈值、随机范围非法、测试接口只执行首轮、同步响应兼容、历史筛选、复制和列表脱敏。
- 浏览器验证：开关与次数禁用状态、编辑回填、有限/无限预览、清晰校验错误、测试、保存并同步、进度与逐轮历史，以及桌面和 390×844 移动端布局。

# Memory-CLI 架构诊断：能否扛起"个人第二大脑"的记忆层

> 日期：2026-06-19
> 上下文：memory-cli 作为"记忆"角色，配合 makro（手脚+主动大脑）和用户构成闭环。
> 本文只评估 memory-cli 这一层，不设计 makro 的大脑/调度。

---

## 总体判断

**当前 memory-cli 能扛起记忆角色，但需要 3 个关键改造才能支撑闭环：**

1. **捕获契约不对**：现在是 pull 模型（`ingest --source`），闭环需要 push 模型（makro hook 推写）。API 写入路径存在但字段不全（不能设 project/provenance）。
2. **缺少"拟合信号"的数据结构**：闭环靠 accept/reject 反馈拟合用户偏好，但目前没有 proposal category、没有反馈状态机、preferences 没有"完成率"回溯字段。
3. **dream 引擎几乎全灭**：3 级里 2 级被禁用，画像合成能力完全缺失。

好消息：**存储（SQLite）、时间线聚合、entity 图谱、FTS 搜索**这些地基已经足够扎实，改造成本可控。

---

## 逐条诊断

### 1. 分类体系

**现状**：14 个 category（inbox/people/project/knowledge/preferences/feedback/decisions/lessons/habits/skills/date/soul/character/reminders）。

| Category | 状态 | 说明 |
|---|---|---|
| `inbox` | ✅ 现成可用 | 原始捕获的落地点，语义不变 |
| `knowledge` | ✅ 可用 | organized 知识沉淀池 |
| `preferences` | ⚠️ 需补字段 | 是拟合信号池，但缺"完成率/accept 历史"回溯字段（见下） |
| `habits` | ⚠️ 需补字段 | 同上，需承载行为画像 |
| `decisions` | ✅ 可用 | accept 的 proposal 可以归入这里 |
| `feedback` | ✅ 可用 | reject 的理由可以归入这里 |
| `lessons` | ✅ 可用 | 不变 |
| `skills` | ✅ 可用 | 不变 |
| `people` | ✅ 可用 | 不变 |
| `project` | ✅ 可用 | 不变 |
| `date` | ✅ 可用 | 不变 |
| `reminders` | ⚠️ 已被新 reminders 表取代 | category=reminders 的旧机制与新 reminders 表并存，需清理 |
| `soul`/`character` | ❓ 用途不清 | 语义模糊，建议废弃或重新定义 |

**缺失的 category**：

#### `capture`（必需）—— 原始聊天捕获的专属分类

聊天记录现在全落 `inbox` + `category=knowledge`（之前硬编码导致）。但聊天原始捕获和"待加工 inbox"语义不同：
- `inbox` = 待加工的原料（会被 Extract/Merge 消化）
- `capture` = **时间线事件**（makro push 的"用户说了什么"，按时间回看的原料，不一定要被加工成知识）

**建议**：新增 `capture` category。makro hook push 进来的每条用户消息 → `category=capture, phase=inbox`。capture 是时间线的原料，inbox 是加工队列的原料。两者可以重叠（capture 同时进 inbox 待加工），但 category 维度区分来源。

#### `proposals`（必需）—— 大脑生成的建议 + 状态机

**frontmatter schema 设计**：

```
category: proposals
phase: organized        # proposal 不走 inbox→organized 流程，直接 organized
status: pending         # pending → accepted | rejected | ignored
source: makro-brain     # 谁生成的
tags: [topic-tag, ...]  # 主题标签
created_at: <生成时间>
---
<proposal 内容>
```

**为什么用 category 而不是新表**：proposal 本质是一条"记忆"（有内容、可被搜索、可被关联到 entity），只是多了 status 状态机。用 category 复用现有 CRUD，status 用 `tags` 或新增字段承载。

**status 落地方案**（二选一）：
- **方案 A（轻量）**：status 编码进 tags（`status:pending` / `status:accepted`）。零 schema 改动，但查询稍笨拙。
- **方案 B（推荐）**：新增 `metadata` JSON 列到 memories 表（`ALTER TABLE memories ADD COLUMN metadata TEXT DEFAULT '{}'`），proposal 的 status/reject_reason/accepted_at 都存这里。通用、可扩展，其他 category 也能用。

#### `preferences` / `habits` 的拟合信号增强

闭环的核心是"拟合用户偏好"。现在 preferences 只是一条条静态记忆（"用户偏好 Go"），没有**反馈历史**。

**建议**：用上面的 `metadata` JSON 列，给 preferences/habits 记忆附加：
```json
{
  "evidence": [
    {"type": "accept", "proposal_id": "xxx", "topic": "rust", "at": "2026-06-19"},
    {"type": "reject", "proposal_id": "yyy", "topic": "python", "reason": "不想学新语言", "at": "2026-06-18"}
  ],
  "accept_rate": 0.3,        // accept / total proposals on this topic
  "last_updated": "2026-06-19"
}
```

大脑 query 时读 `preferences` 记忆的 `metadata.evidence`，就能知道"用户对 X 话题的接受率"。

---

### 2. Dream 引擎

**现状**：3 级 dream 里，Light（分类）和 Medium（合并）**全被禁用**，只有 Deep 的 `extractReminders` 在工作（而且已被新的 reminders 表系统取代）。IdleDetector 写了但没人调用。dream **不接入 daemon**。

**诊断**：dream 引擎目前是个空壳。但它的设计意图（空闲时后台整理）正好是闭环需要的。

**建议：画像合成应该是独立新模块，不是 dream 的新一级。**

理由：
- dream 的三级是"整理已有记忆"（分类/合并/提取），是**对内的**。
- 画像合成是"把 accept/reject 历史回滚成一份画像"，是**对外的**（供大脑读取）。
- 把它们混在 dream 里会让 dream 职责膨胀。

**新模块设计**：`internal/profile/`（或 daemon 的 ProfileTask）
- 输入：所有 `category=proposals` 且 `status` 非 pending 的记忆 + 它们的 metadata.evidence
- 输出：一份"用户画像"记忆（`category=character` 重新定义为"画像"，或新增 `profile` category）
- 画像内容：LLM 把 accept/reject 历史总结成"用户对什么感兴趣 + 完成率 + 近期倾向"
- 触发：每次有 proposal 被 accept/reject 时，或 cron 定期（如每天一次）

**与 dream 的关系**：dream 可以保留（未来重新启用 Light/Medium 做知识整理），但画像是独立模块。

---

### 3. 时间线回看

**现状**：`search --from --to` 是关键词搜索命中，不是叙事。`/api/memories/timeline` 按天聚合计数，但没有内容摘要。

**诊断**：缺失"叙事性时间线"。需要**按天聚合 + LLM 摘要**。

**建议：新增 `/api/timeline` 叙事端点**，不走搜索路径：

```
GET /api/timeline?date=2026-06-18&project=makro
```

逻辑：
1. 查当天（或日期范围）的 `category=capture` 记忆（makro push 的原始对话）
2. 按时间排序，拼成一段"当天流水"
3. 调 LLM（GLM-4.5-Flash，免费）把流水总结成 3-5 句叙事："今天你主要做了 X、Y、Z，提出了 idea A"
4. 返回：摘要 + 原始条目（供展开）

**为什么不靠 agent 现生成**：
- agent 的 `memory_search` 工具只搜索不摘要（agent 框架没有 LLM 循环）
- dashboard 的 `/api/ask` 能调 LLM，但它没有时间过滤 + 按天聚合的逻辑
- 叙事时间线是一个高频需求（"昨天做了什么"），值得有专门的优化端点，而不是每次现拼

**实现成本**：低。复用已有的 timeline 聚合 SQL + LLM 摘要（类似 `/api/ask` 但加了时间过滤）。

---

### 4. 与大脑（makro）的读写契约

**这是最关键的接口定义。** makro 的大脑需要明确的 read/write 契约。

#### Read Contract（大脑 query memory）

大脑 cron 醒来或事件触发时，需要读取：

| 读什么 | API 端点 | 参数 | 用途 |
|---|---|---|---|
| 最近 N 天的 capture | `GET /api/memories?category=capture&from=&to=&limit=` | 时间范围 | "用户最近的 idea" |
| 当前用户画像 | `GET /api/memories?category=character&limit=1` | 取最新一条 | "用户对什么感兴趣" |
| 所有 open proposals | `GET /api/memories?category=proposals&tag=status:pending` | 或 metadata 查询 | "待审 inbox" |
| 最近 N 条 accept/reject | `GET /api/memories?category=proposals&tag=status:accepted&limit=10` | | 反馈信号 |
| 相关知识上下文 | `POST /api/recall` | topic 关键词, max_tokens | proposal 生成时的知识注入 |
| 时间线 | `GET /api/timeline?date=` | 日期 | "今天做了什么" |

**关键缺口**：`POST /api/recall` 已存在（token-budgeted 语义搜索），是最适合"大脑生成 proposal 前注入上下文"的端点。但它目前只搜 memories 表，不搜 proposals 的 metadata——需要确认它能否覆盖 proposals。

#### Write Contract（makro push 到 memory）

| 写什么 | 当前端点 | 问题 | 建议 |
|---|---|---|---|
| 用户消息捕获 | `POST /api/memories` | **不能设 project** | 扩展 API 支持全字段写入（见下） |
| 新 proposal | `POST /api/memories` | category=proposals 可以设，但 status/metadata 不能 | 用 metadata 列 |
| accept/reject 反馈 | **无端点** | 需要更新 proposal 的 status | 新增 `PATCH /api/memories/{id}/metadata` |
| 画像更新 | 内部生成 | 不需外部写 | ProfileTask 自动 |

**最关键的改造：扩展 `POST /api/memories` 支持全字段写入**

当前 `POST /api/memories` 只接受 content/category/scope/tags/source，调 `Store.Write`（没有 project 参数）。makro push 捕获时需要设 project（来自 cwd）、role=user、source=makro-hook。

**建议**：新增 `POST /api/ingest` 端点，直接调 `IngestMemory`（接受完整 Memory struct）。保留 `POST /api/memories` 给简单写入。这样 makro 可以：
```
POST /api/ingest
{
  "content": "我想做一个 X",
  "category": "capture",
  "project": "makro",
  "source": "makro-hook",
  "role": "user",
  "tags": ["idea"]
}
```

#### Auth（安全缺口）

**严重**：REST API (:8765) 的 `auth()` 函数定义了但**没有任何 handler 调用它**——即使配了 API keys，:8765 也是完全开放的。makro 和 memory-cli 如果跨机器部署，这是安全漏洞。

**建议**：在修复阶段给所有 :8765 handler 加上 auth 中间件。

---

### 5. 存储模型

**现状**：双后端（FileStore YAML/MD 文件 vs SqliteStore SQLite）。FileStore **丢失** project/session/provenance/consumed_mask 等字段（frontmatter struct 太窄），且无法做 SQL 聚合。

**诊断**：

**SQLite 完全扛得住。** 不需要换存储。理由：
- 18000 条记录，stats SQL 聚合 83ms——性能充裕
- 已有 FTS5 全文搜索、entity 图谱、reminders 表、活动日志
- metadata JSON 列可以承载 proposal status/画像 evidence 等扩展字段
- 时间线聚合、按天分组、heatmap 都是现成的 SQL

**FileStore 应该被弃用**（至少在这个闭环里）。它丢字段、不能聚合、没有 reminders 支持。建议：
- 在文档/配置里明确 SQLite 为唯一推荐后端
- FileStore 保留为 legacy 兼容，但不投入新功能

**需要引入的索引/缓存**：

| 需求 | 是否需要新索引 | 说明 |
|---|---|---|
| 按天时间线聚合 | ❌ 已有 | `created_at` 已可 GROUP BY |
| 画像回滚（扫 proposals 的 metadata） | ⚠️ 建议索引 | `metadata` 是 JSON，频繁查询 status 可加生成列+索引 |
| proposal 状态查询 | ⚠️ | 如果用 tag 方案（status:pending），tags 表已有索引；如果用 metadata JSON，考虑 SQLite JSON 函数 |
| entity 共现查询 | ❌ 已有 | entity_mentions 表已有索引 |

**结论**：不需要换存储、不需要引入 Redis/向量库。SQLite + metadata JSON 列 + 现有索引足够。语义搜索（向量）是未来可选增强，但不是第一版的阻塞项。

---

## 改动清单（按优先级）

### P0 —— 闭环必须，不做就跑不起来

| # | 改动 | 影响范围 | 工作量 |
|---|---|---|---|
| 1 | **扩展写入 API**：新增 `POST /api/ingest` 支持 project/role/source 全字段 | API 层 | 小 |
| 2 | **新增 `capture` category**：makro push 的用户消息落这里 | category 常量 | 极小 |
| 3 | **新增 `proposals` category + metadata 列**：`ALTER TABLE memories ADD COLUMN metadata TEXT DEFAULT '{}'` | schema + memory struct | 小 |
| 4 | **修复 API auth**：给 :8765 所有 handler 加 auth 中间件 | API 层 | 小 |
| 5 | **定义 read contract 端点**：确认 `/api/recall` + `/api/memories` 能覆盖大脑的查询需求，补缺失参数（tag 过滤、metadata 查询） | API 层 | 小-中 |

### P1 —— 拟合能力，不做则大脑"瞎猜"

| # | 改动 | 影响范围 | 工作量 |
|---|---|---|---|
| 6 | **proposal 状态机**：PATCH metadata 端点 + accept/reject 写回 evidence | API + store | 中 |
| 7 | **画像合成模块**（ProfileTask）：扫 proposals 的 accept/reject 历史 → LLM 生成画像记忆 | 新 daemon 模块 | 中 |
| 8 | **preferences/habits 的 evidence 字段**：在 extract/merge 时把 proposal 反馈沉淀到偏好记忆 | processor 改动 | 中 |

### P2 —— 体验增强，不做能用但不好用

| # | 改动 | 影响范围 | 工作量 |
|---|---|---|---|
| 9 | **叙事时间线 `/api/timeline`**：按天聚合 capture + LLM 摘要 | 新 API 端点 | 中 |
| 10 | **弃用 FileStore**：文档标注 SQLite only，停止 FileStore 新功能 | 文档 + 配置 | 极小 |
| 11 | **清理旧 reminders 机制**：dream 的 extractReminders 和 category=reminders 旧数据归档 | dream 清理 | 小 |
| 12 | **entity 共现查询**：支持"find related ideas" | entity 层 | 中 |

### P3 —— 远期

| # | 改动 | 说明 |
|---|---|---|
| 13 | 语义搜索（向量索引） | proposal 生成时找"语义相关"而非关键词匹配的记忆 |
| 14 | dream 引擎重启用 | Light 分类 + Medium 合并，后台整理知识 |

---

## 风险（最容易出问题的地方）

### 风险 1：拟合信号污染（最高风险）

闭环的核心假设是"accept/reject 反馈让 proposal 越来越准"。但如果：
- 用户大量 `ignore`（不点 accept 也不点 reject）→ 画像没有负信号
- proposal 质量一开始很差 → 用户全 reject → 画像被"我什么都不想做"主导
- accept/reject 理由不被记录 → 画像只有频率没有因果

**缓解**：ignore 不计入画像；最初几轮 proposal 用宽泛偏好（少加约束）；reject 必须带理由（makro 侧强制）。

### 风险 2：捕获噪声淹没信号

makro hook 捕获"用户说的每一句话"——但大部分聊天是琐碎的（"改下这个 bug"、"跑下测试"）。如果这些全进 capture 并被大脑当成"idea"读，proposal 质量会很差。

**缓解**：capture 层面需要一个轻量分类——makro push 时标注 `type: idea|task|chat|debug`（makro 侧能区分），memory 只把 `type: idea` 给大脑读。不要指望 memory 自己判断哪些是 idea。

### 风险 3：写入 API 成为单点

整个闭环依赖 `POST /api/ingest` 能接住 makro 的高频 push。如果 memory serve 挂了、或 API 无响应，makro 的 hook 会静默丢数据。

**缓解**：makro 侧的 hook 需要本地 buffer + retry（makro 的职责，但要明确告诉 makro 这个依赖）。memory 侧的 ingest 要幂等（已有 content_hash 去重，✅）。

### 风险 4：metadata JSON 查询性能

proposal 状态、画像 evidence 都塞 metadata JSON 列。SQLite 的 JSON 函数性能在大数据量下不如原生列。

**缓解**：proposal 总量不会很大（每天几十条），JSON 查询性能足够。如果未来 proposals 上万，再把 status 提取为原生列 + 索引。

### 风险 5：两套 reminders 系统并存

旧的（category=reminders 记忆 + dream extractReminders + NotifyTask）和新的（reminders 表 + ReminderTask）并存，容易混淆。

**缓解**：P2 清理——dream 的 extractReminders 禁用，旧 category=reminders 记忆归档或迁移到 reminders 表。

---

## 结论

memory-cli **能扛起记忆角色**，地基（SQLite、FTS、entity 图谱、时间线聚合、reminder 系统）已经足够扎实。需要做的是：

1. **打通写入管道**（P0 #1-4）：让 makro 能 push 全字段捕获
2. **建拟合信号结构**（P1 #6-8）：proposal 状态机 + 画像合成
3. **补叙事层**（P2 #9）：时间线回看

最大的架构风险不在 memory 这层，而在**捕获质量**（makro 侧的 type 标注）和**冷启动**（最初几轮 proposal 的偏好稀疏）。这两点是 makro 和用户的交互问题，memory 只需要确保"接得住、存得好、查得到"。

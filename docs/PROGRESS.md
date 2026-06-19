# Memory-CLI 进度落盘

> 最近更新：2026-06-19 01:30
> 当前分支：main
> 最新 commit：`7419920` profile + timeline

---

## 总体里程碑

memory-cli 正在支撑一个"个人第二大脑"系统（memory-cli = 记忆，makro = 手脚+主动大脑，用户 = idea 来源）。核心闭环：大脑读 memory 里的画像/idea → 生成 proposal → 推给用户 → accept/reject 反馈写回 memory → 画像越来越准。

### 架构契约
- 对齐文档：`docs/RECONCILE.md`（makro 和 memory-cli 双方裁决）
- 诊断文档：`docs/ARCHITECTURE_DIAGNOSIS.md`（memory-cli 全貌诊断）

---

## 已完成（按时间倒序）

### 2026-06-19 · RECONCILE P0/P1/P2 契约改造

| 优先级 | 项目 | Commit | 说明 |
|---|---|---|---|
| P0 #1 | POST /memories 全字段 + metadata 列 | `1f90138` | project/role/metadata 全字段写入，metadata JSON 列 |
| P0 #2 | GET /memories tags=/from= 参数 | `1f90138` | tags AND 过滤 + 相对/绝对时间过滤 |
| P0 #3 | :8765 auth 修复 | `1f90138` | 所有 handler 包 auth 中间件（之前完全开放） |
| P0 #4 | CategoryCapture/CategoryProposals 常量 | `1f90138` | 新增常量 + AllCategories |
| P1 #7 | PATCH /memories/{id} metadata 状态机 | `1f90138` | proposal accept/reject 的回写路径 |
| P1 #11 | character 重定义 + ProfileTask | `7419920` | 扫 proposals+feedback → LLM 合成画像 |
| P2 #12 | 叙事时间线 /api/timeline | `7419920` | capture 聚合 + LLM 摘要 |

**验收**：
- 全字段写入：project=makro role=user metadata={...} ✓
- timeline 叙事：3 条 capture → 准确的叙事摘要 ✓
- ProfileTask：2 proposals + 29 feedback → 用户画像（accept_rate 0.5, domain 分布）✓
- auth：无 key 返回 401 ✓

### 2026-06-19 · 事件驱动提醒系统

| Commit | 说明 |
|---|---|
| `4ec882e` | reminders 表 + 中文时间解析 + `memory remind`/`memory reminders` 命令 + ReminderTask |

- 支持中文时间解析（下午3点/明天/2小时后/下周一）
- daemon 每 60s 检查到点提醒，发 macOS 通知 + 写 pending.md
- fired 后不重复触发（修了旧 NotifyTask 的 bug）

### 2026-06-18 · Dashboard 扩展性改造

| Commit | 说明 |
|---|---|
| `2939b7e` | SQL 聚合 + processor 维度 + 分页 |

- stats 改 SQL COUNT（83ms，之前要加载 18k 记录）
- stat cards 按 processor 维度展示（unconsumed/fact-processor/process-cmd/...）
- 列表分页（offset + Load more）+ graph 惰性加载（只看 organized）

### 2026-06-18 · 数据加工 pipeline

| Commit | 说明 |
|---|---|
| `f11a246` | `memory process` 命令 + 上下文 Extract + provenance |
| `6f74550` | FTS 搜索下推过滤 + project/time UI + timeline API |
| `395d954` | zcode adapter + Extract JSON 解析加固 |
| `ff59b78` | organized ID 空字符串修复 + JSON wrapper 解析 |
| `b2dac6b` | 去掉 MarkProcessed（inbox phase 不改动） |
| `fdc8e4b` | consumed_mask bitmask 追踪加工状态 |

**关键改进**：
- Extract 带 role/project 上下文（之前是扁平文本，模型分不清问题和答案）
- `memory process --project makro --limit 800` 手动批量加工
- 6 个项目批量加工：4800 条 → 387 extracted → 48 organized
- zcode 对话导入（之前在 ~/.zcode/cli/rollout，adapter 没扫到）
- GLM-4.5-Flash 的 JSON 不稳定，加了多层防御性解析

### 2026-06-18 · LLM 后端切换

| Commit | 说明 |
|---|---|
| `a7dbeee`（含在之前的 commit 里） | Anthropic SDK → GLM-4.5-Flash |

- 完全移除 anthropic-sdk-go，net/http 直连 z.ai
- 复用现有 key（从 ~/.claude/settings.json 读 GLM_API_KEY）
- 识别 Anthropic 协议 URL（/api/anthropic）并回退到 OpenAI 兼容 endpoint
- thinking:disabled 关闭推理（Flash 默认带思考，浪费 token）

### 2026-06-18 · 数据地基

| 改动 | 说明 |
|---|---|
| Memory 表加 6 列 | message_uuid/parent_uuid/role/git_branch/model/prompt_id |
| conversations.go 重写 | 收 user+assistant（含 thinking），subagent 正确归位 |
| ingest_adapter.go bug 修复 | SessionID: m.Source → m.SessionID（之前 session 归属全错） |
| groupBySession 利用 prompt_id | 同一轮问答聚在一起送 Extract |

---

## 当前数据状态

| 指标 | 数值 |
|---|---|
| 总记忆数 | ~18000+ |
| inbox（原始对话） | ~17000 |
| organized（结构化知识） | ~900 |
| processed | ~120 |
| zcode 对话 | 100 |
| 新增 category | capture, proposals |
| reminders | 独立表，支持时间触发 |

---

## 剩余待做（按优先级）

### 高优先（闭环增强）
- [ ] **P2 #10**: 弃用 FileStore（文档标注 SQLite only）
- [ ] **P2 #11**: 清理旧 reminders 机制（dream extractReminders + category=reminders 旧数据）
- [ ] **P3 #18**: preferences/habits 加 evidence 字段（accept/reject 历史）

### 中优先（体验/质量）
- [ ] Extract 对大 session 分段（720 turns 一次提取率低）
- [ ] concept-tags 批量回填（给 organized 记忆打语义标签）
- [ ] dashboard 搜索结果服务端分页（目前大匹配集全拉到客户端）

### 低优先（远期）
- [ ] **P3 #12**: entity 共现查询（find related ideas）
- [ ] 语义搜索（向量索引）
- [ ] dream 引擎重启用（Light 分类 + Medium 合并）
- [ ] 多 agent 主动推送（webhook，目前 agent 读 pending.md）

---

## 关键技术决策记录

1. **SQLite 是唯一推荐后端**——FileStore 丢字段、不能聚合、不支持 metadata/reminders
2. **category 不是固定枚举**——NormalizeCategory 放行非标准值，但加常量让 dashboard/dream 认得
3. **proposal status 走 metadata JSON，不走 tag**——tag 方案非原子
4. **memory 是唯一真相源**——makro 的 inbox.db 是 UI 缓存，从 memory 重建
5. **inbox phase 永不改动**——加工用 consumed_mask bitmask 追踪，不改 phase
6. **画像合成是独立模块（ProfileTask），不是 dream 的新一级**——职责不同
7. **GLM-4.5-Flash 免费，但 JSON 不稳定**——多层防御性解析（嵌套数组/wrapper/单字符串）

---

## 风险跟踪

| 风险 | 状态 | 缓解 |
|---|---|---|
| 拟合信号污染（ignore 不计、冷启动） | 待观察 | makro 侧强制 reject 带理由 |
| 捕获噪声（琐碎对话淹没 idea） | makro 侧负责 | makro push 时标注 type: idea/task/chat |
| 写入 API 单点（serve 挂则丢数据） | 待缓解 | makro 侧 buffer+retry（memory 侧已幂等） |
| metadata JSON 查询性能 | 低风险 | proposal 量小，未来可提取原生列 |

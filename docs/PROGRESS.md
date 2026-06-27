# Memory-CLI 进度落盘

> 最近更新：2026-06-27（追加架构清理阶段）
> 当前分支：main
> 最新 commit：`79b3c77`（架构分层清理完成，四个混乱点全部关闭）
> 历史里程碑：commit `647cdf2` + P3 #18（EvidenceTask）

---

## 架构清理阶段（2026-06-27，5 轮迭代，全部 e2e 验证）

目标：建立清晰的分层契约，消除失控感。5 轮清理后**架构分层干净**——依赖图无循环，四个混乱点全部关闭。

### 完成的收敛（commits `4336e9b`→`79b3c77`）

| Commit | 内容 | 架构混乱点 |
|--------|------|-----------|
| `4336e9b` | 命令清理 27→18（删 dream/decay/agent/consolidate/process/upgrade/link 等 10 个废弃/重复命令 + 5 个 daemon no-op task） | — |
| `19ed0d0` | MCP 补 read/delete tool（6→8）+ store Read/Delete 支持 UUID 前缀匹配 | — |
| `220ab73` | 新建 `internal/query` 公共包，dashboard 与 mcp 共用查询理解逻辑（净删 274 行） | ④ ✅ 收敛 |
| `8fe67a5` | `IngestMemory` 统一写入咽喉点 + supersede 提交后原子触发（所有写入路径自动版本跟踪） | ② ✅ 收敛 |
| `79b3c77` | 版本跟踪逻辑抽到 `store/versioning.go`（职责整理） | ③ ✅ 作废（依据错） |

### 四个混乱点最终状态

| 混乱点 | 状态 | 处理 |
|--------|------|------|
| ① 三套 Extract+Merge | ✅ 澄清 | processor/factprocessor 是正交两维度（SSE 追踪 vs 可插拔契约），非重复，不作收敛 |
| ② 写入入口分散（17处）| ✅ 收敛 | IngestMemory 单一咽喉点，supersede 自动触发 |
| ③ 版本跟踪错位 | ✅ 作废 | 原依据错（纯文本相似度，不用 entity/predicate）；仅做职责整理抽到 versioning.go |
| ④ MCP 复制 dashboard ask | ✅ 收敛 | internal/query 公共包 |

**权威分层文档**：`docs/ARCHITECTURE.md`（含 architecture.svg）。`ARCHITECTURE_DIAGNOSIS.md`/`RECONCILE.md` 是 6/19 的历史快照。

---

## 总体里程碑

memory-cli 正在支撑一个"个人第二大脑"系统（memory-cli = 记忆，makro = 手脚+主动大脑，用户 = idea 来源）。核心闭环：大脑读 memory 里的画像/idea → 生成 proposal → 推给用户 → accept/reject 反馈写回 memory → 画像越来越准。

### 架构契约
- 对齐文档：`docs/RECONCILE.md`（makro 和 memory-cli 双方裁决）
- 诊断文档：`docs/ARCHITECTURE_DIAGNOSIS.md`（memory-cli 全貌诊断）

---

## goal 完成度盘点（2026-06-20）

memory-cli 的 goal = 兑现 RECONCILE.md 契约里所有 `[M]`（memory-cli 侧）改动。

| 优先级 | 项 | Commit | 状态 |
|---|---|---|---|
| P0 #1 | POST /memories 全字段 + metadata 列 | `1f90138` | ✅ |
| P0 #2 | GET /memories tags=/from= 参数 | `1f90138` | ✅ |
| P0 #3 | :8765 auth 修复 | `1f90138` | ✅ |
| P0 #4 | CategoryCapture/CategoryProposals 常量 | `1f90138` | ✅ |
| P1 #7 | PATCH /memories/{id} metadata 状态机 | `1f90138` | ✅ |
| P1 #11 | character 重定义 + ProfileTask | `7419920` | ✅ |
| P2 #12 | 叙事时间线 /api/timeline | `7419920` | ✅ |
| **P3 #18** | preferences/habits 加 evidence 字段 | 本次 | ✅ |

**memory-cli 侧契约 = 全部完成（8/8）。** 第一周验收 #1（P0 通）达标。

makro 侧（`~/Desktop/Code/Makro/internal/brain/`，已核对）：

| `[K]` 项 | 对应文件 | 状态 |
|---|---|---|
| #6 memory_client.go | `memory_client.go`（548 行，对齐 `:8765`，明确标注 "NOT :8090"） | ✅ 已实现 |
| #5 capture sink | `capture.go`（339 行）+ `NewCaptureSink` | ✅ 已实现 |
| #8 brain/reader/propose | `brain.go`/`reader.go`/`propose.go` | ✅ 已实现 |
| #9 inbox | `inbox.go`（244 行） | ✅ 已实现 |
| #10 feedback | `feedback.go`（122 行） | ✅ 已实现 |
| 接入主流程 | `main.go` + `cmd/gui/chat_service.go` 都 `brain.NewBrain(...)` + `go brain.Run()` | ✅ 已接入 |

**结论：闭环两侧的代码都已落地**——memory-cli 的 `[M]` 契约全完成，makro 的 `[K]` brain 模块全实现且对齐 `:8765`。**剩余的是联调验证**（第一周验收 #2/#3：08:00 推送 → accept/reject → 状态一致），需要两侧同时跑起来观察真实数据流。

---

## 已完成（按时间倒序）

### 2026-06-20 · P3 #18 EvidenceTask（契约最后一项）

| 文件 | 说明 |
|---|---|
| `internal/daemon/evidence.go` | EvidenceTask — 按 domain 聚合 verdict 沉淀到 per-topic preferences 记忆 |
| `internal/daemon/evidence_test.go` | 分桶 + accept_rate 计算 + 幂等测试 |
| `internal/cmd/serve.go` | 注册 EvidenceTask（ProfileTask 之后） |

ProfileTask 写**全局**画像（一条 character 记忆）；EvidenceTask 写**per-domain** 的拟合信号（"topic: Debugging — accept_rate 0.5"）。两者互补：
- 扫 proposals（status 非 pending）+ feedback（有 verdict）
- 按 domain 分桶，每桶算 accept_rate / verdict_count
- 用 `source=evidence-task, metadata.topic=<domain>` 做幂等 upsert（`metadata LIKE` 查找，proposal 量小无需索引）
- pending proposals 和无 verdict 的旧 feedback 被跳过（无拟合信号）
- 通过 `UpdateMemoryMetadata` 原子合并，重跑幂等

**测试**：`TestEvidenceTaskBucketsByDomainAndComputesRate`（Debugging 0.5 / Languages 1.0）+ `TestEvidenceTaskIsIdempotent`（重跑不建新行）✅

### 2026-06-19 晚 ~ 06-20 · 中文搜索可靠性攻坚（13 commits，契约之外的体验轴）

这批工作**不在 RECONCILE 契约里**，是另一个需求轴：让用户能直接向 memory 提问。围绕一个核心 bug——中文实体搜索（"瑞福莱暖通设备"）一直返回"没找到"。

| 层次 | 问题 | 解法 | Commit |
|---|---|---|---|
| 分词 | FTS5 unicode61 不切中文，整句当 1 个 token | LLM 抽关键词（glm-4.7-flash）+ `ChatWithModel` 按调用覆写模型 | `8bc2fbd` `647cdf2` |
| 召回 | FTS 默认 AND 太严，OR 又被"上海"噪声淹没 | LIKE + **IDF 加权**排序（稀有实体浮顶），`SearchLike` 出 Store 接口 | `cd9337d` `2b41fdc` |
| 摘要 | "瑞福莱"出现在第 500 字符，300 字窗口看不到 | 关键词居中 snippet（窗口以首个命中为中心） | `cd9337d` |
| 上下文 | 追问"具体的细节"返回 0 结果 | 回退到上一轮用户问题重抽关键词重搜 | `647cdf2` |
| 长串 | 全称 13 字 vs 内容只有 3 字"瑞福莱" | CJK 渐进前缀匹配（12→2 字，每段重算 IDF） | `647cdf2` |
| 日期 | LLM 回"6/19"（加工日期）而非事件日期 | 平衡 phase 采样（inbox 升序排）+ `DATE:` 前缀 + 时间窗 header | `5ceddb9` `f513348` `a97d1f0` |

顺带做的 chat 面板 UX（dashboard）：
- 默认走快速模式 `/api/ask`（单次搜索+单次 LLM，~5s），agent 改为可选"深度搜索" | `62e660f`
- markdown 渲染、IME 回车、分阶段加载 spinner、追问历史 | `5d8d33d` `2fd2508` `27329d6`

**验证**："瑞福莱暖通设备的服务记录" → 返回开票记录（之前"没找到"）；"RSA什么时候制作的" → 6/3→6/8（之前"6/19"）。

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
| `ff59f78` | organized ID 空字符串修复 + JSON wrapper 解析 |
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
| preferences | 91（+ EvidenceTask 产出的 per-domain，运行后） |
| feedback | 30（其中 1 条带 verdict，29 条旧格式无 verdict） |
| proposals | 1（rejected, domain=Debugging） |
| reminders | 独立表，支持时间触发 |

---

## 剩余待做（按优先级）

### 高优先（闭环增强）
- [ ] **两侧联调**：memory-cli `[M]` 契约 + makro `[K]` brain 代码都已落地，但第一周验收 #2/#3（08:00 推送 → accept/reject → 状态一致）从未真实跑过。需同时起 `memory serve` + `makro brain` 观察数据流
- [ ] **daemon CPU 异常**：当前运行的 `memory serve`（PID 1639）CPU 76.9%，可能是死循环；daemon.log 已 58MB。需排查后用新二进制重启（才能加载 EvidenceTask）
- [ ] **P2 #10**: 弃用 FileStore（文档标注 SQLite only）

### 中优先（体验/质量）
- [ ] Extract 对大 session 分段（720 turns 一次提取率低）
- [ ] concept-tags 批量回填（给 organized 记忆打语义标签）
- [ ] dashboard 搜索结果服务端分页（目前大匹配集全拉到客户端）

### 低优先（远期）
- [ ] **P3 #12**: entity 共现查询（find related ideas）
- [ ] 语义搜索（向量索引）
- [ ] dream 引擎重启用（Light 分类 + Medium 合并）
- [ ] 多 agent 主动推送（webhook，目前 agent 读 pending.md）
- [ ] 清理旧 reminders 机制（dream extractReminders + category=reminders 旧数据）

---

## 关键技术决策记录

1. **SQLite 是唯一推荐后端**——FileStore 丢字段、不能聚合、不支持 metadata/reminders
2. **category 不是固定枚举**——NormalizeCategory 放行非标准值，但加常量让 dashboard/dream 认得
3. **proposal status 走 metadata JSON，不走 tag**——tag 方案非原子
4. **memory 是唯一真相源**——makro 的 inbox.db 是 UI 缓存，从 memory 重建
5. **inbox phase 永不改动**——加工用 consumed_mask bitmask 追踪，不改 phase
6. **画像合成是独立模块（ProfileTask），不是 dream 的新一级**——职责不同
7. **EvidenceTask 是 ProfileTask 的细粒度补充**——全局画像 vs per-domain 拟合信号，互不重复
8. **GLM-4.5-Flash 免费，但 JSON 不稳定**——多层防御性解析（嵌套数组/wrapper/单字符串）
9. **中文搜索绕开 FTS 走 LIKE+IDF**——FTS5 unicode61 不切 CJK，IDF 加权让稀有实体浮顶

---

## 风险跟踪

| 风险 | 状态 | 缓解 |
|---|---|---|
| 拟合信号污染（ignore 不计、冷启动） | 待观察 | makro 侧强制 reject 带理由 |
| 捕获噪声（琐碎对话淹没 idea） | makro 侧负责 | makro push 时标注 type: idea/task/chat |
| 写入 API 单点（serve 挂则丢数据） | 待缓解 | makro 侧 buffer+retry（memory 侧已幂等） |
| metadata JSON 查询性能 | 低风险 | proposal 量小，未来可提取原生列 |
| daemon CPU 异常（PID 1639 占 76.9%） | 🔴 待查 | 可能死循环，需排查 daemon.log（58MB） |

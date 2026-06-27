# Known Issues

> 已知问题清单。修完的条目移到 `## Resolved`。

---

## Resolved

### ISSUE-001:`memory_search` 多关键词组合查询召回错误 —— ✅ 已修复(2026-06-27)

**修复 commit**:本次(`SearchLike` 空格分词 + `Search` 多词路由)

**根因**:两个 search 工具路径不一致。
- `memory_search` → `SqliteStore.Search` → FTS5 `MATCH ?`(整串 query)。FTS5 的 `unicode61` tokenizer 不切 CJK,多 CJK 词组合(「橘粒科技 合同 报价」)在默认 AND 语义下返回 0。
- 0 行时 fallback 到 `SearchLike`,但 `SearchLike` 只按 `|`/` OR ` 分 keyword——**不认空格**,于是整串被当成一个 keyword,`Contains` 匹配 → 0。
- 对照 `memory_smart_search`(自带 `tokenize` 分词)多词正常工作,两个工具行为不一致是误用根源。

**修复**(两层):
1. `Search` 检测到 query 含空格(多词)时**直接路由到 `SearchLike`**,绕过 FTS5 的 CJK 分词缺陷。
2. `SearchLike` 的 keyword 分隔增加**空格 + 逗号**(不止 `|`/` OR `),多词 query 现在按词分 keyword,每个词独立 IDF 打分。

**验证**(2026-06-27 实测):
- `memory search "橘粒科技 合同 报价"` → 之前 0/2 条噪音,现在 **8 条**含 橘粒/合同/报价 的真实记忆。
- `memory search "瑞福莱暖通设备"`(单 token,走 FTS)仍正常返回 5 条,0s。
- 回归测试 `TestSqliteSearchLikeMultiWord` 覆盖多词 fan-out + Search 路由。

**备注**:CJK 实体名(「瑞福莱暖通设备」)不含空格,空格分词不会破坏它的整体性——仍是单个不可分 keyword,走 CJK 前缀回退逻辑。

---

### ISSUE-002:`memory_ask` 身份类问题(「用户是谁」)—— ✅ 已修复(2026-06-27)

**修复 commit**:`4a33595`(SearchLike N+1 超时)+ `a129d9b`(evidence 聚合污染 preferences)

**真实根因**(推翻录入时的「缺画像」猜测):
1. **`SearchLike` N+1 全表扫描** —— ASCII 关键词(如 `user`)的 CJK 前缀回退退化成 `us`/`use`,匹配上千文档;13k 记忆 × ~143ms ≈ 23 分钟 → `memory_ask`「用户是谁」**超时返回空**。
2. **evidence 聚合污染 preferences** —— `EvidenceTask` 把 per-domain 统计聚合(`accept_rate=0.00…`)写进 `CategoryPreferences`,这些垃圾不断重写 `content`/`updated_at`,在 newest-first 排序里**淹没真实画像**,于是「用户是谁」浮出来的是统计噪音而非用户画像。

**修复**:
- `4a33595`:prefix IDF **预计算**(每 keyword 一次,而非每 memory)+ ASCII 关键词跳过前缀扩展 + 修 CJK prefix bug。SearchLike 不再卡死。
- `a129d9b`:新增 `CategoryEvidence`,EvidenceTask 改写到那里;`SearchOptions.ExcludeSources` 让语义搜索(memory_ask / dashboard / MCP)排除 `source=evidence-task` + `source=profile-task`;`migrateLegacyPreferenceRows` 自愈存量数据。

**生产 DB 验证**(2026-06-27,只读 SQL):
- preferences 中 `source=evidence-task` 污染 = **0 行**
- preferences 中 `source=profile-task` = **0 行**
- `evidence` category = **5 行**(迁移过来的 legacy 聚合)
- preferences total = **112 干净**
- 与 commit message 吻合("5 legacy rows migrated, preferences now 112 clean, 0 polluted")。

**备注**:录入时「记忆只存事件流、缺画像」的猜测**不准**——画像数据(`profile-task`)其实一直在,只是被 evidence 统计淹没 + SearchLike 超时。`memory_ask` 的问答链路本身没动。

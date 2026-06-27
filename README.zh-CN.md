# Memory CLI

> **[English](./README.md)** | 中文

多 AI Agent 统一记忆管理。

Agent（Claude Code、Copilot、FingerSaver 等）通过 CLI、stdin JSON-RPC 或持久化 Unix socket 读取、写入、搜索、整合记忆。后台守护进程持续分类、关联、精炼存储的记忆。

## 安装

```bash
go build -o memory .
# 或
go install github.com/cybernagle/memory-cli@latest
```

## 快速开始

```bash
# 初始化（创建 ~/.memory/）
memory write "用户偏好深色模式" --category preferences --tags ui

# 读回
memory read <id>

# 搜索
memory search "深色模式"

# 按类别列出
memory list --category knowledge

# 从已有数据源导入
memory ingest --source claude

# 启动守护进程 + socket 服务
memory serve
```

## 存储模型

记忆在 15 个类别间流转于不同 phase：

```
inbox → processed → organized   （Extract+Merge 加工链）
```

记忆存储在 **SQLite**（`~/.memory/memories.db`），不是平铺文件。每条记忆包含：

- `phase` — inbox / processed / organized
- `category` — knowledge / preferences / decisions / project / people / evidence / …（共 15 个）
- `metadata`（JSON）— 版本跟踪（`superseded_by`）、proposal 状态、evidence 聚合、溯源（tmux_session、prompt_id、…）
- `source` — 溯源：谁/什么写的（如 `claude`、`makro-brain`、`consolidate`、`evidence-task`）
- `tags`、`links`（双向 `[[wikilinks]]`）

溯源列（`tmux_session`、`prompt_id`、user+assistant 轮次）让任何 organized 记忆都能追溯回源对话。

> **完整分层架构见 [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md)** —— 5 层（foundation → core → domain → orchestration → composition）、数据流图、每次改 feature 该遵守的跨层规则。

## CLI 参考

### 核心 CRUD

```bash
memory write <content> [--category <cat>] [--tags t1,t2] [--scope global] [--source <name>]
memory read <id>
memory delete <id>
memory list [--phase inbox|organized] [--category <cat>] [--scope ...] [--source ...]
```

### 搜索

```bash
memory search <query> [--tags ...] [--category ...] [--from YYYY-MM-DD] [--to YYYY-MM-DD]
```

### 双向链接

记忆之间可用 `[[wikilink]]` 语法互引。Wikilink 在 ingest 时自动解析，`EntityExtractionTask`（守护进程）保持实体图谱 + 反向链接更新——无需手动链接命令。

### 加工与整合

守护进程运行 Extract+Merge 管线（通过 `factprocessor`，基于 `plugin` 契约的正式实现）加若干后台精炼任务：

```bash
memory serve            # 启动守护进程：consolidate / enrich / profile / entity / evidence / reminder
```

**守护进程任务**（`memory serve` 运行）：
- `ConsolidateLLMTask` — processed → organized（LLM 合并）
- `EnrichTagsTask` — tag 富化
- `ProfileTask` — 用户画像合成（→ `character`）
- `EntityExtractionTask` — LLM 实体发现（填充实体图谱）
- `EvidenceTask` — proposal accept/reject 聚合（→ `evidence`）
- `ReminderTask` — 到期提醒 → macOS 通知

### 通知

```bash
memory notify
```

检查到期提醒并推送 macOS 通知。待办提醒写入 `~/.memory/pending.md` 供 agent 启动时消费。

### 守护进程

```bash
memory serve [--interval 60s]
```

启动后台守护进程（consolidate-llm / enrich / profile / entity-extract / evidence / reminder）加 Unix socket 服务（`~/.memory/memory.sock`）。这是所有后台加工的唯一入口。

### 导入

```bash
memory ingest --source claude|car-agent|fingersaver|logseq|obsidian|all
```

### 导出 / 导入

```bash
memory export [--output file] [--category ...]
memory import [--input file]
```

JSONL 格式，每行一条记忆。

## Agent 集成

### 方式 1：CLI

任何能跑 shell 命令的 agent：

```bash
memory write "用户偏好 vim" --category preferences
memory search "编辑器偏好"
```

### 方式 2：MCP（stdio JSON-RPC）— 供 Claude Code / zcode / makro

```bash
# 列出工具
memory mcp
```

通过 stdio 说 JSON-RPC 2.0（initialize → tools/call）。8 个工具：`memory_ask`、`memory_search`、`memory_write`、`memory_read`、`memory_delete`、`memory_timeline`、`memory_list`、`memory_remind`。

### 方式 3：Unix Socket — 持久连接（守护进程运行时）

```bash
# 连接
socat - UNIX-CONNECT:~/.memory/memory.sock

# 列出工具
{"id":1,"method":"tools/list","params":{}}

# 搜索
{"id":2,"method":"tools/call","params":{"name":"memory_search","params":{"query":"深色模式"}}}

# 简写：直接用工具名作 method
{"id":3,"method":"memory_list","params":{"category":"knowledge"}}
```

每个请求/响应是单行 JSON。

## 配置

`~/.memory/config.yaml`：

```yaml
storage:
  root: ~/.memory
  short_term_ttl: 168h    # Inbox TTL（7 天）

daemon:
  interval: 60s

notification:
  enabled: true
  method: osascript

timezone: "Asia/Shanghai"
```

## 架构

memory-cli 是一个 **5 层**系统，依赖严格自底向上（依赖图是无环 DAG —— 无循环）：

```
⑤ cmd            — 组装层（cobra 接线，18 个子命令）
④ daemon/api/mcp/transport — 编排层（调度 + 协议适配）
③ ingest/factprocessor/entity/plugin/query/dashboard/agent — 领域层（语义加工）
② store          — 存储核心（SQLite，12 个依赖方）★
① config/llm/notify/health — 基础层（零内部依赖）
```

**完整图 + 各层职责 + 跨层规则**：见 [`docs/ARCHITECTURE.md`](./docs/ARCHITECTURE.md)。

## 开发

```bash
go build -o memory .
go test ./...
go vet ./...
go fmt ./...
```

## License

MIT

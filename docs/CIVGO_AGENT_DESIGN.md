# Civgo Agent 化检索改造规格书（dev_civgo 分支专项）

> **给实现者的开场白**：本文档是「抛弃向量检索，改 AI 自主检索」改造的实施规格。
> 开新窗口时：
> 1. 通读本文档（重点：§2 决策、§6 网关验证、§8 工具协议、§9 agent 循环、§12 编排改造、§14 实施顺序）；
> 2. 前置阅读：`docs/CIVGO_DESIGN.md`（旧架构基线，凡与本文档冲突处以本文档为准）、
>    `internal/civgo/service.go`（现有编排）、`internal/civgo/syncer.go`（同步器）、
>    `internal/mailer/mailer.go` 与 `internal/admin/server.go`（邮件通道复用参考）、`main.go`（接入点）；
> 3. 按 §14 顺序实现，每步跑通对应测试；全部完成后 `go test ./...` + `go vet ./...` 全绿；
> 4. 改动边界见 §4 文件清单：civgo 包内自由增删改，`main.go` 仅允许追加邮件注入，
>    其余既有包（bot/rules/admin/state/onebot/config/mailer/watchdog 等）一律不动；
> 5. 提交留在 `dev_civgo` 分支，标题 `cg0.1.x-<简短描述>`（§14），不 push，由用户审阅。

---

## 1. 背景与目标

现有 civgo 问答链路：git 同步文档 → 分块 + **向量检索**（bge-m3 嵌入 + 余弦 top-k）→
拼接固定上下文 → 单轮 AI 回答。本轮改造四个目标：

| # | 目标 | 说明 |
|---|---|---|
| G1 | **抛弃向量搜索** | 删除嵌入客户端、向量索引、分块器及全部相关配置/数据，不再构建索引 |
| G2 | **AI 自主检索** | 类现代 agent 工具：AI 根据群友问题自行决定读哪些文档、读多少，汇总回答（function calling 工具集） |
| G3 | **AI 决策历史** | 智能决定提问是否带历史记录、带哪些（按群历史 + 工具召回，调用即决策） |
| G4 | **token 用量预警** | 滑动窗口计量，短时间内消耗大量 token 后邮件提醒（复用 SMTP 通道） |

## 2. 已确认决策（与用户逐项确认，勿改）

| # | 决策点 | 结论 |
|---|---|---|
| D1 | 检索实现方式 | **仅 function calling**（Responses API tools）。**不做** plan/JSON 计划降级模式；网关不支持 tools 时按 §6 处理 |
| D2 | 历史记录粒度 | **按群**环形缓冲，条目标注提问者；AI 经 `recall_history` 工具按关键词/时间召回 |
| D3 | 旧代码处置 | **直接删除**：embed.go / index.go / chunk.go 及对应测试；配置字段 `retrieval.*`、`ai.embedding_model` 删除；`data/civgo/index.json` 清理（git 历史可回溯） |
| D4 | 计划书 | 本文档落盘并提交（cg0.1.1） |
| D5 | 文档同步 | 沿用 git 轮询同步（clone shallow + sparse，只取 docs/game_content），**同步器保留**，仅把「重建索引」改为「更新文档地图」 |
| D6 | 邮件通道 | 复用 control.json 的 SMTP（main.go 注入 `mailer.Sender` 到 civgo.Options，同 watchdog 模式），civgo 不直接依赖 state/config 包 |
| D7 | 通用常识注入 | **去掉** `loadGeneralContext` 固定注入：索引/阅读顺序类文档由 AI 经 `list_docs` 自主发现阅读（docmap 中标记，工具描述引导） |
| D8 | 配置体系 | 仍独立 `data/civgo/civgo.json`，热重载兼容：新增字段缺失走默认值，旧字段（retrieval/embedding_model）忽略 |

## 3. 现状与改动边界

### 3.1 保留（勿动语义）

| 模块 | 处置 |
|---|---|
| `syncer.go` | 保留整体框架（clone/fetch/merge/退避/状态），`indexAndRecord` 改为 `docmapAndRecord`（§7.4） |
| `chat.go` | 保留单轮 `Complete` 能力，扩展 tools 协议（§9.1~9.2），对外暴露 agent 循环 |
| `config.go` | 保留 Store/热重载/校验框架，增删字段（§5） |
| `service.go` | 保留 OnMessage 入口（@检测/问题清理/限流/并发信号量/分条回复），问答编排改 agent（§12） |
| `mailer` / `watchdog` / admin | 不动，仅 main.go 注入邮件发送 |
| `scanDocs` / `SupportedExt` / `tokenize` | 从被删文件中**迁入** `docmap.go`（list/recall 均需要） |

### 3.2 删除（G1 直接删除）

- `internal/civgo/embed.go`、`embed_test.go`（嵌入客户端 + 自检）
- `internal/civgo/index.go`、`index_test.go`（向量索引 + keyword 降级）
- `internal/civgo/chunk.go`、`chunk_test.go`（分块器）
- `service.go` 中：`embedRecoverLoop`、`index.Search` 调用、`buildDocContext`、`topicHints`/`validTopic`/`cleanTopic`、`loadGeneralContext`
- `config.go` 中：`RetrievalConfig`、`AIConfig.EmbeddingModel`、`NewEmbedClient` 引用
- 运行时清理：`data/civgo/index.json`（启动时检测到旧文件则删除并日志）

### 3.3 改动红线

civgo 包内文件自由增删改；`main.go` 仅允许追加 §11.3 的邮件注入块；
其余既有包、`Dockerfile`、`go.mod`/`go.sum`、`deploy/*` 一律不动。

## 4. 文件清单

```
新增：
internal/civgo/
├── docmap.go          # 文档地图：扫描/大纲提取/JSON 持久化/增量更新（含迁入 scanDocs/SupportedExt/tokenize）
├── docmap_test.go
├── tools.go           # 工具执行器：list_docs / get_doc_outline / read_doc / recall_history + 路径防逃逸
├── tools_test.go
├── agent.go           # agent 循环编排（工具调用循环/预算保护/直答降级）
├── agent_test.go
├── history.go         # 群问答历史环形缓冲 + 持久化 + 关键词召回
├── history_test.go
├── usage.go           # token 滑动窗口计量 + 预警触发（邮件由注入的 sender 发送）
├── usage_test.go
docs/CIVGO_AGENT_DESIGN.md  # 本文档

修改：
internal/civgo/chat.go      # §9：tools 入参、function_call 解析、续轮（input 支持复杂条目）
internal/civgo/config.go    # §5：配置结构变更
internal/civgo/syncer.go    # §7.4：indexAndRecord → docmapAndRecord；state 字段调整
internal/civgo/service.go   # §12：编排改造
main.go                     # §11.3：邮件注入（追加 civgo.Options.SendMail）

删除：
internal/civgo/embed.go / embed_test.go / index.go / index_test.go / chunk.go / chunk_test.go
```

## 5. 配置变更（civgo.json）

### 5.1 新模板

```json
{
  "_template_note": "civgo 社区服务配置。必填 ai.api_key；填好后重启生效。",
  "enabled": true,
  "repo": {
    "url": "https://github.com/AutumnPizazz/civgo.git",
    "branch": "",
    "docs_path": "docs/game_content",
    "sync_interval_sec": 300,
    "clone_shallow": true,
    "sparse_checkout": true
  },
  "ai": {
    "base_url": "https://ai.realseek.wiki/v1",
    "api_key": "",
    "chat_model": "deepseek-v4-flash",
    "chat_timeout_sec": 90,
    "max_output_tokens": 2048
  },
  "agent": {
    "max_tool_calls": 8,
    "max_context_chars": 12000,
    "read_page_lines": 200,
    "read_page_max_chars": 8000
  },
  "history": {
    "enabled": true,
    "max_entries_per_group": 50,
    "persist": true,
    "max_recall_entries": 5
  },
  "usage_alert": {
    "enabled": true,
    "window_minutes": 5,
    "threshold_tokens": 500000,
    "cooldown_minutes": 30,
    "email_to": ""
  },
  "groups": [],
  "rate_limit": {
    "per_user_min": 3,
    "per_group_min": 10,
    "max_concurrent_ai": 2
  }
}
```

### 5.2 Go 类型

```go
type Config struct {
    Enabled    bool             `json:"enabled"`
    Repo       RepoConfig       `json:"repo"`
    AI         AIConfig         `json:"ai"`
    Agent      AgentConfig      `json:"agent"`
    History    HistoryConfig    `json:"history"`
    UsageAlert UsageAlertConfig `json:"usage_alert"`
    Groups     []int64          `json:"groups"`
    RateLimit  RateLimitConfig  `json:"rate_limit"`
}
type AIConfig struct {          // 去掉 EmbeddingModel
    BaseURL, APIKey, ChatModel string
    ChatTimeoutSec, MaxOutputTokens int
}
type AgentConfig struct {
    MaxToolCalls     int `json:"max_tool_calls"`      // 1~30，默认 8
    MaxContextChars  int `json:"max_context_chars"`   // 1000~100000，默认 12000（累计读入上限）
    ReadPageLines    int `json:"read_page_lines"`     // 10~1000，默认 200（read_doc 单次行数）
    ReadPageMaxChars int `json:"read_page_max_chars"` // 1000~30000，默认 8000（read_doc 单次字符上限）
}
type HistoryConfig struct {
    Enabled           bool `json:"enabled"`              // 默认 true
    MaxEntriesPerGroup int `json:"max_entries_per_group"` // 10~200，默认 50
    Persist           bool `json:"persist"`              // 默认 true（data/civgo/history/<group>.jsonl）
    MaxRecallEntries  int `json:"max_recall_entries"`    // 1~10，默认 5
}
type UsageAlertConfig struct {
    Enabled         bool   `json:"enabled"`           // 默认 true
    WindowMinutes   int    `json:"window_minutes"`    // 1~120，默认 5
    ThresholdTokens int64  `json:"threshold_tokens"`  // ≥1000，默认 500000（窗口内 input+output 合计）
    CooldownMinutes int    `json:"cooldown_minutes"`  // 1~1440，默认 30（冷却期内不重复发）
    EmailTo         string `json:"email_to"`          // 空 = 回退 main 注入的默认收件人
}
```

### 5.3 校验与兼容

- 新增字段按 5.2 区间校验（越界返回错误，沿用 fail-safe 热重载）；
- 旧配置文件（含 `retrieval`/`embedding_model`）反序列化时未知字段自然忽略，热重载无缝；
- `agent.max_context_chars` 取代旧 `retrieval.max_context_chars` 语义；
- 启动检测 `data/civgo/index.json` 存在 → 删除 + 日志（G1 清理）。

## 6. 网关 tools 能力验证（第 0 步，决定是否可开工）

### 6.1 自检请求（Service.New 内，超时 10s，一次性）

```
POST {base_url}/responses
{"model": "<chat_model>", "input": [{"role":"user","content":"ping"}],
 "tools": [{"type":"function","name":"ping_tool","description":"自检工具",
            "parameters":{"type":"object","properties":{}}}],
 "tool_choice":"auto", "store": false, "max_output_tokens": 16}
```

### 6.2 判定

| 结果 | 判定 | 处理 |
|---|---|---|
| 200 且 output 含 `function_call` 或正常 message | **支持** | 正常启动 |
| 4xx（400/404/422 等，body 提示 tools 不支持/未知参数） | **不支持** | 模块按 `ErrNotConfigured` 禁用，`slog.Error` 写明「网关不支持 function calling（body 摘要），civgo 问答禁用，请更换支持 tools 的网关」 |
| 网络错误/超时/5xx | 暂不确定 | 告警但**继续启动**（agent 循环中工具失败自然降级直答，§9.5） |

> 验证结论需记入提交说明与日志；若为「不支持」，停下向用户报告，按用户决定更换网关或调整方案（**不得**擅自改做 plan 模式）。

## 7. 文档地图模块（docmap.go）

### 7.1 动机

替代旧向量索引，为 AI 提供「低成本定位 → 精准分页阅读」的能力：
AI 先看目录树与大纲（几千字符），再按行号精准读目标段落，避免盲读大文件烧 token。

### 7.2 数据格式（data/civgo/docmap.json）

```json
{
  "version": 1,
  "built_at": "2026-08-09T10:00:00+08:00",
  "files": {
    "units/archer.md": {
      "bytes": 45210,
      "lines": 980,
      "index_file": false,
      "headings": [ {"line": 1, "level": 1, "text": "弓箭手"},
                    {"line": 40, "level": 2, "text": "技能体系"} ]
    },
    "阅读顺序.md": { "...": "..." }
  }
}
```

- `headings`：`^#{1,6}\s+` 标题行提取（复用旧 chunk.go 的标题正则思路），行号为 1 起；
- `index_file`：文件名含「索引」/「index」标记（沿用 `isIndexFile` 语义，迁入 docmap.go），
  供 `list_docs` 输出时标注「建议优先阅读」；
- 仅支持 `.md`/`.txt`（`SupportedExt`）；跳过隐藏文件/目录（沿用 scanDocs 语义）。

### 7.3 生成与增量更新

- `BuildDocmap(docsDir) (*Docmap, error)`：全量扫描 + 逐文件读内容提大纲；
- 增量：以文件 `bytes+mtime` 或内容 sha256 为指纹（沿用旧哈希思路，用 mtime+bytes 即可，
  减少读盘；不放心可 sha256，量级与旧索引一致，无压力）——**推荐 sha256 复用旧语义**；
- 文件数大时仅变化文件重提大纲；未变化复用旧条目。

### 7.4 同步器改造（syncer.go）

- `indexAndRecord` → `docmapAndRecord`：调 `BuildDocmap`（增量）→ 落盘 → 更新 state；
- `SyncState`：`LastIndexAt/LastIndexSum` → `LastDocmapAt/LastDocmapSum`（如 `files=12 headings=98`）；
- state 旧字段反序列化忽略，`loadState` 兼容；
- 同步日志：`civgo 同步完成 head=xxx files=12 headings=98`。

### 7.5 工具读取

docmap 载入内存（map + RWMutex，结构体 `DocmapStore`），`list_docs`/`get_doc_outline` 纯内存响应；
`read_doc` 走真实文件读取（按行）。同步器更新时原子替换指针（同 Store 模式）。

## 8. 工具集（tools.go）

### 8.1 工具定义（Responses API tools 数组，固定顺序输出）

| 工具 | 参数 | 说明 |
|---|---|---|
| `list_docs` | `path?`（子目录，空 = 根） | 列出目录下子目录与文件（文件名/行数/字节数）；索引类文件标注「⭐建议优先阅读」；返回 ≤ 60 行，超出提示用 path 下钻 |
| `get_doc_outline` | `path`（必填） | 返回文档大纲：标题 + 行号（来自 docmap）；供 read_doc 定位 |
| `read_doc` | `path`、`start_line?`（默认 1）、`max_lines?`（默认 agent.read_page_lines） | 按行读取，带行号前缀；超出文档末尾截断并提示；单次字符上限 `read_page_max_chars` |
| `recall_history` | `query?`（关键词，空 = 最近）、`max_items?`（≤ history.max_recall_entries） | 召回本群历史问答（§10.4） |

### 8.2 JSON Schema（list_docs 示例，其余同风格）

```json
{
  "type": "function", "name": "list_docs",
  "description": "列出文档库目录内容。群友问题通常先调用本工具定位目标文档，再 read_doc 阅读。返回目录树含文件大小信息。",
  "parameters": {
    "type": "object",
    "properties": {"path": {"type": "string", "description": "子目录相对路径，省略则列出根目录"}},
    "additionalProperties": false
  }
}
```

### 8.3 执行规则与安全

- **路径防逃逸**：所有 path 参数 `filepath.Clean` 后必须 `filepath.Rel(docsDir, abs)` 不以 `..` 开头；
  非法路径返回「路径不存在，请用 list_docs 查看可用目录」；
- **上下文预算**（agent 级状态，§9.3）：`read_doc` 实际返回字符数计入累计；超预算 → 拒绝并返回
  「上下文预算已满，请基于已读内容直接作答」；`list_docs`/`get_doc_outline` 输出计入轻量额度
  （预算的 20%）；
- **并发安全**：工具执行器无共享可变状态（docmap 只读指针），单次 agent 循环串行执行；
- 工具执行失败（文件被删等）返回错误文本给 AI，不 panic。

### 8.4 工具描述文案要点（影响 AI 行为）

- 整体引导：中文、口语化、面向游戏顾问场景；建议 AI「先 list_docs 看有什么 → 需要时
  get_doc_outline 定位 → read_doc 读相关段落 → 需要上下文时 recall_history」；
- 索引/阅读顺序类文档标⭐，描述中明示「了解文档结构时优先阅读」；
- 明确「若目录中已有足够信息，不必阅读全文；找不到相关信息就如实说明」。

## 9. Agent 循环（agent.go + chat.go 扩展）

### 9.1 chat.go 协议扩展（Responses API tools）

请求（`Complete` 改造为可选 tools 参数，或新增 `CompleteAgent`；**推荐改造 `Complete` 签名**，
旧调用点仅 service.go 一处）：

```json
{
  "model": "...", "instructions": "...", "store": false,
  "max_output_tokens": 2048,
  "tools": [ ...§8 工具集... ],
  "tool_choice": "auto",
  "input": [
    {"role": "system", "content": "<systemDoc，可为空>"},
    {"role": "user", "content": "<问题>"},
    {"type": "function_call", "call_id": "fc_1", "name": "list_docs", "arguments": "{}"},
    {"type": "function_call_output", "call_id": "fc_1", "output": "<工具结果文本>"}
  ]
}
```

- input 元素类型：`{"role","content"}` 消息 + `{"type","call_id","name","arguments"}` 调用 +
  `{"type","call_id","output"}` 结果（chat.go 的 `[]map[string]string` 改为 `[]map[string]any`）；
- 响应解析：output 数组元素——
  - `{"type":"function_call", ...}`：取 `call_id`（部分网关用 `id`，两者兼容解析）、`name`、`arguments`（JSON 字符串，解析失败按空对象处理并日志）；
  - `{"type":"message"}`：拼接 `output_text`（沿用旧逻辑）；
  - 其余（reasoning 等）跳过；
- 单次响应中可能含**多个** function_call（并发执行后按序回填）。

### 9.2 循环伪代码（agent.go）

```
Run(ctx, q, groupID, userID) (answer string, usage Usage, err error):
    msgs := [system(systemPrompt), user(q)]
    budget := {chars: 0, maxChars: agent.max_context_chars}
    for turn := 0; turn <= agent.max_tool_calls; turn++:     # ≤8 次工具调用
        resp := chat.Complete(ctx, msgs, tools)              # 带工具
        usage += resp.usage
        calls := resp.functionCalls
        if len(calls) == 0:                                  # 直接回答
            return resp.text, usage, nil
        if turn == max_tool_calls:                           # 工具次数耗尽
            注入一条 system:「工具调用已达上限，请基于已读内容直接回答」
            再调一次 Complete（不带 tools）→ 返回
        for c in calls:
            out := tools.Execute(c.name, c.args, groupID, budget)   # §8.3
            msgs += function_call(c); msgs += function_call_output(out)
    return lastText
```

- 每次 `Complete` 超时走配置 `ai.chat_timeout_sec`（沿用）；中途超时返回错误 → 群内友好提示；
- 响应文本为空且无工具调用 → 按旧逻辑报「AI 服务异常」。

### 9.3 保护机制

| 机制 | 配置 | 行为 |
|---|---|---|
| 工具调用次数上限 | `agent.max_tool_calls`（8） | 耗尽后强制直接作答 |
| 累计上下文预算 | `agent.max_context_chars`（12000） | read_doc 超预算拒绝（§8.3） |
| 单页上限 | `read_page_lines`/`read_page_max_chars` | 单次读取有界 |
| 单次请求超时 | `ai.chat_timeout_sec`（90s） | 整体失败友好提示 |
| 总耗时上限 | 无（串行循环，max_tool_calls×超时理论上限内） | 日志记录每轮耗时 |

### 9.4 日志

每轮工具调用 `slog.Info("civgo 工具调用", "tool", name, "args", args截断, "out_chars", n)`；
完成 `slog.Info("civgo 问答完成", group/user/q/in_tok/out_tok/ms/tool_calls)`。

### 9.5 直答降级路径

网关网络抖动导致工具阶段失败（`Complete` 报错）时：若已读过文档，用已读上下文重试一次直答；
否则按旧「无命中」风格处理（AI 仅凭常识/闲聊规则回答，prompt 注明未检索到文档）。
**不 panic、不重试循环**（最多一次重试）。

## 10. 群历史记录模块（history.go）

### 10.1 结构

```go
type HistoryEntry struct {
    Time     time.Time `json:"time"`
    UserID   int64     `json:"user_id"`
    Question string    `json:"question"`   // 截断 100 字
    Answer   string    `json:"answer"`     // 截断 300 字
    Tokens   int       `json:"tokens"`     // 该次问答总消耗（in+out）
}
type HistoryStore struct {  // 按群
    mu     sync.Mutex
    groups map[int64][]HistoryEntry   // 环形缓冲，尾插
    dir    string                     // persist 时 data/civgo/history/
}
```

- 追加时机：**问答完成后**（含直答降级路径）；限流拒绝/工具自检阶段失败不记录；
- 环形上限 `history.max_entries_per_group`（50），满则丢最旧。

### 10.2 持久化

- `persist=true`：每群 `data/civgo/history/<groupID>.jsonl`，追加写 + 启动时全量载入；
- 原子性：单条追加 `\n` 结尾，损坏行跳过（fail-safe，同 state 风格）；
- 文件写入失败仅日志（内存缓冲继续工作）。

### 10.3 决策机制（G3 核心）

- **AI 调用 `recall_history` 即决策**：不调用 = 不带历史（省 token）；调用 = 按参数带；
- 召回结果以 `function_call_output` 注入上下文，AI 自行决定如何使用（引用/忽略）；
- 工具描述写明：只有问题依赖前文（如「它」「上面说的」「那个技能」或追问他人话题）时才调用。

### 10.4 召回逻辑

- 入参 `query` 非空：`tokenize`（迁入的二元分词）命中任一 token 的条目；空：最近条目优先；
- 按时间倒序取 `min(max_items, max_recall_entries)` 条（默认 5）；
- 输出格式：

```
[3 分钟前 用户12345] 问：那个技能怎么解锁？
答：需要 40 级解锁天赋树……
```

- 输出总字符 ≤ 2500（超出截断最旧条目）。

## 11. token 计量与邮件预警（usage.go）

### 11.1 计量

- 全局单例 `UsageMeter`（Service 持有）：
  - 每分钟聚合桶：`map[unixMinute]int64`（in+out 合计），`Add(usage)` 上报；
  - 滑动窗口 = 最近 `window_minutes` 个桶之和（含当前分钟）；
  - 桶数量上限 1440（24h），过期桶惰性清理；
- 上报点：agent 循环每次 `Complete` 返回的 `Usage`（含历史召回产生的输入 token，天然计入）。

### 11.2 触发与邮件

- 每次 `Add` 后检查：窗口和 ≥ `threshold_tokens` 且距上次发送 ≥ `cooldown_minutes` → 触发；
- 触发后重置窗口计数（从当前分钟重新累计，避免连续触发）；
- 邮件内容：

```
【civgo 机器人 token 用量预警】
近 5 分钟消耗约 500,000 tokens（约 N 次 AI 请求），请检查群内是否异常刷问。
消耗 Top 群：<群号> xxx tokens / <群号> xxx
消耗 Top 用户：<QQ> xxx tokens / <QQ> xxx
建议：可调低 rate_limit 限流或 agent.max_tool_calls。
```

- Top 统计：Meter 内维护群/用户维度累计（分钟桶维度太细，用「窗口内累计 map」即可）；
- 发送失败仅日志（下次窗口满足条件重试，冷却期不受发送失败影响？**否**——发送失败不记冷却，下次满足即重发，防漏报）；
- 预警不阻塞问答（异步 goroutine 发送）。

### 11.3 main.go 邮件注入

```go
// civgo.Options 新增：SendMail func(to, subject, body string) error
var sendMail func(string, string, string) error
if e := svc.Effective().Email; e.SMTPHost != "" && e.SMTPPort > 0 && e.SMTPUser != "" && e.SMTPPassword != "" {
    sender := mailer.New(mailer.Config{
        Host: e.SMTPHost, Port: e.SMTPPort, User: e.SMTPUser,
        Password: e.SMTPPassword, From: e.SMTPUser,
    })
    defTo := e.EmailTo // Effective() 已归一化（watchdog.email_to → 系统邮箱收件人，见 state/convert.go）
    sendMail = func(to, subject, body string) error {
        if to == "" { to = defTo }
        return sender.Send(to, subject, body)
    }
}
cv, cerr := civgo.New(civgo.Options{DataDir: dataDir, Manager: manager, SendMail: sendMail})
```

- `SendMail` 为 nil 时 usage_alert 不启用（日志说明「未注入邮件通道，用量预警关闭」）；
- 收件人优先级：`usage_alert.email_to` → 注入的 `defTo`；
- civgo 包内仅依赖 `func(string,string,string) error` 签名，零 import 泄漏。

## 12. service.go 编排改造

### 12.1 新问答流程

```
OnMessage（不变：@检测 → 问题清理/截断 → 限流 → 并发信号量）
  └─ handleQuestion(m, q):
       ctx, cfg 快照
       answer, usage, err := agent.Run(ctx, q, groupID, userID)   // §9.2，内部含工具/历史/预算
       meter.Add(usage)                                            // §11
       history.Append(entry)                                       // §10（问答完成后）
       成功 → sendAnswer(m, answer)（分条回复，无来源/话题尾巴）
       失败 → 友好提示（AI 服务暂时不可用）
```

### 12.2 删除项（service.go）

- `index`/`embed` 字段与 `NewIndexer`/`NewEmbedClient`/`SelfCheck` 调用；
- `embedRecoverLoop`、`buildDocContext`、`topicHints` 及辅助函数、`loadGeneralContext`；
- `Hit`/`Summary` 引用（随 index.go 删除）；
- `systemPrompt` 保留并微调：加入工具行为引导（§8.4 文案并入），仍走 instructions 字段。

### 12.3 保留项

- `sendAnswer` 分条逻辑（默认 3500 字/条，最多 5 条）——**去掉尾部话题提示**（无检索命中来源）；
- 限流、并发信号量、`reply` 快捷回复、热重载。

## 13. 测试要点

| 文件 | 要点 |
|---|---|
| `docmap_test.go` | 大纲提取（标题层级/行号/中文标题）、增量更新（改/增/删文件）、index_file 标记、旧 state 兼容 |
| `tools_test.go` | 路径防逃逸（`../`、绝对路径、不存在文件）、list 下钻、read_doc 分页与截断、预算拒绝 |
| `agent_test.go` | fake ChatCompleter 模拟：直答 / 单工具 / 多工具 / 工具上限强制作答 / 多 function_call 并发回填 / 超时失败降级 / 预算耗尽 |
| `history_test.go` | 环形淘汰、持久化往返、损坏行跳过、关键词召回排序与截断 |
| `usage_test.go` | 滑动窗口滚动、阈值触发、冷却期抑制、窗口重置、Top 统计、发送失败重试 |
| `chat_test.go` | tools 请求体、function_call/function_call_output 续轮解析、call_id 兼容（id/call_id）、arguments 坏 JSON |
| `config_test.go` | 新字段默认值、区间校验、旧配置（含 retrieval）热重载兼容 |
| `service_test.go` | 编排改造后的消息流（fake agent + fake meter + fake history） |
| `smoke_test.go` | 真实网关冒烟：提问 → 工具调用 → 回答（依赖网关可用，失败跳过） |

## 14. 实施顺序与提交规划（提交留在 dev_civgo，不 push）

| 提交 | 内容 |
|---|---|
| `cg0.1.1-添加agent化检索改造计划书` | 本文档（本轮） |
| `cg0.1.2-验证网关function calling能力` | §6 自检验证结论（验证代码可并入后续提交，本提交只记录结论与必要接线） |
| `cg0.1.3-文档地图模块替代向量索引` | docmap.go + syncer 改造 + 删 chunk.go（scanDocs 等迁入）+ config 新字段骨架 |
| `cg0.1.4-agent循环与文档工具` | tools.go + agent.go + chat.go 扩展 + service.go 接入（此步起问答走 agent） |
| `cg0.1.5-群历史记录与AI决策召回` | history.go + recall_history 工具接入 |
| `cg0.1.6-token用量计量与邮件预警` | usage.go + main.go 邮件注入 |
| `cg0.1.7-删除向量检索残留` | 删 embed.go/index.go 及测试、旧配置字段、index.json 清理、service.go 清理收尾 |
| `cg0.1.8-测试补全与冒烟` | 全量 `go test ./...` + `go vet ./...` 绿 + 真实文档冒烟 + CIVGO_DESIGN.md 标注废弃章节 |

> 说明：cg0.1.3~cg0.1.7 提交间允许临时编译不过（分步迁移），但**每步对应测试通过**；
> cg0.1.8 结束时整体必须全绿。

## 15. 风险与对策

| 风险 | 对策 |
|---|---|
| 网关不支持 function calling | §6 启动自检：明确 4xx 则模块禁用并报告用户，不擅自降级方案 |
| deepseek-v4-flash 工具调用质量差（乱调/不调） | 工具描述引导 + max_tool_calls 上限 + 预算保护；日志观察 tool_calls 分布，必要时调参 |
| agent 多轮 token 消耗高于向量检索 | 预算/页上限双约束 + usage_alert 预警 + 既有 rate_limit 限流 |
| 大文档盲读 | docmap 大纲先行 + read_doc 分页（200 行/页） |
| 历史召回引入噪音/隐私 | 仅本群召回、条数/字数截断、`history.enabled` 可整体关闭 |
| 同步与问答并发读 docmap | 原子指针替换（同 Store 模式），工具执行串行于 agent 循环内 |

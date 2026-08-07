# Civgo 社区服务实施规格书（dev_civgo 分支专项）

> **给实现者的开场白**：本文档是一份可直接照做的实施规格。开新窗口时：
> 1. 通读本文档（重点：§2 决策、§3 接入点、§4 文件清单、§5~§10 各模块规格、§12 实现顺序）；
> 2. 前置阅读仓库文件：`main.go`（接入点）、`internal/onebot/manager.go`（`On`/`SendGroupMsgAt`）、
>    `internal/onebot/events.go`（`GroupMessage` 结构）、`internal/bot/bot.go`（消息处理参考）；
> 3. 按 §12 顺序实现，每步跑通对应测试；全部完成后 `go test ./...` + `go vet ./...` 全绿；
> 4. 只允许改 `main.go` 与 `Dockerfile` 两个既有文件（各十余行/一行），其余一律新增文件；
> 5. 所有提交留在 `dev_civgo` 分支工作区，**不主动 commit/push**，由用户审阅后自行提交。

---

## 1. 背景与目标

qqbot（Go + OneBot v11/NapCat QQ 群管理机器人，已公网部署，域名 `qqbot.civgo.net`）
特化为 **civgo 游戏社区服务机器人**：

1. **文档自动同步**：检测公开仓库 `https://github.com/AutumnPizazz/civgo.git` 中
   `docs/game_content/` 目录变化，第一时间拉取最新版到云服务器本地。
2. **AI 问答**：群友 @ 机器人提问 → 以 `docs/game_content` 为知识源做
   **分块 + 向量检索** → 调第三方 AI（OpenAI 兼容网关 `https://ai.realseek.wiki/v1`，
   模型 `deepseek-v4-flash`）→ 答案发回群。

## 2. 已确认决策（与用户逐项确认，勿改）

| # | 决策点 | 结论 |
|---|---|---|
| D1 | civgo 仓库 | 公开仓库 `https://github.com/AutumnPizazz/civgo.git`，匿名访问，默认分支探测 `origin/HEAD` |
| D2 | 变化检测与拉取 | **git 轮询**：定时 `fetch` → 检测 `docs/game_content` 提交变化 → `pull --ff-only`；首次 `clone`（shallow + sparse） |
| D3 | AI 知识检索 | **分块 + 向量检索**（余弦相似度 top-k），纯 Go 自研轻量索引，零第三方依赖 |
| D4 | AI 对话协议 | **Responses API：`POST {base}/responses`**（**不是** `/v1/chat/completions`；deepseek-v4-flash 在网关挂 responses 路由） |
| D5 | 嵌入协议 | `POST {base}/embeddings`（OpenAI 兼容），启动自检，失败自动降级 `keyword` 检索 |
| D6 | 新配置存放 | 独立配置文件 `data/civgo/civgo.json`，不触碰 control.json / config.yaml 体系 |
| D7 | 分支边界 | 新代码全部在 `dev_civgo`；既有文件仅允许改 `main.go`（接入）与 `Dockerfile`（装 git，见 §3.3）；`go.mod`/`go.sum` 零变更 |
| D8 | 数据目录 | 全部落在已挂载的 `./data` 卷（`data/civgo/`），**不改 docker-compose** |

## 3. 现状、接入点与必要改动

### 3.1 复用既有能力（勿重复造轮子）

| 既有能力 | 位置 | civgo 如何使用 |
|---|---|---|
| 多 handler 并行分发 | `onebot.Manager.On` + `client.dispatchEvent`（每 handler 独立 goroutine） | 独立注册 `message` handler，**bot.go 零改动** |
| @ 回复发送 + 每群 800ms 串行限速防风控 | `Manager.SendGroupMsgAt(groupID, userID, text)`（`groupSender` 实现） | 直接复用 |
| 主密钥/配置体系 | `state` 包 | **不碰**（civgo 独立配置，明文 api_key 仅存服务器卷内） |
| 日志 | `log/slog` | 统一使用，日志统一前缀 `civgo`，便于 `docker logs qqbot \| grep civgo` |

### 3.2 `main.go` 接入（唯一代码改动，~12 行）

`runManaged` 的 `startComponents`（`sync.Once` 内、`b.Start()` 之后）追加：

```go
// civgo 社区服务：独立配置（data/civgo/civgo.json），未配置/未启用时零副作用
if cv, err := civgo.New(civgo.Options{DataDir: dataDir, Manager: manager}); err != nil {
    if !errors.Is(err, civgo.ErrNotConfigured) {
        slog.Error("civgo 模块初始化失败", "err", err)
    }
} else {
    cv.Start(ctx) // 内部：注册 message handler + 启动同步/重载 goroutine
}
```

语义：`civgo.json` 缺失时 `New` 生成默认模板文件并返回 `ErrNotConfigured`
（main.go 静默跳过）；api_key 为空等校验失败也走 `ErrNotConfigured`。
`import` 需新增 `"qqbot/internal/civgo"` 与 `"errors"`（**已确认 main.go 当前无 errors import**，两者都要加）。

### 3.3 `Dockerfile` 改动（唯一部署改动，1 行）

运行阶段 `debian:trixie-slim` **没有 git 二进制**，而同步器用 `os/exec` 调 git。在既有 apt 行追加：

```dockerfile
RUN sed -i 's|deb.debian.org|mirrors.aliyun.com|g' /etc/apt/sources.list.d/debian.sources \
    && apt-get update \
    && apt-get install -y --no-install-recommends ca-certificates git \
    && rm -rf /var/lib/apt/lists/*
```

代码侧用 `exec.LookPath("git")` 探测：找不到 git 时 civgo 模块禁用并输出
明确日志（本机开发与容器行为一致，不 panic）。

### 3.4 不改动清单（约束红线）

`internal/bot`、`internal/rules`、`internal/admin`、`internal/state`、`internal/onebot`、
`internal/config`、`internal/mailer`、`internal/fsutil`、`internal/watchdog`、
`deploy/*`、`go.mod`、`go.sum`、`main.go` 以外的任何既有文件 —— **一律不动**。

## 4. 文件清单

```
新增：
internal/civgo/
├── config.go        # Config 结构/默认模板/加载/校验/热重载（atomic.Pointer）
├── config_test.go
├── chunk.go         # 文档分块器（标题 + 长度 + 重叠）
├── chunk_test.go
├── embed.go         # embeddings 客户端（/v1/embeddings）+ 启动自检
├── embed_test.go
├── chat.go          # Responses API 客户端（/v1/responses）
├── chat_test.go
├── index.go         # 向量索引：余弦检索/持久化/哈希缓存/keyword 降级
├── index_test.go
├── syncer.go        # git 轮询同步器（clone/fetch/检测/pull/状态/退避）
├── syncer_test.go
├── service.go       # Service 聚合：OnMessage 编排/限流/Sender 接口/回复分条
├── service_test.go
docs/CIVGO_DESIGN.md # 本文档
修改（仅两处）：
main.go              # §3.2 接入块
Dockerfile           # §3.3 apt 追加 git
```

## 5. 配置模块（config.go）

### 5.1 配置结构（civgo.json，模块首次启动自动生成模板）

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
    "embedding_model": "bge-m3",
    "chat_timeout_sec": 90,
    "embed_timeout_sec": 30,
    "max_output_tokens": 2048
  },
  "retrieval": {
    "mode": "vector",
    "top_k": 6,
    "chunk_size": 800,
    "chunk_overlap": 100,
    "min_score": 0.25,
    "max_context_chars": 12000
  },
  "groups": [],
  "rate_limit": {
    "per_user_min": 3,
    "per_group_min": 10,
    "max_concurrent_ai": 2
  }
}
```

### 5.2 Go 类型与函数

```go
type Config struct {
    Enabled   bool            `json:"enabled"`
    Repo      RepoConfig      `json:"repo"`
    AI        AIConfig        `json:"ai"`
    Retrieval RetrievalConfig `json:"retrieval"`
    Groups    []int64         `json:"groups"`
    RateLimit RateLimitConfig `json:"rate_limit"`
}
type RepoConfig struct {
    URL             string `json:"url"`
    Branch          string `json:"branch"`           // 空 = 探测 origin/HEAD
    DocsPath        string `json:"docs_path"`
    SyncIntervalSec int    `json:"sync_interval_sec"`
    CloneShallow    bool   `json:"clone_shallow"`
    SparseCheckout  bool   `json:"sparse_checkout"`
}
type AIConfig struct {
    BaseURL         string `json:"base_url"`
    APIKey          string `json:"api_key"`
    ChatModel       string `json:"chat_model"`
    EmbeddingModel  string `json:"embedding_model"`
    ChatTimeoutSec  int    `json:"chat_timeout_sec"`
    EmbedTimeoutSec int    `json:"embed_timeout_sec"`
    MaxOutputTokens int    `json:"max_output_tokens"`
}
type RetrievalConfig struct {
    Mode            string  `json:"mode"` // "vector"(默认) | "keyword"
    TopK            int     `json:"top_k"`
    ChunkSize       int     `json:"chunk_size"`
    ChunkOverlap    int     `json:"chunk_overlap"`
    MinScore        float64 `json:"min_score"`
    MaxContextChars int     `json:"max_context_chars"`
}
type RateLimitConfig struct {
    PerUserMin      int `json:"per_user_min"`
    PerGroupMin     int `json:"per_group_min"`
    MaxConcurrentAI int `json:"max_concurrent_ai"`
}

var ErrNotConfigured = errors.New("civgo 未配置（缺失配置/缺少 api_key/缺少 git），模块禁用")

func DefaultConfig() *Config                     // 模板默认值（与上方 JSON 一致）
func Load(path string) (*Config, error)          // 不存在 → 写模板文件 + 返回 ErrNotConfigured
func (c *Config) Validate() error                // 校验规则见 5.3；api_key 空 → ErrNotConfigured
type Store struct{ cfgPtr atomic.Pointer[Config]; path string; mtime time.Time }
func NewStore(path string) (*Store, error)       // 首载 + 记录 mtime
func (s *Store) Get() *Config                    // 原子读
func (s *Store) ReloadIfChanged()                // stat mtime 变化 → 重载；校验失败保留旧配置 + 日志
```

### 5.3 校验规则（Validate）

| 字段 | 规则 |
|---|---|
| repo.url | 非空；以 `.git` 或 `http(s)://` 开头均可，非空即合法（错误在同步时暴露并告警） |
| repo.docs_path | 非空；要求相对路径（不以 `/` 开头），防路径逃逸 |
| repo.sync_interval_sec | 30 ~ 3600，默认 300 |
| ai.base_url | 非空；要求 `http(s)://` 前缀；默认 `https://ai.realseek.wiki/v1`（**含 /v1**，请求拼 `base_url + "/responses"`、`base_url + "/embeddings"`） |
| ai.api_key | 非空，否则 **ErrNotConfigured** |
| ai.chat_model / embedding_model | 非空 |
| ai.chat_timeout_sec | 10 ~ 600；embed_timeout_sec 5 ~ 120 |
| ai.max_output_tokens | 100 ~ 8192 |
| retrieval.mode | 仅 `vector`/`keyword` |
| retrieval.top_k | 1 ~ 20 |
| retrieval.chunk_size | 200 ~ 2000；chunk_overlap < chunk_size |
| retrieval.min_score | 0 ~ 1 |
| retrieval.max_context_chars | 1000 ~ 100000 |
| rate_limit.* | 均 ≥ 1；per_user_min ≤ per_group_min |
| groups | 可空（空 = 任何群都不启用问答；同步器仍工作） |

### 5.4 热重载

- 同步循环每轮末尾调用 `ReloadIfChanged()`（默认 5 分钟粒度，够用）；
- 额外监听 `SIGHUP`：service 里起一个 goroutine 用 `signal.Notify`，收到即重载；
- 重载仅原子替换配置指针；**不中断**正在进行的 AI 请求（请求开始时已快照配置）；
- 同步器/限流器读配置也走 `Store.Get()` 快照，参数即时生效（如限流值取请求开始时的配置）。

## 6. 文档同步器（syncer.go）

### 6.1 目录与状态

- 仓库镜像：`<dataDir>/civgo/repo/`（git 工作树，sparse checkout 后仅含 docs 目录）
- 状态文件：`<dataDir>/civgo/state.json`

```go
type SyncState struct {
    LastHead     string    `json:"last_head"`     // 上次已同步的远端 commit
    LastSyncAt   time.Time `json:"last_sync_at"`
    LastError    string    `json:"last_error,omitempty"`
    FailCount    int       `json:"fail_count"`
    LastIndexAt  time.Time `json:"last_index_at"`
    LastIndexSum string    `json:"last_index_sum"` // 例如 "files=12 chunks=342"
}

type Syncer struct {
    store   *Store
    index   *Indexer
    repoDir string
    statePath string
    mu      sync.Mutex // 串行化 syncOnce（防重载并发触发）
}
func NewSyncer(store *Store, index *Indexer, dataDir string) *Syncer
func (s *Syncer) Run(ctx context.Context)   // 首次同步 → ticker(当前间隔) 循环 → 每轮后 ReloadIfChanged
func (s *Syncer) syncOnce(ctx context.Context) error
func (s *Syncer) git(args ...string) (string, string, error) // 包装 exec.CommandContext("git", args...)，记录日志
```

### 6.2 git 命令序列（syncOnce 完整流程）

```
① 探测 git 二进制：exec.LookPath("git")；缺失 → ErrNotConfigured（模块禁用）
② 分支探测（仅 branch 为空时）：git ls-remote --symref <url> HEAD
   解析输出行 "ref: refs/heads/<name> HEAD" → 取 <name>；失败则用 "main"
③ 首次（repo/.git 不存在）：
   git clone --depth 1 --branch <branch> <url> <repoDir>
   （CloneShallow=false 时去掉 --depth 1）
   SparseCheckout=true 时追加：
     git -C <repoDir> sparse-checkout init --cone
     git -C <repoDir> sparse-checkout set <docs_path>
   失败 → 记录 state（error+fail_count）→ 返回错误（下一轮重试）
④ 轮询（repo 已存在）：
   git -C <repoDir> fetch origin <branch>            // 失败 → 退避（见 6.4）→ 返回
   head := git -C <repoDir> rev-parse FETCH_HEAD     // 取 hash
   若 state.LastHead == head → 无变化，直接返回（轻量轮询）
⑤ 变化检测（双保险）：git -C <repoDir> log --format=%H HEAD..FETCH_HEAD -- <docs_path>
   输出非空 → docs 有变化；为空 → 仅记录 LastHead=head 并返回
   （sparse checkout 下 log 仍正常：commit 元数据完整，仅 blob 按需拉取）
⑥ 更新：git -C <repoDir> merge --ff-only FETCH_HEAD
   失败（本地被意外改动等）→ git -C <repoDir> reset --hard HEAD 后重试一次；
   仍失败 → 告警 + 返回错误
⑦ 更新 state：LastHead=head、LastSyncAt=now、FailCount=0
⑧ 索引重建：index.RebuildChanged()（内部按文件哈希增量，见 §8.4）
⑨ 日志：slog.Info("civgo 同步完成", "head", head[:8], "changed", n, "summary", sum)
```

### 6.3 同步日志摘要

变更统计来自索引重建返回值：`{added, changed, removed int, chunks int}`；
无变化时输出 `slog.Debug`（避免刷屏），有变化或出错输出 `slog.Info/Warn`。

### 6.4 失败退避

- `FailCount` 连续失败 ≥ 3：同步间隔翻倍（`interval × 2^min(fail-2, 4)`，上限 3600s）；
- 成功后立即恢复配置间隔；间隔变化写入日志。

### 6.5 测试要点（syncer_test.go）

用 `git init --bare` 建本地 fixture 仓库（测试内 `t.TempDir()`）：
`TestCloneFirstTime`、`TestDetectNewCommit`（追加提交后 syncOnce 拉取并更新 LastHead）、
`TestNoChangeSkipped`、`TestMergeFFOnlyConflict`（本地篡改文件 → reset 恢复）、
`TestFetchFailureBackoff`、`TestStatePersistReload`、`TestBranchAutoDetect`。

## 7. 文档分块器（chunk.go）

### 7.1 类型

```go
type Chunk struct {
    File    string `json:"file"`    // 相对 docs_path 的路径，如 "units/archer.md"
    Heading string `json:"heading"` // 所属最近标题（无则空）
    Index   int    `json:"index"`   // 文件内块序号（从 0）
    Text    string `json:"text"`
}

// ChunkFile 切分单个文件；返回 nil 表示跳过（非支持类型/空文件）。
func ChunkFile(relPath string, content string, cfg RetrievalConfig) []Chunk
func SupportedExt(name string) bool  // ".md", ".txt"（其余扩展名跳过，syncer 日志计数）
```

### 7.2 算法（按行流式，单遍）

```
支持扩展名判断（.md/.txt，大小写不敏感）；否则返回 nil。
heading := ""；cur := strings.Builder{}；chunks := []Chunk{}
flush()：cur 非空（TrimSpace 后 ≥ 1 字符）→ 追加 Chunk{File, Heading, Index: len(chunks), Text: cur}；cur 重置
逐行处理（保留原始换行语义，行尾 \n 统一）：
  line = TrimRight(line, "\r")
  if 标题行（regexp ^#{1,6}\s+）：
      flush()
      heading = TrimSpace(TrimPrefix(line, "#"))（去掉 # 后 trim）
      cur.WriteString(line + "\n")（标题本身作为块首行，保证标题块自含）
  else：
      cur.WriteString(line + "\n")
  若 cur 的 rune 数 ≥ chunk_size → flush()
流结束 → flush()
```

重叠处理：为简单可靠，**不做跨块字符重叠**（`chunk_overlap` 字段保留但首版仅用于
预留）。理由：标题先行切分已避免大部分语义断裂；段落块 800 字切分边界落在行首
（按行追加天然整行切块）。若后续发现切分质量问题，再实现尾部 overlap 回补。
（**实现时按此简化，勿过度设计**；`ChunkOverlap` 仍入配置供未来使用。）

边界：过滤纯空白/纯符号块（TrimSpace 为空）；单行超长（> chunk_size）不硬切，
整体成块（文档内罕见，避免切断表格/代码块）。

## 8. 嵌入与向量索引（embed.go / index.go）

### 8.1 embeddings 客户端（embed.go）

```go
type EmbedClient struct {
    baseURL, apiKey, model string
    http    *http.Client    // Timeout 由配置
    batch   int             // 固定 32
}
func NewEmbedClient(cfg AIConfig) *EmbedClient
func (c *EmbedClient) EmbedTexts(ctx context.Context, texts []string) ([][]float32, error)
func (c *EmbedClient) SelfCheck(ctx context.Context) error
```

请求（每次一批，最多 32 条）：

```
POST {base_url}/embeddings
Authorization: Bearer <api_key>
Content-Type: application/json
{"model": "<embedding_model>", "input": ["<text1>", "<text2>", ...]}

响应 200：
{"data": [{"embedding": [0.0012, -0.0034, ...], "index": 0}, ...], "model": "..."}
解析：按 data[].index 归位（网关可能乱序）；每向量 float64 → []float32。
非 2xx：读取 body 前 500 字符，返回 fmt.Errorf("embeddings HTTP %d: %s", code, body)
超时/网络错误：包装错误（含耗时）。
```

`SelfCheck`：嵌入 `["ping"]`，成功即返回 nil；失败返回带 body 的错误
（供 service 决定降级与日志：`civgo 嵌入自检失败: ...（将降级 keyword 检索）`）。

### 8.2 索引结构（index.go）

```go
type IndexEntry struct {
    Chunk Chunk    `json:"chunk"`
    Vec   []float32 `json:"-"`   // 内存态，L2 归一化
    VecB64 string  `json:"vec_b64"` // 持久化：math.Float32bits 打包为 []byte 后 base64
}
type IndexFile struct {
    Version   int               `json:"version"`   // 1
    Entries   []IndexEntry      `json:"entries"`
    FileHashes map[string]string `json:"file_hashes"` // relPath → sha256(内容)
    BuiltAt   time.Time         `json:"built_at"`
}
type Hit struct { Chunk Chunk; Score float64 }

type Indexer struct {
    mu      sync.RWMutex
    entries []IndexEntry
    hashes  map[string]string
    embed   *EmbedClient
    mode    string        // 取自配置（vector/keyword），降级时置 keyword
    path    string        // <dataDir>/civgo/index.json
}
func NewIndexer(store *Store, embed *EmbedClient, path string) *Indexer
func (ix *Indexer) Load() error          // 启动载入；文件缺失/损坏 → 日志 + 空索引（由首次同步重建）
func (ix *Indexer) Save() error          // 原子写（临时文件 + rename；复用 fsutil 模式但零依赖，自实现）
func (ix *Indexer) RebuildChanged() (Summary, error) // §8.4
func (ix *Indexer) RebuildAll() error    // 全量重建（自检/兜底用）
func (ix *Indexer) Search(ctx context.Context, query string) ([]Hit, error) // vector 或 keyword 按 mode
func (ix *Indexer) KeywordSearch(query string, topK int) []Hit
```

### 8.3 向量检索

- 查询：`embed.EmbedTexts([query])` → 归一化 → 与全部条目点积（已归一化，点积=余弦）
- 排序取 `top_k`，过滤 `score < min_score`；空索引 → 空结果
- 归一化：`v / sqrt(Σvᵢ²)`，零向量（全 0，异常响应）跳过该条目并日志计数
- 查询嵌入失败（网关异常）→ 返回错误，由 service 决定降级 keyword 或提示不可用
- 复杂度：数千条目 × 768 维点积 ≈ 毫秒级，无需 ANN

### 8.4 增量重建（RebuildChanged）

```
1. 扫描 repo/<docs_path> 递归所有文件（filepath.WalkDir）
2. 过滤：仅 SupportedExt；忽略隐藏文件（. 开头）
3. 每个文件计算 sha256；与 FileHashes 比对：
   - 哈希相同 → 跳过（复用旧向量）
   - 新文件/内容变化 → 读内容 → ChunkFile 分块 → 收集待嵌入
   - 本地已删除 → 记入 removed
4. 待嵌入文本分批（≤32/批）调 EmbedTexts → 构造新条目
5. 重建 entries 切片（未变条目 + 新条目，顺序无关）+ 更新 FileHashes
6. Save()（原子写）；返回 Summary{Added, Changed, Removed, Chunks}
7. 任一步失败（如嵌入批量 5xx）→ 保留旧索引与旧哈希，返回错误（下次重试）
8. 空文档目录 → 空索引 + Warn 日志（可能是 docs_path 配错）
```

### 8.5 keyword 降级（retrieval.mode=keyword 或嵌入自检失败自动切换）

```
分词：unicode 字母数字串整体成词；CJK 连续汉字按 2-gram 切（如 "弩炮台" → "弩炮","炮台"）
词频：query 词 → 在块文本中的出现次数加权；文件名/Heading 命中词 +3 权重
打分：Σ(词权重 × 命中块内次数)；取 top_k
实现位置：index.go 内 ~60 行；无外部依赖
```

## 9. AI 对话模块（chat.go）— Responses API

### 9.1 客户端

```go
type ChatClient struct {
    baseURL, apiKey, model string
    timeout time.Duration
    maxTokens int
    http *http.Client
}
func NewChatClient(cfg AIConfig) *ChatClient
type Usage struct{ InputTokens, OutputTokens int }
// Complete 调用 /v1/responses；返回助手文本与用量。
func (c *ChatClient) Complete(ctx context.Context, instructions, systemDoc, userText string) (string, Usage, error)
```

### 9.2 请求（精确格式）

```
POST {base_url}/responses          # base_url 默认 https://ai.realseek.wiki/v1
Authorization: Bearer <api_key>
Content-Type: application/json

{
  "model": "deepseek-v4-flash",
  "instructions": "<固定系统提示：角色设定与回答规则，见 §9.4>",
  "input": [
    {"role": "system", "content": "<知识源：检索到的文档块，见 §10.4>"},
    {"role": "user",   "content": "<群友问题>"}
  ],
  "max_output_tokens": 2048,
  "store": false
}
```

**注意：不是 chat/completions**（deepseek-v4-flash 在网关挂 responses 路由；
chat/completions 会 404/模型不存在）。`instructions` 与 input 中的 system 消息
职责划分：instructions 放**稳定**的角色/规则文本；input system 放**每次变化的**
检索文档上下文（避免每次重传固定文本，也便于调试）。

### 9.3 响应解析

```
200：
{
  "id": "resp_...",
  "output": [
    {"type": "reasoning", ...},                      // 可能存在的思考项，跳过
    {"type": "message", "role": "assistant",
     "content": [{"type": "output_text", "text": "答案..."}]}
  ],
  "usage": {"input_tokens": 123, "output_tokens": 45}
}
解析规则：
- 遍历 output：仅取 type=="message" 的条目；其 content 中仅取 type=="output_text"
  （多个则拼接，用 \n 分隔）；其余类型（reasoning/function_call 等）一律跳过
- 没有任何 output_text → 返回错误 "responses 输出为空"
- 非 2xx：body 前 500 字符进错误（网关错误格式 {"error":{"message":...}}，解析失败
  则原文截断即可）
- 超时：ctx 由调用方控制（chat_timeout_sec），错误文案含耗时
```

### 9.4 固定系统提示（instructions 内容，代码内常量）

```
你是 civgo 游戏社区的游戏问答助手。你的知识来源是社区提供的游戏文档。
回答规则：
1. 只依据提供给你的文档片段回答问题；文档中没有的信息，明确说"文档中未找到"，
   不要编造。
2. 引用文档时在句末标注来源文件名（如 (archer.md)）。
3. 回答使用简体中文，简洁、直接、面向 QQ 群聊场景；控制在 500 字内。
4. 与游戏无关的问题（闲聊、编程、其他游戏等），礼貌说明"我是 civgo 游戏助手，
   只回答游戏内容相关的问题"。
```

## 10. 问答服务（service.go）

### 10.1 类型与生命周期

```go
// Sender 抽象：onebot.Manager 天然实现；测试注入 fake。
type Sender interface { SendGroupMsgAt(groupID, userID int64, text string) error }

type Options struct {
    DataDir string
    Manager *onebot.Manager // 实现 Sender；New 内断言
}
type Service struct {
    store  *Store
    index  *Indexer
    chat   *ChatClient
    embed  *EmbedClient
    sender Sender
    rl     *rateLimiter
    sem    chan struct{}        // 并发信号量（MaxConcurrentAI）
    dataDir string
}
func New(opts Options) (*Service, error)
// New 流程：NewStore（ErrNotConfigured 冒泡）→ exec.LookPath("git") 缺失 → ErrNotConfigured
// → NewEmbedClient + SelfCheck（失败：日志 + Retrieval.Mode 强制 "keyword"，不阻断）
// → NewIndexer + Load() → NewChatClient
func (s *Service) Start(ctx context.Context)
// Start：1) 若索引为空 → 触发首次 RebuildAll（同步器会做，这里仅兜底幂等）
// 2) mgr.On("message", s.OnMessage) 3) go NewSyncer(...).Run(ctx) 4) SIGHUP 重载 goroutine
func (s *Service) OnMessage(raw json.RawMessage) error   // §10.2
func (s *Service) handleQuestion(ctx context.Context, m onebot.GroupMessage, q string) // §10.3
```

### 10.2 OnMessage 流程（逐步）

```
1. unmarshal onebot.GroupMessage；失败 → 返回 err（与 bot 同风格）
2. m.MessageType != "group" 或 m.UserID == m.SelfID → return nil
3. cfg := s.store.Get()；!cfg.Enabled → nil
4. cfg.Groups 不含 m.GroupID → nil
5. @ 检测：atRe = regexp.MustCompile(`\[CQ:at,qq=(\d+|all)\]`)
   matches := atRe.FindAllStringSubmatch(m.RawMessage, -1)
   无匹配 或 无 qq==strconv(SelfID) 且无 "all" → nil（未 @ 机器人，忽略）
6. 问题文本：q := cqCodeRe.ReplaceAllString(m.RawMessage, "")（剔除所有 CQ 码）
   → strings.TrimSpace → 去除行内前导 @昵称残留（按文本截取到第一个中文字符前的
   空白/标点）→ TrimSpace
   长度检查（rune）：< 2 → nil（纯 @ 不回答）；> 300 → 截断到 300 并追加 "…"
7. 限流（§10.5）：rl.Allow(m.GroupID, m.UserID) == false → 发提示
   "⏳ 提问太频繁啦，稍等一会儿再试吧"（该提示不再占限流额度，但受群发送队列限速）→ nil
8. select s.sem（非阻塞；满 → 发 "🤖 当前请求较多，请稍后再试"）→ defer 释放
9. go 协程执行 handleQuestion（不阻塞事件分发；每消息独立 goroutine 与 bot 并行安全）
```

### 10.3 handleQuestion（检索 → 拼上下文 → 调 AI → 回复）

```
1. 检索：
   hits, err := s.index.Search(ctx, q)
   err（嵌入失败且 mode=vector）→ 尝试 KeywordSearch 兜底；仍失败 → 回复
     "🤖 知识检索服务暂时不可用，请稍后再试" → return
2. hits 为空（含全被 min_score 过滤）→ 回复
     "📚 知识库中暂未找到与「<q 前 30 字>」相关的内容。换个说法试试？
      也可以等游戏文档更新后再问～" → return
3. 拼知识源文本（max_context_chars 上限）：
   按 score 降序取块；每块 "【来源: <file>」<heading>】\n<text>\n\n"；
   累计 rune 数 ≥ max_context_chars 即停止（保证不超模型上下文）
4. instructions := systemPrompt（§9.4 常量）
   answer, usage, err := s.chat.Complete(ctx, instructions, docContext, q)
5. 回复内容组装：
   reply := "[CQ:at,qq=<uid>] " + "\n" + answer
   末尾附来源（去重、最多 5 个）："\n\n📄 " + strings.Join(files, "、")
6. 长度分条（§10.6）→ 逐条 s.sender.SendGroupMsgAt(gid, uid, part)
7. 旁路日志：
   slog.Info("civgo 问答完成", "group", gid, "user", uid,
     "q", truncate(q, 50), "hits", len(hits), "in_tok", u.InputTokens,
     "out_tok", u.OutputTokens, "ms", elapsed)
8. 错误统一：chat 超时/5xx → 回复 "🤖 AI 服务暂时不可用（已记录），请稍后再试"；
   不重试（防放大）；同样记 Warn 日志
```

### 10.4 知识源文本格式（input system 消息）

```
以下是 civgo 游戏文档（检索自 docs/game_content，按相关度排序）：
每个片段以【来源: 文件名】开头，回答时如需引用请标注对应文件名。

【来源: units/archer.md】
弓手：远程单位，射程 2 格，攻击力 5...
（…按 score 顺序继续…）
```

### 10.5 限流（rateLimiter，令牌桶）

```go
type bucket struct { tokens float64; last time.Time }
type rateLimiter struct {
    mu sync.Mutex
    users  map[int64]*bucket   // key: userID
    groups map[int64]*bucket   // key: groupID
}
func newRateLimiter() *rateLimiter
// Allow：rate = perMin/60.0；tokens = min(cap=perMin, tokens + dt*rate)；
// tokens < 1 → 拒绝；否则 tokens -= 1 放行。
// 并发：每 60s 清理一次 10 分钟无活动的桶（在 Allow 内惰性抽样清理即可，勿起额外 goroutine）
```
- 用户维度用 `per_user_min`，群维度用 `per_group_min`，两个桶都要过；
- 并发维度：`s.sem` 信号量（`max_concurrent_ai`），非阻塞获取。

### 10.6 回复分条规则

- 单条安全上限 `maxLen = 3500` rune（QQ 群消息 + CQ at 码余量；超出触发风控/截断）；
- answer ≤ 3500 → 一条发出（含 at 前缀与来源尾注，组装后整体 ≤ 3500 再校验）；
- 超长 → 按 `\n` 切段，贪心合并段落到 ≤ 3500；首条带 at 前缀，末条带来源尾注；
  中间条直接发文本；总条数 > 5 → 只发前 5 条，最后补一条 "…内容过长，其余已省略"；
- 发送顺序即调用顺序（groupSender 每群 800ms 串行，天然有序）。

### 10.7 Sender 接口理由

`onebot.Manager` 直接满足接口，测试用 fake（记录调用序列）验证：
@ 格式、分条数量与顺序、限流提示、错误提示文案。

## 11. 降级与容错总表

| 故障 | 行为 |
|---|---|
| civgo.json 缺失 | 生成模板 + ErrNotConfigured（main 静默，日志一条） |
| api_key 为空 | 校验失败 → ErrNotConfigured |
| 容器/机器无 git | LookPath 失败 → ErrNotConfigured |
| embeddings 自检失败 | 日志告警 + mode 强制 keyword；每轮同步后重试自检（自检通过回切 vector） |
| 同步 fetch/pull 失败 | 保留旧索引继续问答；退避轮询；日志 Warn |
| 索引损坏/缺失 | Load 失败 → 空索引 → 首次同步全量重建；期间问答返回"知识库未就绪" |
| 检索无命中 | 友好提示换说法，不调 AI（省钱） |
| chat 超时/5xx | 友好提示 + Warn 日志；不重试 |
| 长回答 | 分条 ≤5 条 + 省略提示 |
| 配置热重载失败 | 保留旧配置 + Warn 日志 |

## 12. 实现顺序（每个任务含验收）

| # | 任务 | 验收 |
|---|---|---|
| T1 | `config.go` + `config_test.go` | 模板生成/校验/ErrNotConfigured/热重载单测绿 |
| T2 | `chunk.go` + `chunk_test.go` | 标题切分/长度边界/过滤/扩展名单测绿 |
| T3 | `embed.go` + `embed_test.go` | httptest：批量 32 分批、请求体断言、4xx/畸形/超时单测绿 |
| T4 | `chat.go` + `chat_test.go` | httptest：断言 POST 路径为 `/responses`、body 精确字段（model/instructions/input 数组/max_output_tokens）、output_text 提取、reasoning 跳过、usage 解析、错误路径 |
| T5 | `index.go` + `index_test.go` | 向量检索 top-k/min_score、Save/Load 往返（VecB64 一致）、增量重建只重嵌入变化文件（mock embed 计数）、keyword 降级 |
| T6 | `syncer.go` + `syncer_test.go` | 本地 bare repo fixture 全流程（clone→提交→检测→merge→无变化跳过→冲突 reset→退避） |
| T7 | `service.go` + `service_test.go` + **main.go 接入** + **Dockerfile 加 git** | OnMessage 全分支单测绿（fake sender）；`go build ./...` 通过；`go vet ./...` 无告警 |
| T8 | 全量回归 | `go test ./...` 全绿（既有测试不受影响）；`go test -race ./...` 可选 |
| T9 | 真实联调（用户执行） | 见 §13 |

> 每个任务提交粒度：T1~T7 各自可独立 commit 信息（如 `0.2.0-civgo-T1 配置模块`），
> 但**不主动 commit**，全部留在工作区由用户审阅。

## 13. 部署与验收（服务器侧，用户执行）

```bash
# ① 构建新镜像并部署（沿用现有流程）
cd deploy && docker compose -f docker-compose.public.server.yml build qqbot
docker compose -f docker-compose.public.server.yml up -d qqbot
# ② 首次启动自动生成 data/civgo/civgo.json 模板 → 填写 api_key / groups → 重启
vim data/civgo/civgo.json
docker compose restart qqbot
# ③ 验证同步
docker logs -f qqbot | grep civgo        # 期望：clone/sparse 完成 → 索引构建摘要
# ④ 验证问答（真实群）：@机器人 提问 → 期望：@ 提问者 + 答案 + 📄 来源
# ⑤ 验证增量：向 civgo 仓库推送一条 docs/game_content 改动 → 5 分钟内出现
#    "civgo 同步完成" 日志且新内容可被检索
# ⑥ 验收参数观察期：top_k/min_score/限流值按问答质量微调（改配置即热生效）
```

## 14. 风险与开放项

| 风险 | 应对 |
|---|---|
| 网关 embeddings 渠道缺失/模型名不符 | 自检 + keyword 降级；`embedding_model` 可配（如 text-embedding-v1/bge-m3，以网关实际为准） |
| `/v1/responses` 的 input 数组（含 system 角色）在网关实现差异 | T4 用 httptest 锁协议；真实冒烟确认；若 system 角色被拒 → 回退方案：全部拼入 instructions |
| `docs/game_content` 实际文件格式/量级未知 | 先支持 md/txt；同步日志输出跳过清单；量级大时调 chunk_size/max_context_chars |
| 默认分支非 main | branch 空 → ls-remote 探测 HEAD；可显式配置 |
| 文档频繁变更导致嵌入费用 | 内容哈希增量，只重嵌入变化文件 |
| QQ 风控 | 复用 groupSender 800ms 限速 + 分条 ≤3500 + 三层限流 |
| 与 dev 分支长期分叉 | 改动面极小（main.go ~12 行 + Dockerfile 1 行 + 新包），合并成本低 |

## 15. 分支与提交规范

- 仅 `dev_civgo` 分支；不向 dev/stable 合入；
- 提交信息沿用项目风格（如 `0.2.0-civgo-文档同步与AI问答`）；
- 未经用户明确要求不 commit、不 push（工作区先行）。

---

### 附录 A：本规格相对 v0.1 的修正

| 项 | v0.1（旧） | 本版（新） |
|---|---|---|
| 对话端点 | `/v1/chat/completions` | **`/v1/responses`（Responses API，用户确认）** |
| chat 请求/响应格式 | messages/choices | instructions+input/max_output_tokens、output[].content[].output_text |
| Dockerfile | 不改 | **apt 追加 git**（容器无 git 二进制，同步必需） |
| 分块重叠 | chunk_overlap 生效 | 首版按行整行切块（标题先行），overlap 预留 |
| 新增 | — | 完整类型签名、协议样例、测试清单、实现顺序、降级总表 |

---

### 附录 B：实现记录（cg0.0.2 ~ cg0.0.8）

实现已完成并全量测试通过（`go test ./...` 9 包全绿、`go test -race ./internal/civgo` 通过、
`go vet ./...` 无告警）。与规格书的偏差与补充：

| 项 | 说明 |
|---|---|
| 接口抽象 | `ChatClient` 抽象为 `ChatCompleter`、`Indexer` 嵌入依赖抽象为 `Embedder`、`Options.Manager` 为 `Sender + Registerer` 聚合接口（onebot.Manager 编译期断言满足）——均为了测试注入 fake |
| 同步器 FETCH_HEAD | 浅克隆 fetch 后本地 HEAD 不自动前进，变化检测与 LastHead 记录一律基于 `FETCH_HEAD`（初版误用 HEAD，测试暴露后修复） |
| remote set-url | 每轮 fetch 前先 `git remote set-url origin <配置URL>`（幂等），使 repo.url 配置变更即时生效 |
| 索引重建 | 删除文件时其条目同步清除；Save 时填充 VecB64（初版遗漏导致持久化向量为空，往返测试暴露）；嵌入失败整体原子保留旧索引 |
| keyword 检索 | 不应用向量 min_score（分数尺度不同）；CJK 二元分词注意 `unicode.IsLetter` 对汉字亦为 true，需先判 `isCJK` |
| sparse cone 行为 | cone 模式会包含中间目录（docs/）的同级**文件**、只排除兄弟**目录**——git 预期行为，不影响 docs_path 内容完整性 |
| 真实仓库验证 | `github.com/AutumnPizazz/civgo` 可匿名 clone；`docs/game_content` 存在，15 个 .md 共 157KB；按 800 字符分块得 214 块（12_远古内容落表.md 46 块最大）——暴力线性检索毫秒级，嵌入成本极低 |
| smoke 测试 | `internal/civgo/smoke_test.go` 用真实文档样本回归分块（testdata 不入库，缺失时自动 skip） |
| 邮件提醒 | 本轮未遇到需要用户介入的阻塞问题，未触发邮件提醒流程 |

# 规则引擎设计（网页可组合管理规则）

> 状态：**已实施**（2026-08-06；AND/OR 条件组合与计数引导同日增补）· 对应代码 `internal/rules`、`state` v2、`bot` 事件管线、网页规则编辑器
> 目标版本：0.1.0（Schema v1 → v2 破坏性升级，启动时自动迁移）
> 本文档是 `docs/WEB_ADMIN_DESIGN.md` 的演进补充，沿用其配置事务 / 审计 / 校验约定。

## 0. 已确认决策

| # | 决策 | 说明 | 实施状态 |
|---|------|------|--------|
| D1 | **规则级 continue / break** | 每条规则执行完动作后，可选择「继续匹配后续规则」或「停止匹配」，规则按配置顺序执行 | ✅ `Engine.Process` |
| D2 | **规则引擎替换全部旧模块** | 关键词过滤 / 刷屏检测 / 新人欢迎 / 入群审批全部重写为统一规则；旧配置启动时自动迁移为等价规则 | ✅ `state/migrateV1ToV2` + `rules.MigrateV1` |
| D3 | **支持累计计数** | 计数条件（如：同一用户累计命中 N 次才处罚）带时间窗口，替代零散的 warn_limit / mute_limit / kick_threshold | ✅ `counterStore` + `strike_count` |

## 1. 背景与目标

### 1.1 背景

现状每个群配置被拆成四个互相独立的模块（keyword / flood / welcome / join_request），
各模块参数零散、升级逻辑硬编码（如"warn 累计 3 次自动禁言"）、无法组合。
用户希望**在网页上像搭积木一样组合"条件 + 动作"**，覆盖更多管理场景。

### 1.2 目标

- 网页可视化组合：事件（消息 / 入群 / 加群申请）→ 条件（AND 组合、可取反）→ 动作（顺序执行）。
- 规则引擎覆盖现有全部能力（迁移等价，行为不回退）。
- 条件 / 动作类型由**服务端注册表**驱动，前端根据下发 schema 动态渲染表单，
  新增类型不改前端代码（前后端解耦）。
- 规则命中与动作执行全部写入审计，规则名可溯源。
- 支持计数条件（带窗口），支持纯"过滤器规则"（无动作 + break，如管理员豁免）。

### 1.3 非目标（第一版不做）

- 定时任务（按 cron 触发的动作）、任意 OneBot action 暴露。
- OR 条件组（多条规则 + 每规则 break 即可表达 OR，见 §6.3）。
- 多事件联动（如"刷屏被踢后拒绝其加群"的跨事件状态）。
- 规则模拟 / 测试台（第二版考虑）。
- 语义 / 模型审核。

## 2. 总体架构

```text
NapCat ──WS──► onebot ──► bot（消息管线入口）
                              │
                              ▼
                       rules.Engine（新包 internal/rules）
                    事件上下文 ─► 按序匹配规则（break/continue）
                              │
              ┌───────────────┼────────────────┐
              ▼               ▼                ▼
     条件评估（无副作用）   动作执行（有副作用）  计数器存储（内存）
              │               │                │
              └──────► ActionService（OneBot + 审计，复用现有）
```

依赖方向（无环）：

```text
internal/rules  （底层：DTO、注册表、评估器、计数器；不依赖 state/bot）
      ▲
internal/state  （Control 的 GroupConfig 持有 rules.Rule；校验委托 rules）
      ▲
internal/bot    （引擎实例化：事件上下文 → 条件/动作执行，动作经 ActionService）
      ▲
internal/admin  （rule-meta API 下发注册表；群配置 API 透传 rules）
```

## 3. 配置模型（Schema v2）

### 3.1 变更点

`state.GroupConfig` 删除 `welcome / keyword / flood / join_request` 四个字段，
新增统一 `rules []rules.Rule`。`whitelist` 保留（作为 `in_whitelist` 条件的输入，
仍是"豁免名单"语义，但**不再自动跳过全部检查**——豁免由迁移生成的规则显式表达，
用户可自行编辑）。

`SchemaVersion` 1 → **2**。启动时 `ConfigService.Open` 检测到 v1 文件 → 自动迁移
（§8）→ 原子写回 v2（沿用现有 `writeFileAtomic` 事务），历史条目同步转换。

### 3.2 规则 DTO

```go
// internal/rules/types.go

// Rule 一条规则：事件触发后，当所有条件（AND）满足时顺序执行动作。
type Rule struct {
    ID       string      `json:"id"`                 // 群内唯一稳定标识（迁移生成 r1..rn，新建 UUID 短码）
    Name     string      `json:"name"`               // 规则名（审计展示），群内唯一
    Enabled  bool        `json:"enabled"`
    Event    string      `json:"event"`              // message / group_increase / group_request
    WhenMode string      `json:"when_mode,omitempty"` // all=全部满足（AND，默认）/ any=任一满足（OR）
    When     []Condition `json:"when,omitempty"`     // 空 = 恒真（欢迎语即如此）
    Then     []Action    `json:"then"`               // 顺序执行；空 = 纯过滤器（配 break）
    Break    bool        `json:"break"`              // true = 执行后停止匹配后续规则
}

// Condition 条件（判别联合：Type 决定 Params 的解析器）。
type Condition struct {
    Type   string          `json:"type"`
    Negate bool            `json:"negate,omitempty"` // 取反
    Params json.RawMessage `json:"params,omitempty"`
}

// Action 动作（判别联合）。
type Action struct {
    Type   string          `json:"type"`
    Params json.RawMessage `json:"params,omitempty"`
}
```

`Params` 各类型自有强类型 struct（如 `TextContainsParams{Text string}`），
在注册表中登记解析与校验函数（§5.3），复用 `DisallowUnknownFields` 保证参数严格。

### 3.3 配置示例（control.json v2 片段）

```json
{
  "schema_version": 2,
  "groups": [{
    "group_id": 123456,
    "enabled": true,
    "whitelist": [10001],
    "rules": [
      { "id": "r1", "name": "管理员豁免", "event": "message",
        "when": [{"type": "is_exempt"}], "then": [], "break": true },
      { "id": "r2", "name": "关键词-广告", "event": "message",
        "when": [{"type": "text_contains", "params": {"text": "加群领福利"}}],
        "then": [{"type": "warn", "params": {"reason": "禁止广告"}}], "break": true },
      { "id": "r3", "name": "广告升级禁言", "event": "message",
        "when": [
          {"type": "text_contains", "params": {"text": "加群领福利"}},
          {"type": "strike_count", "params": {"counter_id": "kw", "min_count": 3}}
        ],
        "then": [{"type": "mute", "params": {"minutes": 30}},
                 {"type": "reset_counter", "params": {"counter_id": "kw"}}],
        "break": true },
      { "id": "r4", "name": "刷屏检测", "event": "message",
        "when": [{"type": "flood", "params": {"window_sec": 10, "max_count": 8}}],
        "then": [{"type": "mute", "params": {"minutes": 10}},
                 {"type": "increment_counter", "params": {"counter_id": "flood"}}],
        "break": true },
      { "id": "r5", "name": "新人欢迎", "event": "group_increase",
        "when": [],
        "then": [{"type": "send_message",
                  "params": {"message": "欢迎 {nickname} 加入本群！", "at": true}}],
        "break": true },
      { "id": "r6", "name": "拒绝广告备注", "event": "group_request",
        "when": [{"type": "request_contains", "params": {"text": "推广"}}],
        "then": [{"type": "reject_join", "params": {"reason": "本群不接受推广"}}],
        "break": true }
    ]
  }]
}
```

## 4. 事件与执行模型

### 4.1 事件上下文

```go
// internal/rules/engine.go
type EventContext struct {
    Event     string    // message / group_increase / group_request
    GroupID   int64
    UserID    int64     // 消息发送者 / 入群者 / 申请人
    SelfID    int64
    Role      string    // owner / admin / member（message 事件）
    Text      string    // 消息纯文本（message）；申请备注（group_request）
    MsgTypes  []string  // 消息含有的 segment 类型（text/image/at/url/...）
    MessageID int32     // 撤回动作需要
    SubType   string    // group_request：add / invite
    Flag      string    // group_request：审批 flag
    Time      time.Time
    // 派生（惰性求值）：
    IsWhitelisted func() bool
    IsOwner       bool // userID == 系统 owner
}
```

### 4.2 匹配流程（每事件一次）

```text
事件到达（群已启用）→ 构造 EventContext
→ 遍历该群 Rules（配置顺序，仅 Enabled）
    1) Event 不匹配 → 跳过
    2) 条件组合评估（WhenMode）：all=全部条件为真（含逐条 Negate 取反）→ 命中；
       any=任一条件为真 → 命中；空 when = 恒真
    3) 顺序执行 Then 动作（逐个；单动作失败不中断后续，记日志+审计）
    4) 命中后按 Break 决定：true 停止；false 继续下一条规则
→ 记录审计：规则名 + 命中的条件类型 + 动作结果
```

要点：

- **条件评估无副作用**；动作副作用即时生效（**后续规则可看到本规则动作的计数变化**）。
- 与/或/非：`when_mode=all`（AND）+ 逐条件 `negate`（非）覆盖"非 A 且 B"；
  `when_mode=any`（OR）覆盖"A 或 B"；混合逻辑（如 (A或B)且C）拆成多条规则 + break 组合。
- 同 counter_id 的多条规则组成**升级处罚链**：前置规则「计数+N」（break=false）→
  升级规则「累计计数≥N」+「计数清零」（break=true）→ 普通处罚规则；网页提供「⚡ 升级处罚模板」一键生成。
- 一条消息可命中多条 `break=false` 规则（等价现在"关键词 + 刷屏同时检测"）。
- 白名单 / 管理员 / owner **不再由管线硬编码豁免**，由迁移生成的豁免规则显式处理
  （§8），用户可删改。这是行为变更，在迁移日志中明示。
- 机器人自身消息、未配置群的消息在进入引擎前丢弃（沿用现有 onMessage 开头逻辑）。

### 4.3 动作失败语义

- OneBot 连接不可用（503 类错误）：该动作记失败，**继续执行后续动作**（已尽力）。
- 动作结果写审计（复用 `ActionService` 的 ok/failed/unknown 三态）。
- `send_message` 失败不重试（沿用发送限速队列，避免风控）。

## 5. 条件 / 动作注册表

### 5.1 设计

```go
// internal/rules/registry.go
type ConditionSpec struct {
    Type        string          // "text_contains"
    Label       string          // 中文标签（前端展示）
    Description string
    Events      []string        // 适用事件（校验 + 前端过滤）
    Params      any             // 参数零值 struct（json schema 由反射/声明生成）
    Eval        func(ctx *EventContext, raw json.RawMessage) (bool, error)
}

type ActionSpec struct {
    Type     string
    Label    string
    Description string
    Events   []string
    Params   any
    Run      func(c *Engine, ctx *EventContext, raw json.RawMessage) error
    Dangerous bool  // kick 等，前端二次确认
}
```

注册表同时是**校验器**（解析 params → 类型检查 → 范围检查 → 编译正则）与
**API 元数据源**（`GET /api/v1/rule-meta` 下发 Label / Params 的 JSON Schema，
前端据此动态渲染表单）。新增类型 = 注册一个新 spec，零前端改动。

### 5.2 条件类型清单（当前 26 个）

| type | 适用事件 | 参数 | 说明 |
|---|---|---|---|
| `always` | 全部 | — | 恒真（显式占位，空 when 同义） |
| `is_exempt` | message | — | 群主 / 群管理 / 白名单 / 系统 owner（迁移豁免规则用） |
| `is_owner` | 全部 | — | 用户是系统超级管理员 |
| `user_id` | 全部 | `user_id` | 指定 QQ |
| `user_id_in` | 全部 | `user_ids []int64` | 多个 QQ（上限 50） |
| `user_role` | message | `roles []string` | owner / admin / member 之一 |
| `in_whitelist` | message | — | 群白名单内 |
| `text_contains` | message | `text` | 纯文本包含（大小写不敏感，≤200 字符） |
| `text_regex` | message | `pattern` | 正则匹配（编译校验） |
| `text_repeat` | message | `window_sec`, `min_count` | 窗口内相同文本出现 ≥N 次（含本条；复用 msgRing） |
| `text_length` | message | `min`, `max` | 消息字符数区间（0=不限；防长文本） |
| `url_count` | message | `min_count` | 消息含链接 ≥N 个（广告特征） |
| `image_count` | message | `min_count` | 消息含图片 ≥N 张（图片刷屏） |
| `at_count` | message | `min_count` | 消息 @ 的人 ≥N 个（@轰炸） |
| `mention_self` | message | — | 消息 @ 了机器人 |
| `message_has_type` | message | `types []string` | 消息含指定 segment 类型（text/image/at/url/record/video） |
| `flood` | message | `window_sec` 3~3600, `max_count` 1~100 | 窗口内消息数（沿用 floodDetector 语义） |
| `strike_count` | message | `counter_id`, `min_count` 1~100, `window_hours` 1~720 | 计数条件（D3） |
| `time_between` | message | `start` "HH:MM", `end` "HH:MM" | 时间段（支持跨午夜；与 end 相同=全天） |
| `weekday` | message | `days []int` 0~6 | 星期几（周末严管等） |
| `probability` | message | `percent` 1~100 | 随机概率命中 |
| `user_joined_within` | message | `days` 1~3650 | 入群未满 N 天（新人保护期；成员信息缓存） |
| `request_contains` | group_request | `text` | 申请备注包含 |
| `request_regex` | group_request | `pattern` | 申请备注正则 |
| `request_comment_empty` | group_request | — | 申请备注为空（广告特征） |
| `subtype_is` | group_request | `subtype` add/invite | 申请类型 |
| `inviter_is` | group_increase | `user_id` | 由指定 QQ 邀请入群 |

### 5.3 动作类型清单（当前 15 个）

| type | 适用事件 | 参数 | 说明 |
|---|---|---|---|
| `send_message` | 全部 | `message` 模板, `at bool` | 发消息 / @目标；group_increase 时即欢迎语 |
| `send_private` | message | `message` 模板 | 向目标私聊发送（违规提醒等） |
| `warn` | message | `reason` | @警告，文案模板 `警告：{reason}（累计 {count} 次…见 §5.4）` |
| `mute` | message | `minutes` 1~43200, `reason` | 禁言（含自动通知，文案模板化） |
| `kick` | message | `reason` | 移出（Dangerous） |
| `recall` | message | — | 撤回本条消息（Dangerous） |
| `whole_ban` | message | `enable` | 全员禁言开/关（宵禁自动化；Dangerous） |
| `card` | message | `card` 模板 | 设置目标群名片（留空=清空） |
| `increment_counter` | message | `counter_id`, `step`, `window_hours` | 计数 +1（升级逻辑的"计分"动作） |
| `decrement_counter` | message | `counter_id`, `step` | 计数 -N（撤销计分） |
| `reset_counter` | message | `counter_id` | 清零（升级处罚后调用） |
| `set_counter` | message | `counter_id`, `count` | 直接设值 |
| `approve_join` | group_request | — | 同意加群 / 邀请 |
| `reject_join` | group_request | `reason` | 拒绝加群 |
| `noop` | 全部 | — | 显式无操作（过滤器规则可空 then） |

### 5.4 通知文案模板变量

`warn` / `mute` / `kick` / `send_message` 的文本支持变量替换：

| 变量 | 含义 |
|---|---|
| `{nickname}` | 目标用户昵称（无缓存时回退 QQ 号） |
| `{user_id}` | 目标 QQ |
| `{group_id}` | 群号 |
| `{bot_name}` | 机器人名（System.BotName） |
| `{matched_text}` | 首个命中条件的匹配文本（text_contains/regex 的原文） |
| `{rule_name}` | 规则名 |
| `{count}` | 命中时的计数（warn 动作附带，用于"累计 N 次"提示） |

> 旧版 `doMute` 等硬编码文案（"你因【xx】被禁言 N 分钟"）迁移时固化为动作参数，
> 用户在网页可改。

## 6. 组合模式

### 6.1 升级处罚（D3 核心模式）——"计数先行"

旧"warn 累计 3 次升级禁言"：

```text
规则A（break=false）：when=[text_contains(广告)]  then=[increment_counter(kw)]
规则B（break=true） ：when=[text_contains(广告), strike_count(kw ≥ 3)]  then=[mute(30), reset_counter(kw)]
规则C（break=true） ：when=[text_contains(广告)]  then=[warn(禁止广告)]
```

第 1~2 次命中：A 计数 → C 警告；第 3 次：A 计数 → B 禁言并清零。
**计数器是 (群, counter_id, 用户) 维度**，同 counter 可被多条规则共享
（旧版所有关键词规则共享一个计分，迁移后默认共享 `kw`）。

### 6.2 过滤器规则（豁免 / 放行）

`when=[...]` + `then=[]` + `break=true`：命中即跳过后续所有规则。
用于"白名单用户发广告不处理""凌晨 1 点后全员禁言前先放行管理员"等。

### 6.3 OR 语义

规则级 `when_mode=any`（任一满足）直接表达 OR：
"文本包含 广告 或 文本包含 诈骗" → 单条规则，条件组合选「任一满足（OR）」。

更复杂的混合逻辑（如 `(A或B)且C`）拆成多条规则 + break 组合：
规则1（break=true）：A 或 B → 动作；规则2（break=false）：C → 计数/标记；规则3：C 且 标记 → 动作。

### 6.4 时段规则（宵禁 / 严管模式）

```text
规则A（break=true）：when=[time_between(23:00, 07:00), text_contains(刷屏)]  then=[kick]
规则B（break=true）：when=[text_contains(刷屏)]  then=[mute(10)]
```

## 7. 计数器存储

泛化现有 `bot/strikeStore` → `internal/rules/counterStore`：

```go
type counterStore struct {  // 内存，重启清零（沿用设计取舍）
    mu    sync.Mutex
    data  map[string]map[string]map[int64]*counter  // groupID/counterID → user → count+updated
}
// Increment / Get / Reset；窗口滑动语义与 strikeStore 一致：
// 距上次更新超过窗口 → 重置为 1。
```

- 校验**不强制** `strike_count` 引用的 counter 必须存在（未引用时计 0），
  但 `GET /api/v1/groups/{id}/counters` 返回该群已出现的 counter 清单，
  前端条件表单用下拉选择，降低拼错概率。
- 内存上限：每群每 counter 最多 200 用户，LRU 淘汰（防刷爆）。

## 8. 迁移映射（v1 → v2 规则）

启动自动迁移，对每个群按**固定顺序**生成规则（ID 前缀 `r1..rn`，可读名称），
`normalizeAndValidate` 后原子写回；历史条目逐条转换，转换失败的历史丢弃并记日志。

### 8.1 豁免规则（所有群置顶，原管线硬编码逻辑）

```text
规则「默认豁免」event=message  when=[is_exempt]  then=[]  break=true
```

> 行为差异说明：旧版豁免在关键词/刷屏检查**之前**硬编码生效；迁移后为显式规则，
> 用户可在网页关闭/修改（比如让白名单用户也受刷屏限制——把 is_exempt 换成
> `user_role in (owner, admin)` + `negate in_whitelist` 等）。

### 8.2 welcome

```text
「新人欢迎」event=group_increase  when=[]  then=[send_message(原模板, at=true)]  break=true
```

### 8.3 keyword（每个规则 1~2 条，按配置顺序）

pattern 原样；regex=true → `text_regex`，否则 `text_contains`。

```text
warn 规则：  then=[warn(reason=原 action 语义), increment_counter(kw)]  break=true
   + 前置（若 warn_limit>0）：
     升级规则 when=[匹配, strike_count(kw ≥ warn_limit)] then=[mute(30), reset_counter(kw)] break=true
mute 规则：  then=[mute(minutes=rule.MuteMinutes), increment_counter(kw)]  break=true
   + 前置（若 mute_limit>0）：升级规则 → kick(多次违规) + reset_counter(kw)
kick 规则：  then=[kick(发布违规内容)]  break=true
```

**注意顺序**：升级规则必须排在对应普通规则**之前**（§6.1 模式）。
旧版 `StrikeTTLH`（默认 24h）→ 所有 strike_count 条件的 `window_hours`。
`warn` 动作文案：`群内禁止发布内容：{matched_text}`（原硬编码文案固化为参数）。

### 8.4 flood

```text
前置（若 kick_on_repeat）：
  「刷屏多次移出」when=[flood(window,max), strike_count(flood ≥ kick_threshold)]
                  then=[kick(多次刷屏), reset_counter(flood)]  break=true
「刷屏检测」when=[flood(window,max)]
           then=[mute(minutes), increment_counter(flood)]  break=true
```

### 8.5 join_request + invite

```text
若 reject_keyword 非空：
  「拒绝申请-{kw}」when=[request_contains(kw)]  then=[reject_join(原 reason)]  break=true
若 auto_approve：
  「自动同意申请」when=[subtype_is(add)]  then=[approve_join]  break=true
「同意邀请」when=[subtype_is(invite), is_owner]  then=[approve_join]  break=true   （原固定逻辑）
「拒绝邀请」when=[subtype_is(invite)]  then=[reject_join(机器人不接受邀请)]  break=true
```

### 8.6 迁移幂等与回退

- 迁移前完整校验 v1（复用现有 normalizeAndValidate），失败**中止启动**并提示手工处理。
- 迁移写回失败 → 中止启动，原 v1 文件与 .bak 不动（无损坏风险）。
- 升级后 `config validate` / `secrets rotate` 等 CLI 兼容 v2（共用同一 ConfigService）。
- 提供 `--no-migrate-rules` 逃生开关（默认 false）：v1 文件直接以旧结构只读运行？
  ——**不提供**。破坏性升级按 README 约定"新版本生成的新配置不保证旧版本可读"，
  迁移失败时用户可先备份 control.json 再用旧二进制回滚。

## 9. 校验规则（v2 新增，沿用 ValidationError 逐字段报告）

| 项 | 规则 |
|---|---|
| 规则数量 | 每群 ≤ 100（合并原 50 关键词 + 其他模块） |
| 名称 | 必填、≤64 字符、群内唯一 |
| ID | 必填、≤32 字符、群内唯一 |
| Event | 枚举；When/Then 内类型必须声明支持该事件（§5.1 Events），否则报错并给出适用事件 |
| When | ≤8 个条件；空合法（恒真） |
| Then | ≤4 个动作；空合法（过滤器） |
| 参数 | 按 spec 解析：正则编译、`time_between` 的 HH:MM 合法性（跨午夜：start>end 视为次日）、
  `user_id_in` ≤50、`window_sec` 3~3600、`min_count` 1~100、`minutes` 1~43200、
  文本类 ≤500 字符（复用 validateText） |
| 模板 | 未知 `{变量}` 不报错（仅替换已知），避免升级后旧模板炸校验 |

## 10. API 变更

| 方法/路径 | 变更 |
|---|---|
| `GET /api/v1/rule-meta` | **新增**：事件 / 条件类型 / 动作类型注册表（含参数 JSON Schema，前端动态表单） |
| `GET /api/v1/groups`、`GET/PUT/POST/DELETE /api/v1/groups/{id}` | 群 DTO 内 `rules` 字段替换旧四模块（其余不变） |
| `GET /api/v1/groups/{id}/counters` | **新增**：计数器实时状态（counter → user → count，供网页排障） |
| `GET /api/v1/history` | 不变（历史快照 v2；v1 历史已迁移） |

`rule-meta` 响应示例：

```json
{
  "events": [
    {"name": "message", "label": "群消息", "conditions": ["text_contains", "flood", "..."], "actions": ["warn", "mute", "..."]},
    {"name": "group_increase", "label": "新人入群", ...},
    {"name": "group_request", "label": "加群申请", ...}
  ],
  "conditions": [
    {"type": "text_contains", "label": "文本包含", "events": ["message"],
     "params_schema": {"type": "object", "required": ["text"], "properties": {"text": {"type": "string", "maxLength": 200, "title": "关键词"}}},
    ...
  ],
  "actions": [ {"type": "mute", "label": "禁言", "dangerous": false, "params_schema": {...}}, ... ]
}
```

## 11. 前端设计（群管理页重构）

### 11.1 规则列表卡片（替换原四个卡片）

```
┌─ 管理规则（N）─────────────────────────────────────────────┐
│ [＋ 添加规则]                                      [排序说明] │
│ ┌────────────────────────────────────────────────────────┐ │
│ │ ⚙ 管理员豁免        [消息] 豁免        [停止]        ┆   │ │
│ │   条件: 免检       动作: 无           ●●●○  ┆ ↑ ↓ 编辑 删 │
│ ├────────────────────────────────────────────────────────┤ │
│ │ ⚙ 关键词-广告       [消息] 文本包含    [停止]        ┆   │ │
│ │   条件: 文本包含"加群领福利"  动作: 警告       ○●●○    │ │
│ └────────────────────────────────────────────────────────┘ │
└───────────────────────────────────────────────────────────┘
```

- 每行：开关、名称、事件 badge、条件/动作摘要 chips、`break=停止/继续` badge、↑↓ 排序、编辑/删除。
- 摘要生成规则：`条件: 文本包含"xx"、刷屏(10s/8条)`；`动作: 禁言30分、计数+1`。

### 11.2 规则编辑器（模态）

- **名称** + **事件** 下拉（切换事件时过滤条件/动作类型，并丢弃不适用项）。
- **条件列表**：每行 `[类型下拉] [取反 ☐] [动态参数表单] [删除]`；＋添加条件。
  参数表单由 `rule-meta` 的 params_schema 驱动渲染（text/number/select/checkbox/time）。
- **动作列表**：每行 `[类型下拉] [动态参数表单] [删除]`；＋添加动作。
- **执行后**：radio `继续匹配后续规则 / 停止匹配`。
- 底部提示区：模板变量说明、计数条件使用说明（"需要某规则先 increment_counter"）。
- 危险动作（kick/recall）选择时弹二次确认；规则名可含 emoji（≤64 字符）。
- 校验错误回显到对应字段（沿用后端 422 fields）。

### 11.3 计数机制引导

- **「⚡ 升级处罚模板」**：一键生成「计数+1（继续）→ 累计≥N 升级处罚+清零（停止）→ 普通处罚（停止）」三条规则，
  输入关键词/次数/处罚方式即可，用户无需理解计数链。
- **计数器 ID 下拉建议**：编辑器内 counter_id 输入框带 datalist 候选（本群已用 ID + 常用名 kw/flood）。
- **上下文提示**：编辑器动态检测计数相关类型组合，提示缺「计数+N」或「计数清零」等链路断点。

### 11.4 迁移提示

旧 v1 配置加载后（首次进入群管理页）显示横幅：
`"旧版配置已自动迁移为 N 条规则，行为与原来一致。默认豁免为规则 r1，可编辑。"`

## 12. 实施阶段（完成情况）

| 阶段 | 内容 | 状态 |
|---|---|---|
| A 核心 | `internal/rules`：DTO、注册表（16 条件 + 11 动作）、引擎、counterStore、v1→v2 迁移；`state` 接 v2 + 校验委托 | ✅ |
| B 集成 | bot 改接引擎（onMessage/onNotice/onRequest 重构）；删旧 keyword/flood/welcome/join 逻辑；通知文案模板化 | ✅ |
| C API | `rule-meta`、counters 端点；api_groups 透传 rules；新建群/重置注入默认规则 | ✅ |
| D 前端 | 规则列表 + 编辑器模态（动态参数表单）+ 计数器排障面板 | ✅ |
| E 收尾 | README/设计文档更新、全量 `-race` 测试 | ✅ 自动化验收；真实环境待部署后补充 |

每阶段独立可提交；A~B 完成后旧模块代码删除（含 `config` 包中 Keyword/Flood 等
遗留字段——**保留** `config` 包仅作一次性 YAML 迁移输入）。

## 13. 测试计划

- **rules 单测**：每个条件/动作的参数解析与边界（正则编译、跨午夜时间段、
  计数器窗口滑动、Negate）；引擎语义（AND、break/continue、空 when、过滤器规则、
  动作失败继续）；counterStore 并发与 LRU。
- **迁移黄金测试**：构造 v1 Control（全模块开启）→ `MigrateV1ToV2` → 断言规则 JSON
  与 §8 映射一致；升级规则顺序断言；历史条目转换。
- **bot 集成**（mock OneBot）：场景矩阵——关键词 warn 升级禁言、刷屏、重复消息、
  欢迎语变量替换、拒绝/同意加群、invite 审批、豁免规则放行、时段规则。
- **E2E**（现有框架扩展）：网页 API 创建含 rules 的群 → 热生效 → 事件驱动断言。
- **回归**：`go vet`、`go build`、`-race`、`go test ./...` 全绿。

## 14. 风险与兼容性

| 风险 | 缓解 |
|---|---|
| v2 破坏性升级 | 启动自动迁移 + 原子写回 + 失败中止不损坏原文件；旧二进制回滚=恢复备份 |
| 迁移行为偏差 | 黄金测试逐条断言；豁免规则置顶显式化；迁移日志打印差异摘要 |
| 计数条件拼错 counter_id 导致永不处罚 | 前端下拉候选（counters 端点）+ 规则列表展示 counter 使用 |
| 规则复杂导致误处罚 | 全部处罚动作走审计可溯源；kick/recall 前端二次确认；规则可一键停用 |
| 前端动态表单复杂度 | params_schema 收敛为 text/number/select/checkbox/time 五类控件 |
| 多规则叠加误伤 | 每个事件最多 N 条 break 规则文档化；规则列表提供「模拟」预告（第二版） |

## 15. 参考

- 现有实现：`internal/bot/{keyword,flood,strike,bot}.go`、`internal/state/{control,validate,service,migrate}.go`
- 复用组件：`ActionService`（动作+审计）、`msgRing`（重复文本检测）、
  `writeFileAtomic`（迁移写回）、`ValidationError` 逐字段校验。

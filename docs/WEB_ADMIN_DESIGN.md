# Web 管理后台设计方案

> 状态：实施完成（阶段 1~5 全部完成，真实环境验收通过 ✅）
> 适用项目：qqbot
> 目标：用内网/VPN 网页后台替代 QQ 私聊配置和邮件登录通知，并提供完整人工群管能力。

## 0. 实施进度

| 阶段 | 内容 | 状态 |
|---|---|---|
| 1 | 配置核心（BootstrapConfig、DTO、control.json、ConfigService、迁移） | ✅ 完成：`internal/state`，事务/迁移/校验测试全绿 |
| 2 | 组件服务化（OneBot Manager、ActionService、NapCat Client、审计扩展） | ✅ 完成：generation 重配测试、动作 unknown 语义、NapCat 并发刷新防护 |
| 3 | 鉴权和 API | ✅ 完成：setup/Argon2id/session/CSRF/限流 + 全部 REST 端点 + 幂等 + 统一错误模型，集成测试全绿 |
| 4 | 网页界面 | ✅ 完成并通过浏览器验收：Edge 无头端到端（setup→设置→建群→群配置→人工群管→审计→设置）桌面 1280 + 移动 375 无溢出；Docker 验收（重启配置/密码保留、session 失效、端口仅 loopback） |
| 5 | 切换和清理 | ✅ 完成：私聊指令/SMTP/IMAP/notify 包全部停用删除；CLI（reset-password/rotate/validate）；Docker/README/部署文档更新；真实环境验收 ✅（真实 NapCat + 真实群 178055086 全部通过） |

阶段 1 完成说明：`main.go` 双路径启动（有 control.json 走新路径，否则旧路径回归基线）；`QQBOT_IMPORT_CONFIG` 一次性迁移。
阶段 2 完成说明：`onebot.Manager` 替代直接持有 Client（Bot/main 均改用）；手动指令与自动处罚统一走 `ActionService`；`internal/napcat` 独立客户端（typed HTTP error + singleflight + 状态缓存）。
阶段 3 完成说明：`internal/admin` 包（auth/session/CSRF/限流/统一错误模型/全部 REST API）；`main.go` 三路启动（control.json 网页化 / 一次性迁移 / 初始化模式 HTTP-only setup）；配置热生效（ConfigService.Subscribe → Bot 外部配置源 + OneBot Reconfigure）。
阶段 4 说明：前端资源嵌入 `internal/admin/web`（go:embed，同源 /static），无构建工具；敏感字段只返回 configured；二维码由后端生成、前端 `<img>` 直连（no-store）。

验收补充（浏览器 + Docker 发现并修复的问题）：
- `GET /groups` 未初始化时返回 `null` → 后端保证空数组、前端 `|| []` 容错。
- 初始化模式下 `/config/history` 503 导致设置页崩溃 → 前端容错为空列表。
- 架构缺口：配置首次提交后 Bot 不自动启动 → `main.go` 订阅 ConfigService，初始化完成即自动启动 Manager/Bot（`admin.Server.SetComponents` 支持运行时注入，无需重启进程）。
- 移动端顶栏溢出 → topbar flex-wrap。

## 1. 背景

当前项目存在三类管理入口：

1. `config.yaml` 保存启动配置、群基准配置、OneBot/NapCat 凭据和邮件配置。
2. owner 通过 QQ 私聊 `/cfg` 修改群级运行时配置，覆盖内容保存到 `data/runtime.json`。
3. NapCat 掉线后，通过 SMTP 发送登录二维码，再通过 IMAP 读取“请重发”邮件。

这些入口分散且依赖 QQ 私聊、邮箱服务和人工维护 YAML，远程管理成本较高。本方案将管理能力集中到同一个网页后台，同时保留现有单二进制、轻量部署和配置热生效特性。

## 2. 已确认决策

| 决策项 | 结论 |
|---|---|
| 访问范围 | 仅内网或 VPN，不直接暴露公网 |
| 第一版范围 | 完整管理台：配置、登录二维码、审计和人工群管 |
| 配置目标 | 除不可避免的启动引导项外，尽量全部网页化 |
| 旧入口 | 网页版本上线时立即停用 QQ 私聊管理和 SMTP/IMAP |
| 部署形态 | 保持 Go 单二进制，前端资源嵌入二进制 |
| 管理账户 | 第一版只支持单管理员，不做注册、多租户和角色体系 |

不可网页化的最小启动配置只有：

- 管理后台监听地址，默认 `127.0.0.1:8080`。
- 数据目录，默认 `data`。
- 独立主密钥文件，用于加密网页管理的敏感凭据。

## 3. 目标与非目标

### 3.1 目标

- 浏览器中管理机器人、OneBot、NapCat 和所有群配置。
- 配置修改经过完整校验、原子持久化并即时生效。
- 支持新增、禁用和删除群，不再依赖修改只读 YAML。
- 浏览 NapCat 登录状态，直接显示和刷新 QQ 登录二维码。
- 提供禁言、解禁、踢人、全员禁言、撤回和群名片操作。
- 统一记录网页配置变更、人工操作和自动群管审计。
- 支持配置版本冲突检测、历史恢复和管理员密码恢复。
- 首次部署没有业务配置时，也能先启动网页完成初始化。

### 3.2 非目标

- 不开放公网 SaaS 服务。
- 不提供多管理员、细粒度 RBAC 或群管理员自助登录。
- 不提供任意 OneBot action 转发接口。
- 不在第一版实现语义审核、模型审核或复杂策略编排。
- 不保证网页版本生成的新配置可被旧版机器人反向读取。

## 4. 现状与可复用能力

### 4.1 静态配置

`internal/config/config.go` 定义 `Config`、默认值和校验逻辑。当前 `main.go` 在创建 HTTP 服务之前强制加载 YAML，并要求：

- `onebot.ws_url` 非空。
- `bot.owner` 非零。
- 启用 `login_notify` 时 SMTP、NapCat 和收件人配置完整。

因此，现有启动流程不能直接支持“无配置先打开初始化页面”，需要拆分 bootstrap 生命周期和业务生命周期。

### 4.2 运行时覆盖

`internal/bot/runtime.go` 已提供群级覆盖、深拷贝、JSON 持久化和配置合并；`internal/bot/bot.go` 已采用 `atomic.Pointer[config.Config]` 进行 copy-on-write 配置切换，事件处理可以无锁读取稳定快照。

但当前更新流程存在两个一致性问题：

1. `runtime.update` 先修改内存，后续校验失败不会回滚这次修改。
2. `applyRuntime` 先替换生效快照，再写磁盘；写盘失败会造成内存与磁盘不一致。

网页后台不得直接复用该更新顺序，必须抽取事务化配置服务。

### 4.3 人工群管

`internal/bot/commands.go` 和 `internal/onebot/client.go` 已实现禁言、解禁、踢人、全员禁言、撤回和群名片。这些逻辑应抽成不依赖 QQ 文本命令的 `ActionService`，网页 API 和自动处罚通过同一服务调用 OneBot 并写审计。

### 4.4 NapCat 登录能力

`internal/notify/napcat.go` 已实现 WebUI 登录认证、登录状态查询、二维码获取和刷新，以及二维码 PNG 生成。这部分应迁移到独立的 `internal/napcat` 包。SMTP、IMAP 和邮件 MIME 逻辑不再进入生产启动路径。

## 5. 总体架构

```text
浏览器 / 内网 VPN
        |
        | HTTPS
        v
+--------------------------+
| Admin HTTP Server        |
| - Auth / Session / CSRF  |
| - REST API               |
| - Embedded Web UI        |
+------------+-------------+
             |
     +-------+--------+----------------+
     |                |                |
     v                v                v
ConfigService    ActionService    NapCat Service
     |                |                |
     v                v                v
control.json     OneBot Manager    NapCat WebUI
     |
     v
atomic config snapshot -> Bot event pipeline
```

推荐新增模块：

```text
internal/
  admin/       HTTP 服务、鉴权、中间件、API 和嵌入式前端
  state/       control.json、迁移、事务更新、配置历史
  napcat/      NapCat 登录状态和二维码客户端
  bot/         Bot、ActionService、自动群管和审计
  onebot/      可重配置的连接管理器和 OneBot 动作
```

`admin` 只负责编排和 HTTP 语义，不直接修改状态文件，也不直接调用底层 WebSocket。

## 6. 启动生命周期

### 6.1 BootstrapConfig

进程先读取以下启动参数或环境变量：

```text
QQBOT_ADMIN_LISTEN       默认 127.0.0.1:8080
QQBOT_DATA_DIR           默认 data
QQBOT_MASTER_KEY_FILE    生产环境必须提供
QQBOT_IMPORT_CONFIG      可选，一次性导入旧 config.yaml
```

环境变量不再静默覆盖迁移后的 OneBot/NapCat token。旧环境变量只允许作为显式迁移输入，导入后必须停止使用。

### 6.2 初始化模式

当 `data/control.json` 或管理员认证状态不存在时：

1. 只启动管理 HTTP 服务。
2. 不创建 Bot。
3. 不连接 OneBot。
4. 不启动任何邮件或 IMAP goroutine。
5. 除 `/healthz`、静态资源和 setup/login 接口外，其余 API 返回 `503 setup_required`。

首次启动生成高熵 setup token：

- 明文只输出到日志一次。
- 磁盘只保存 token 哈希、创建时间和消费状态。
- setup 成功时通过原子更新标记为已消费。
- 重启不会重新生成可绕过已有认证状态的新 token。

### 6.3 运行模式

管理员完成基础配置后：

1. `ConfigService` 加载并校验规范化配置。
2. 创建 OneBot Manager。
3. 创建 Bot 并注册群消息、通知和请求事件。
4. 创建 NapCat Service。
5. 页面状态由“待初始化”切换为“运行中/重连中/异常”。

## 7. 规范化配置模型

### 7.1 唯一配置源

迁移后 `data/control.json` 是唯一配置写入源。`config.yaml`、`runtime.json` 和 `aliases.json` 只参与一次性导入，不再被生产代码写入。

建议结构：

```json
{
  "schema_version": 1,
  "revision": 42,
  "updated_at": "2026-01-01T00:00:00Z",
  "system": {
    "bot_name": "群管小助手",
    "owner": 10001,
    "timezone": "Asia/Shanghai",
    "onebot": {
      "ws_url": "ws://napcat:3001",
      "api_timeout_ms": 5000,
      "access_token": {"encrypted": "...", "key_id": "v1"}
    },
    "napcat": {
      "webui_url": "http://napcat:6099",
      "webui_token": {"encrypted": "...", "key_id": "v1"}
    }
  },
  "groups": [],
  "history": []
}
```

实际 DTO 必须：

- 使用独立 JSON 类型，不直接序列化当前 `config.Config`。
- 禁止未知字段。
- 排除 `time.Location`、已编译正则和 `time.Duration` 等派生字段。
- 将群备注并入群 DTO，参与同一 revision 和事务。
- 对启用和禁用群都执行结构校验。

### 7.2 敏感字段

OneBot AccessToken 和 NapCat WebUI token 允许在网页替换，但遵守以下规则：

- 使用 AES-256-GCM 加密落盘。
- 每次加密生成随机 nonce。
- AAD 至少包含 schema version、字段路径和 key ID。
- 主密钥来自独立只读文件，不写入 `control.json`。
- GET API 只返回 `configured: true/false`，不返回明文或密文。
- 更新请求中字段缺失表示保持原值，显式 `clear: true` 才清除。
- 日志、审计和错误响应不得记录凭据。

主密钥丢失后无法恢复加密凭据，必须将主密钥和配置备份分开保存，并提供停机状态下的密钥轮换工具。

### 7.3 配置历史

`control.json` 内保留最近 20 个完整、可恢复的配置快照，以及 revision、修改时间、actor、变更字段摘要和配置摘要哈希。历史与当前配置在同一次原子文件替换中提交，避免两个 sidecar 文件分别 rename 造成事务边界不完整。

## 8. 配置事务

所有配置修改必须经过同一个 `ConfigService.Update`：

```text
获取全局更新锁
  -> 校验 If-Match / revision
  -> 深拷贝当前规范化状态
  -> 在候选副本上执行修改
  -> 解密并构造候选生效配置
  -> NormalizeAndValidate
  -> 生成新 revision 和历史快照
  -> 写同目录临时文件
  -> 设置 0600 权限
  -> fsync 临时文件
  -> 原子 rename
  -> fsync 父目录（平台支持时）
  -> atomic.Pointer.Store
  -> 发布组件重配置事件
  -> 释放锁
```

约束：

- 校验失败返回 `422`，候选副本直接丢弃。
- revision 不匹配返回 `409`，页面必须重新读取配置。
- 写盘失败不得替换内存快照。
- 写盘成功后的 Bot 快照切换不再包含可能失败的工作。
- OneBot/NapCat 重连失败不回滚有效配置，而是进入 `error/retrying` 状态。
- 配置审计写入失败需要报警，但不能伪装成配置提交失败。

建议统一错误格式：

```json
{
  "error": {
    "code": "validation_failed",
    "message": "配置校验失败",
    "fields": {"groups.0.flood.window_seconds": "范围必须为 3~3600"}
  }
}
```

## 9. 配置校验

在当前 `Validate()` 基础上补充：

- 所有 QQ 号和群号必须为正数，群号不得重复。
- 群备注非空、非纯数字、不得重复，并限制长度。
- 欢迎语、拒绝原因和群名片限制长度及控制字符。
- 规则 pattern 非空并限制长度。
- 每群规则数和白名单数量设置上限。
- action 只能是 `warn`、`mute`、`kick`。
- 正则必须可编译。
- 所有数值范围与现有 `/cfg` 行为一致。
- URL 只允许 `http/https/ws/wss` 对应的合法场景。
- NapCat URL 拒绝 userinfo、非法 scheme 和不允许的目标地址。

## 10. OneBot 连接管理

当前 `onebot.Client` 将 URL、token 和 timeout 固定在实例字段中。网页修改这些字段后需要新的 `onebot.Manager`：

- 每次连接拥有独立 generation 和 context。
- `readLoop`、ping loop 和连接对象绑定同一 generation。
- 重配置时先发布新 endpoint，再取消并关闭旧 generation。
- 取消旧 pending 请求，返回明确的 disconnected/reconfigured 错误。
- 等待旧循环退出后连接最新 endpoint。
- 断线期间动作 API 返回 `503 onebot_disconnected`。
- 对外暴露 `connected/reconnecting/disconnected/error` 状态和最近错误时间。

不能简单替换 `Client.conn`，否则旧 read loop 可能读取新连接，旧 ping goroutine 也可能继续运行。

## 11. NapCat 登录服务

将 `internal/notify/napcat.go` 重构为独立客户端：

- 使用 typed HTTP error 保存状态码，不通过错误字符串判断 401。
- 使用互斥或 singleflight 防止并发重复刷新 credential。
- credential 过期或收到 401 时重新认证一次。
- 状态查询设置短时间缓存，避免页面轮询击穿 NapCat。
- 二维码由后端生成 PNG，NapCat token 不进入浏览器。
- 二维码响应设置 `Cache-Control: no-store` 和正确 Content-Type。
- 二维码内容、credential 和 URL 参数不得进入审计或普通日志。

邮件相关的 SMTP、IMAP、MIME 和“请重发”逻辑不再进入运行路径。旧 `login_notify` 字段在迁移时只读取 NapCat URL/token，其余字段忽略且不再参与启动校验。

## 12. 人工群管服务

新增 `ActionService`，网页 handler 不直接调用 `onebot.Client`：

```text
Admin Handler
  -> 鉴权与 CSRF
  -> 参数校验
  -> Idempotency-Key 检查
  -> ActionService
  -> OneBot Manager
  -> 审计成功 / 失败 / unknown
```

支持禁言、解禁、踢出群、全员禁言、撤回消息和群名片操作。

要求：

- 所有危险动作在前端二次确认。
- 服务端要求 `Idempotency-Key`，按管理员、动作和参数缓存短期结果。
- OneBot 超时返回 `unknown`，因为 QQ 端可能已经成功执行，不能提示用户安全重试。
- API 同时返回 `action_result` 和 `audit_result`；远端动作成功后审计失败无法回滚。
- 撤回前校验消息与群的关联；无法校验时必须在 UI 和审计中明确说明。
- 不提供任意 action 名称和任意参数透传。

## 13. 鉴权与网络安全

### 13.1 管理员认证

- 单管理员账户。
- 密码使用 Argon2id，参数和算法版本随哈希保存。
- 密码文件权限为 `0600`。
- 修改密码需要当前密码，并使全部 session 失效。
- 登录失败按 IP 和全局维度限流。
- **邮箱两步验证**（可选开关，`system.email.enabled`）：
  - 密码正确后向配置的收件邮箱发送 6 位数字验证码，`POST /auth/verify` 校验通过才建立会话；
  - 验证码/票据纯内存存储（重启即失效），只存 SHA-256 哈希，票据 32 字节随机、一次性、10 分钟有效，
    错误 5 次自动作废，同 IP 60 秒重发冷却，与登录限流共用额度；
  - SMTP 授权码 AES-256-GCM 加密落盘（AAD 路径 `system.email.smtp_password`），GET API 只暴露 configured；
  - 发信失败拒绝登录（不降级）；开关默认关闭（未配置 SMTP 时登录不阻塞）。

### 13.2 Session

- 使用至少 256 bit 随机 opaque session ID。
- 服务端内存保存 session，进程重启后全部退出登录。
- Cookie 设置 `HttpOnly`、`Secure`、`SameSite=Strict`。
- 同时限制空闲过期时间和绝对过期时间。
- 不使用 localStorage 保存 session、CSRF 或任何 token。

### 13.3 CSRF 与响应头

- 所有非 GET/HEAD/OPTIONS 请求要求 session 绑定的 CSRF token。
- 校验 `X-CSRF-Token` 和同源 `Origin`。
- 默认关闭 CORS。
- 设置 CSP、`X-Content-Type-Options: nosniff`、`Referrer-Policy: no-referrer` 和 `frame-ancestors 'none'`。
- QR、认证和敏感状态接口统一 `Cache-Control: no-store`。

### 13.4 网络部署

- 应用默认只监听 loopback。
- HTTPS 由 Tailscale、VPN 网关或受信任反向代理终止。
- 只有显式配置的代理地址才允许提供 `X-Forwarded-*`。
- 管理后台、NapCat WebUI 和 OneBot WS 均不得直接暴露公网。
- Docker 中后台加入内部网络，通过 VPN/反向代理访问。

## 14. REST API

统一前缀：`/api/v1`。

### 14.1 认证

```text
POST /auth/setup
POST /auth/login      # 两步开启时返回 {step:"verify", ticket, email(脱敏), resend_after}
POST /auth/verify     # {ticket, code} → 校验验证码并建立会话
POST /auth/logout
GET  /auth/session
PUT  /auth/password
```

### 14.2 状态和系统设置

```text
GET  /status
GET  /settings
PUT  /settings
POST /settings/test-onebot
POST /settings/test-napcat
POST /settings/test-email   # 用当前生效 SMTP 配置发测试邮件
GET  /config/history
POST /config/history/{revision}/restore
```

### 14.3 群配置

```text
GET    /groups
POST   /groups
GET    /groups/{group_id}
PUT    /groups/{group_id}
DELETE /groups/{group_id}
POST   /groups/{group_id}/reset
```

规则和白名单作为完整群 DTO 一次提交。若后续需要局部编辑，再补充细粒度 endpoints；第一版不应同时维护“全量 PUT”和多套独立更新逻辑。

### 14.4 群成员和消息

```text
GET /groups/{group_id}/members?query=&cursor=
GET /groups/{group_id}/messages?cursor=&limit=
```

成员信息由 OneBot 查询。最近消息优先来自机器人接收到的有界内存环形缓冲；若依赖 NapCat 扩展历史接口，必须将其作为可选能力。

### 14.5 人工动作

```text
POST /groups/{group_id}/actions/mute
POST /groups/{group_id}/actions/unmute
POST /groups/{group_id}/actions/kick
POST /groups/{group_id}/actions/whole-ban
POST /groups/{group_id}/actions/recall
POST /groups/{group_id}/actions/card
```

### 14.6 审计和 NapCat

```text
GET  /audit?group_id=&source=&result=&cursor=&limit=
GET  /napcat/status
GET  /napcat/qrcode.png
POST /napcat/qrcode/refresh
```

HTTP 状态约定：

- `400` 请求结构错误。
- `401` 未登录或 session 过期。
- `403` CSRF、来源或权限错误。
- `404` 资源不存在。
- `409` revision 或幂等冲突。
- `422` 业务配置校验失败。
- `502` NapCat/OneBot 返回异常结果。
- `503` setup 未完成、OneBot 断线或组件不可用。

## 15. 页面信息架构

### 15.1 登录与初始化

- 首次 setup token 验证。
- 创建管理员密码。
- 填写机器人 owner、OneBot 和 NapCat 配置。
- 测试连接并添加第一个群。

### 15.2 总览

- OneBot 连接状态和最近错误。
- NapCat 登录状态和二维码。
- 运行时间和配置 revision。
- 已配置/已启用群数量。
- 最近失败或 unknown 动作。

### 15.3 群管理

左侧群列表，右侧为当前群配置：

- 概览和启用状态。
- 新人欢迎。
- 关键词过滤、升级阈值和计分有效期。
- 规则表格，支持新增、编辑、删除和排序；规则顺序影响首条命中结果。
- 刷屏窗口、消息数、禁言时长和重复处罚。
- 入群审批策略。
- 白名单和群备注。

页面保存时携带 revision；发生 `409` 时展示冲突并要求重新加载，不自动覆盖。

### 15.4 人工群管

- 选择群并搜索成员或输入 QQ。
- 禁言、解禁、踢人、全员禁言和设置群名片。
- 从最近消息中选择撤回目标。
- 显示 OneBot 断线、处理中、成功、失败和结果未知状态。

### 15.5 审计和系统设置

- 审计按群、来源、结果和时间筛选。
- 查看配置历史并恢复指定 revision。
- 替换 OneBot/NapCat token，密码输入框不预填。
- 修改管理员密码。
- 只读显示监听地址、数据目录和 master key 状态。

前端使用同源 HTML/CSS/原生 ES Modules，通过 `go:embed` 嵌入。第一屏直接进入管理界面，不增加产品落地页。

## 16. 审计模型

在现有 `AuditEntry` 基础上增加：

```text
request_id
actor_type       admin / automatic / system
actor_id         第一版固定 admin
source_ip
config_revision
action_status    ok / failed / unknown
audit_status
```

审计约束：

- 记录字段名和变更摘要，不记录 secret 值。
- QR 内容、session、CSRF 和 setup token 永不记录。
- OneBot/NapCat 错误先清洗，再返回页面和写入审计。
- 配置成功与审计写入失败是两个独立结果。
- 审计仍可有界保存，但应支持分页查询，而不是一次读取全部记录。

## 17. 数据迁移

首次启动且不存在 `control.json` 时执行：

1. 读取并解析旧 `config.yaml`。
2. 忽略 SMTP/IMAP 完整性要求。
3. 应用 `runtime.json` 群级覆盖。
4. 合并 `aliases.json`。
5. 将 OneBot/NapCat token 加密写入候选配置。
6. 完整校验候选配置。
7. 原子生成 `control.json`。
8. 写入迁移完成标记和系统审计。
9. 原文件保留为只读备份，不再写入。

迁移规则：

- 旧 JSON 解析失败不得静默当成空配置。
- 优先尝试 `.bak`，仍失败则停止迁移并给出恢复路径。
- 数据目录不可写时直接失败。
- 迁移失败不得启动业务 Bot，避免网页显示和实际运行配置不一致。
- 旧 YAML 中的明文 secret 在只读挂载下无法自动删除，部署完成后必须由运维人员移除并轮换。

## 18. 旧入口下线

网页版本切换时，在同一发布版本完成：

- 私聊消息不再进入 `handlePrivateCommand`。
- 生产代码不再构建或注册命令树。
- `main.go` 不再调用 `notify.Run`。
- 旧 `login_notify.enabled` 不再影响启动校验。
- SMTP/IMAP 配置不进入新配置模型。
- 清理不再使用的邮件依赖和部署文档。

旧指令代码可以在第一阶段暂时保留用于迁移对照，但必须没有生产调用路径；稳定后再删除。

## 19. 管理员与密钥恢复

提供独立 CLI：

```text
qqbot admin reset-password --data-dir ... --master-key-file ...
qqbot secrets rotate --data-dir ... --old-key-file ... --new-key-file ...
qqbot config validate --data-dir ... --master-key-file ...
```

恢复要求：

- 默认要求服务停机，避免跨进程同时写文件。
- 若允许在线恢复，必须实现可靠文件锁和 session 失效通知。
- 密码恢复原子更新 Argon2id 哈希。
- 密钥轮换先解密全部 secret，再用新 key 重加密并原子替换配置。
- 操作完成后所有网页登录 session 失效。

## 20. 实施阶段

### 阶段 1：配置核心

- 新增 `BootstrapConfig` 和 HTTP-only setup 生命周期。
- 新增规范化 DTO、`control.json` 和 ConfigService。
- 实现候选配置校验、revision、原子提交、备份恢复。
- 实现 YAML/runtime/aliases 一次性迁移。
- 保持旧机器人行为作为回归基线。

完成标准：配置事务和迁移测试通过，写盘或校验失败不会改变内存和磁盘。

### 阶段 2：组件服务化

- 将 OneBot Client 重构为 generation-aware Manager。
- 抽取 ActionService。
- 抽取 NapCat Client，修复并发 credential 刷新和 401 判断。
- 扩展审计模型。

完成标准：OneBot 可网页配置后安全重连，动作与 QR 能通过 service 层调用。

### 阶段 3：鉴权和 API

- 实现 setup、Argon2id、session、CSRF 和登录限流。
- 实现配置、群、动作、审计和 NapCat REST API。
- 实现 ETag、幂等和统一错误模型。

完成标准：未认证和错误 CSRF 无法读取配置、审计或二维码，API 集成测试通过。

### 阶段 4：网页界面

- 嵌入式管理界面。
- 群配置编辑、规则排序、人工群管和审计页面。
- 登录状态和二维码页面。
- 移动端与桌面端布局验证。

完成标准：主要工作流无需 QQ 私聊、YAML 或邮件即可完成。

### 阶段 5：切换和清理

- 停用 QQ 私聊管理入口。
- 停用 SMTP/IMAP 和 `notify.Run`。
- 更新 Docker、README 和部署文档。
- 执行真实 NapCat/OneBot 环境验收。
- 轮换并移除旧明文凭据。

完成标准：生产进程不存在邮件/IMAP goroutine，私聊命令无响应，网页成为唯一管理入口。

## 21. 测试与验收

### 21.1 配置和迁移

- YAML + runtime + aliases 迁移结果与旧版生效配置一致。
- 非法正则、重复群号和越界参数返回 `422`。
- 失败提交后读取配置仍为原值。
- 写盘失败时内存和磁盘均保持旧版本。
- 并发页面修改产生 revision 冲突。
- `control.json` 损坏时可从 `.bak` 恢复。
- 只读旧配置和可写 data 的 Docker 场景正常迁移。

### 21.2 鉴权和安全

- setup token 只能使用一次。
- 登录、退出、空闲过期和绝对过期行为正确。
- 错误或缺失 CSRF 被拒绝。
- 登录限流生效。
- 密码修改使全部 session 失效。
- API、日志、审计和前端资源不存在 secret 泄漏。
- QR 接口未认证不可访问且禁止缓存。

### 21.3 OneBot 和人工动作

- 修改 endpoint 后旧 generation 完整退出并连接新 endpoint。
- 重连期间动作返回 `503`。
- 六类人工动作参数校验完整。
- 幂等 key 防止浏览器重复提交。
- 超时动作返回 `unknown`。
- 成功、失败和 unknown 均正确写审计。

### 21.4 NapCat

- credential 过期后自动重新认证一次。
- 并发状态和二维码请求无竞态。
- 二维码刷新后返回新内容。
- NapCat 不可用时返回清洗后的 `502/503`。

### 21.5 前端和部署

- 桌面和移动宽度无横向溢出或控件重叠。
- revision 冲突不会静默覆盖其他页面修改。
- 危险动作要求确认并阻止重复点击。
- Docker 重启后配置和密码保留，session 失效。
- 管理端口、NapCat WebUI 和 OneBot WS 未暴露公网。

最终验证命令和场景：

```bash
go test ./...
go test -race ./...
go vet ./...
go build ./...
```

并使用模拟 OneBot WebSocket、模拟 NapCat HTTP 服务、浏览器端到端测试和真实部署各验证一次。

## 22. 主要风险

| 风险 | 处理方式 |
|---|---|
| setup 页面依赖旧配置才能启动 | 拆分 bootstrap 与业务生命周期 |
| 现有更新先改内存后校验 | 使用候选副本和先落盘后切换事务 |
| OneBot 重连时旧 goroutine 读取新连接 | 使用独立 generation/context |
| NapCat credential 并发刷新竞态 | typed error + mutex/singleflight |
| 网页重复提交危险动作 | Idempotency-Key + unknown 结果状态 |
| master key 丢失 | 分离备份并提供停机密钥轮换工具 |
| 旧 YAML 仍含明文 secret | 迁移后删除并轮换凭据 |
| 内网后台被误暴露公网 | loopback 默认、VPN/反代、部署端口检查 |
| QQ 动作成功但审计写入失败 | 分离 action_result 与 audit_result |
| Windows/Docker 原子 rename 差异 | 对 bind mount、断电和恢复做专项测试 |

## 23. 实施约束

- 所有配置入口只能调用 ConfigService，不得直接修改 map、DTO 或文件。
- 所有人工动作只能调用 ActionService，不得从 handler 直接调用任意 OneBot action。
- HTTP 层不得持有或返回解密后的长期 secret。
- 配置修改必须有 revision，危险动作必须有 request ID 和幂等 key。
- 迁移和恢复失败必须显式停止，不允许静默回退为空配置。
- 实现过程中保持一个写入线程，阶段完成后独立审查安全、迁移和用户流程。

本文件作为后续实现、评审和验收的基线。产品范围或安全边界发生变化时，应先更新本文件，再修改代码。

## 真实环境验收记录（2026-08-02）

环境：本机 Docker（NapCat 容器 + qqbot 容器），机器人 QQ `BotPizazz`(3781312810)，测试群 `178055086`（4 成员，机器人管理员）。

| 验收项 | 结果 |
|---|---|
| 网页初始化（setup token 一次性 / 管理员密码） | ✅ |
| NapCat WebUI 认证与二维码（真实 txz.qq.com 码，PNG 后端生成 no-store） | ✅ |
| 二维码刷新（NapCat 异步生成新码） | ✅（刷新后码内容变化） |
| 手机 QQ 扫码登录 | ✅ |
| OneBot WS 连接（ws://napcat:3001） | ✅ connected |
| test-onebot（get_login_info 返回真实账号） | ✅ 登录账号 BotPizazz (3781312810) |
| 群成员查询（真实群） | ✅ 4 成员含角色 |
| 关键词警告（"广告"） | ✅ 自动警告审计 ok |
| 关键词禁言（"开挂"→10 分钟） | ✅ 审计 ok |
| 刷屏检测（连发 10 条） | ✅ 自动禁言审计 ok |
| 配置热生效（新增规则无需重启） | ✅ 新规则即时生效 |
| 人工动作全套（禁言/解禁/名片/全员禁言/撤回） | ✅ 6 项 ok + request_id 审计 |
| 消息记录（网页撤回列表） | ✅ |
| 新人欢迎语（退群加群） | ✅ "@Autumn_Pizazz 欢迎 Autumn_Pizazz 加入本群" |
| 容器重启：配置/密码保留、session 失效 | ✅ |
| 发现并修复：test-onebot 用 get_version 在 NapCat 返回 1404 → 改用 get_login_info；热重连优化（endpoint 未变化不重连） | ✅ |

### 本轮工作成果（2026-08-02 真实环境验收）

除验收表全部通过外，本轮还完成：

1. **真实环境发现并修复 2 个问题**：
   - `test-onebot` 使用 `get_version` 动作在 NapCat 返回 `retcode=1404`（NapCat 未实现该动作）→ 改用 `get_login_info`，返回真实登录账号（`BotPizazz (3781312810)`），并补充响应解析与错误提示。
   - 配置热生效中**群规则修改也会触发 OneBot 重连**（endpoint 未变化时无意义）→ `main.go` 的 Subscribe 回调先比较 endpoint（URL/token/timeout），未变化时只更新 Bot 配置快照、不重连，避免频繁断线。
2. **回归保障**：E2E 测试同步适配 `get_login_info`；`go test ./...`（5 包）+ `go vet` + `go build` 全绿。
3. **部署产物**：`deploy/keys/master.key` 主密钥就位；qqbot 容器以网页化参数（`--data-dir`/`--master-key-file`/`--admin-listen`）运行；验收环境仍在使用中。

### 遗留事项（验收后待办，按优先级）

| # | 事项 | 风险 | 处理方式 | 状态 |
|---|---|---|---|---|
| 1 | **修改初始管理员密码**（验收时设置的临时密码） | 他人可能猜测/复用初始密码进入后台 | 登录网页后台 → 系统设置 → 修改管理员密码 | ✅ 已完成（2026-08-02，用户网页改密成功，旧密码已验证失效）；期间发现并修复：前端静态资源缓存策略 `max-age=3600` → `no-cache`（旧缓存导致改密校验不更新） |
| 2 | **NapCat 端口映射收紧**（当前 `3001/6099` 映射 `0.0.0.0`，暴露公网） | 公网可直接访问 NapCat WebUI（可扫码劫持机器人）与 OneBot WS | 用户决策：**保持 0.0.0.0 映射（省 VPN）**，但已完成最小缓解：① OneBot WS（3001）设置 AccessToken `29e0eb...`（NapCat 配置文件 + qqbot 后台同步，实测无 token 连接被 1403 拒绝，qqbot 带 token 正常）；② WebUI（6099）token 已随机化。**注意**：NapCat 鉴权在 WS 握手后校验（先 101 再 1403），攻击者仍可发起握手但无法使用连接；如需彻底收敛可随时改回 127.0.0.1 | 🟡 已最小缓解（用户决策保持映射） |
| 4 | **邮箱两步验证 SMTP 配置**（2026-08-06 新增功能） | 未配置 SMTP 时无法收到登录验证码（开关默认关闭，不影响登录） | 系统设置 → 邮箱两步验证：填 SMTP 服务器/账号/授权码/收件邮箱 → 发送测试邮件 → 勾选开启并保存。QQ 邮箱用 smtp.qq.com:465，网易用 smtp.163.com:465（授权码在邮箱设置生成） | ⏳ 待用户配置 |
| 3 | **旧明文凭据轮换与清理**（旧 config.yaml 中的 webui_token / 旧主密钥） | 明文凭据留存磁盘有泄露风险；主密钥丢失无法恢复加密凭据 | 删除或迁移旧 config.yaml；如需轮换主密钥：停机执行 `qqbot secrets rotate --data-dir deploy/data --old-key-file <旧> --new-key-file <新>`（自动备份 `.pre-rotate`），轮换后更新挂载密钥并单独备份新密钥 |

> #1 已完成（网页后台为唯一管理入口）；#2 已最小缓解（AccessToken）；#3 无强制时间要求但建议尽早。

### 公网部署产出（2026-08-02）

按"仅暴露 443 + Caddy HTTPS + NapCat 端口收敛内网"的推荐架构产出：
- `deploy/docker-compose.public.yml`：napcat（端口不映射）/ qqbot / caddy 三服务编排。
- `deploy/Caddyfile`：自动 HTTPS 反代，透传 `X-Forwarded-For`。
- `deploy/.env.example`：域名 / NapCat WebUI 密码 / 可信代理网段。
- 代码：新增 `QQBOT_TRUST_PROXY`（逗号分隔 IP/CIDR，仅信任来源采用 `X-Forwarded-For`，
  登录限流与审计按真实客户端 IP；非信任来源伪造 XFF 无效），含单元测试与模拟反代端到端验证。
- `docs/DEPLOYMENT.md` 重写为公网部署手册（安全拓扑/安全组/迁移/密码策略/验证清单/故障排查）。

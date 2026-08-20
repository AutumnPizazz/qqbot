# qqbot notify — QQ 消息传信工具（AI agent 进度通报）

`qqbot notify` 是 qqbot 内置的**机器对机器通知通道**：AI agent / 脚本 / 定时任务
通过 HTTPS 调用云服务器上 qqbot 的通知端点，让机器人**以管理员 QQ 的身份向指定
QQ（私聊）或群（群聊）发送消息**，用于通报 AI agent 工作进度、任务完成、异常告警等。

```
AI agent / 脚本 ──HTTPS──▶ 云服务器 qqbot (/api/v1/notify) ──OneBot WS──▶ NapCat ──▶ 管理员 QQ / 群
```

## 1. 架构

| 组件 | 说明 |
|---|---|
| 服务端 | qqbot 主程序新增 `POST /api/v1/notify` 端点，复用已建立的 OneBot 连接（无需额外暴露 NapCat 端口） |
| 客户端 | 同一二进制内置 `qqbot notify send` 子命令（win11 / linux 各一份） |
| 鉴权 | 独立 Bearer token（环境变量 `QQBOT_NOTIFY_TOKEN`），不依赖浏览器 session，AI agent 无需登录 |
| 通道 | Caddy HTTPS（`https://qqbot.civgo.top:30443`）公网可达 |

## 2. 服务端配置（一次性）

在云服务器上为 qqbot 容器配置通知 token：

```bash
# 1. 生成 token（建议 32 字节 hex）
openssl rand -hex 32

# 2. 写入服务器 .env（docker-compose.public.yml 中 qqbot 服务引用）
echo "QQBOT_NOTIFY_TOKEN=<上一步生成的token>" >> /opt/qqbot/.env

# 3. 重新部署 qqbot 容器（见部署章节）
```

未配置 `QQBOT_NOTIFY_TOKEN` 时，notify 端点返回 503（功能未启用），不影响其他功能。

## 3. 客户端用法

```bash
# 私聊管理员 QQ（推荐：通报 AI agent 进度）
qqbot notify send --token <t> --to 2170191481 "进度：构建完成 ✅"

# 发到群
qqbot notify send --token <t> --group 675179266 "发布完成，请查收"

# 从 stdin 读取长文本（管道传消息）
echo "进度：全部 42 个测试通过" | qqbot notify send --token <t> --to 2170191481

# 位置参数传消息
qqbot notify send --token <t> --to 2170191481 任务已完成，用时 3 分钟
```

### 3.1 参数说明

| 参数 | 说明 |
|---|---|
| `--server <url>` | 通知 API 地址，默认 `https://qqbot.civgo.top:30443`（或环境变量 `QQBOT_NOTIFY_SERVER`） |
| `--token <t>` | 通知 token（或环境变量 `QQBOT_NOTIFY_TOKEN`），必填 |
| `--to <qq>` | 私聊目标 QQ（与 `--group` 二选一） |
| `--group <gid>` | 群聊目标群号（与 `--to` 二选一） |
| `--text <msg>` | 消息内容（优先级最高） |
| `--timeout <sec>` | 请求超时秒数，默认 15 |

消息来源优先级：`--text` > 位置参数 > stdin（非终端时自动读取）。

### 3.2 退出码

| 退出码 | 含义 |
|---|---|
| 0 | 发送成功（机器人已确认收到 message_id） |
| 1 | 请求失败 / 发送失败（token 无效、机器人离线、网络错误等） |
| 2 | 参数错误 |

### 3.3 环境变量（免重复传参）

```bash
export QQBOT_NOTIFY_SERVER="https://qqbot.civgo.top:30443"
export QQBOT_NOTIFY_TOKEN="<token>"
qqbot notify send --to 2170191481 "进度：..."
```

## 4. AI agent 集成示例

在 agent 配置中注册工具（以 pi 为例，可将以下命令加入 AGENTS.md 或自定义工具）：

```markdown
## QQ 消息通报工具
当任务完成、失败或需要人工介入时，用以下命令向管理员通报进度：
- Windows:  C:\path\to\qqbot-windows-amd64.exe notify send --to 2170191481 "<消息>"
- Linux:    /usr/local/bin/qqbot notify send --to 2170191481 "<消息>"
- token 见环境变量 QQBOT_NOTIFY_TOKEN（已配置，无需每次传入）
- 消息 ≤ 5000 字符；退出码 0=成功
```

规则建议：
- **长任务**（编译/测试/批量操作/下载）完成或失败中断时通报
- **任务异常中断**、需要用户介入时通报
- 多条进度合并成一条发送，避免刷屏（QQ 有风控）
- 正文只写关键信息，不含密钥/密码等敏感内容

## 5. API 参考（服务端端点）

```
POST /api/v1/notify
Authorization: Bearer <QQBOT_NOTIFY_TOKEN>
Content-Type: application/json

# 私聊
{"to": 2170191481, "text": "进度：构建完成"}

# 群聊
{"group_id": 675179266, "text": "发布完成"}
```

成功响应 `200`：

```json
{"ok": true, "action": "send_private_msg", "message_id": 123456}
```

错误响应（统一格式）：

| HTTP | code | 含义 |
|---|---|---|
| 401 | unauthorized | token 无效 |
| 422 | validation_failed | 参数错误（to/group 二选一、text 非空 ≤5000 字符） |
| 503 | onebot_disconnected | 机器人离线 / OneBot 未启动 |
| 502 | onebot_failed | 发送失败（含超时，QQ 端可能已收到） |
| 503 | internal | notify 未启用（未配置 token） |

## 6. 重新部署云服务器

代码变更后更新云服务器上的 qqbot 镜像（本地已安装 Docker 时）：

```bash
# 1. 构建镜像并导出（项目根目录）
docker build -t deploy-qqbot:latest .
docker save deploy-qqbot:latest | gzip > deploy/qqbot-image.tar.gz

# 2. 上传到服务器（deploy 目录脚本）
python deploy/quick_upload.py deploy/qqbot-image.tar.gz /opt/qqbot/qqbot-image.tar.gz

# 3. 服务器上加载镜像并重启
python deploy/ssh_deploy.py run "cd /opt/qqbot && docker load < qqbot-image.tar.gz && docker compose -f docker-compose.public.yml up -d --force-recreate qqbot"

# 4. 验证
python deploy/ssh_deploy.py run "docker logs --tail 20 qqbot | grep -i notify || docker logs --tail 20 qqbot"
```

## 7. 安全说明

- token 仅配置在服务端环境变量与可信调用方，**勿提交 git / 勿写入前端**
- 端点走 Caddy HTTPS，明文 HTTP 不提供
- 建议定期轮换 token（改 `.env` 后重启 qqbot 容器）
- 本工具仅做**发信**，不提供收信/指令执行能力（收信由 qqbot 机器人本体处理）

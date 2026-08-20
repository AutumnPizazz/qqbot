# qqbot notify 速查

通过 QQ 消息向管理员传信（AI agent 进度通报）。

## 文件

| 文件 | 说明 |
|---|---|
| `qqbot-windows-amd64.exe` | Win11 可执行文件 |
| `qqbot-linux-amd64` | Linux 可执行文件 |
| `notify_token.txt` | 通知 token（分发时随附） |
| `NOTIFY.md` | 详细文档（架构 / 部署 / API） |

## 配置（一次性，可选）

```bash
export QQBOT_NOTIFY_SERVER="https://qqbot.civgo.top:30443"  # 默认值，可省略
export QQBOT_NOTIFY_TOKEN="<notify_token.txt 内容>"
```

不配置环境变量时，每次调用加 `--token` 参数即可（见下）。

## 用法

```bash
qqbot notify send --to 2170191481 "进度：构建完成 ✅"        # 私聊管理员
echo "全部测试通过" | qqbot notify send --to 2170191481      # 管道传消息
qqbot notify send --group 675179266 "发布完成"               # 发到群
```

参数：`--to <QQ>` 或 `--group <群号>`（二选一）；`--text` / 位置参数 / stdin 均可传消息；`--timeout <秒>` 默认 15。

> Windows PowerShell 调用：`.\qqbot-windows-amd64.exe notify send --token <token> --to 2170191481 "消息"`

## 退出码

| 码 | 含义 |
|---|---|
| 0 | 发送成功 |
| 1 | 失败（token 无效 / 机器人离线 / 网络错误） |
| 2 | 参数错误 |

## 建议

- 长任务完成 / 失败 / 需人工介入时通报；多条合并一条，避免刷屏
- 正文不含密钥等敏感信息
- ⚠️ `notify_token.txt` 可让持有者以机器人身份发消息，请勿公开分享

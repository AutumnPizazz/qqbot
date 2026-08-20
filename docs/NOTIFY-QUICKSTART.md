# qqbot notify 用法

一次性配置命令

```bash
# Linux / macOS / Git Bash
export QQBOT_NOTIFY_TOKEN="<notify_token.txt 内容>"
```

```powershell
# Windows PowerShell
$env:QQBOT_NOTIFY_TOKEN = "<notify_token.txt 内容>"
```

传信命令

```bash
qqbot notify send --to 2170191481 "进度：构建完成 ✅"        # 私聊管理员，默认用此
echo "全部测试通过" | qqbot notify send --to 2170191481      # 管道传消息
qqbot notify send --group 675179266 "发布完成"               # 发到群
```

参数：`--to <QQ>` 或 `--group <群号>`（二选一）；`--text` / 位置参数 / stdin 均可传消息；`--timeout <秒>` 默认 15。

> 未配置环境变量时，每次调用加 `--token <token>` 即可。

| 退出码 | 含义                          |
| --- | --------------------------- |
| 0   | 发送成功                        |
| 1   | 失败（token 无效 / 机器人离线 / 网络错误） |
| 2   | 参数错误                        |

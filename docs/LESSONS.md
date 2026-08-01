> ⚠️ 本文档基于旧版（config.yaml + 私聊指令 + 邮件通知）。阶段 5 起管理入口已迁移到网页后台，部署请以 [README](../README.md) 为准。

# 血泪教训清单（勿再犯）

> 每一条都对应一次真实的线上事故/长时间排障。改代码、部署、配置前先读一遍。

## A. Docker 构建与部署（最痛的三个坑）

### 1. ⛔ 不要在 Dockerfile 里 VOLUME 整个应用目录
**事故**：`VOLUME ["/app"]` 导致每次容器重建都挂载同一个匿名卷，**镜像里的新二进制被旧卷覆盖**，
容器永远运行初始代码。所有功能迭代（私聊指令、备注功能）部署后都"无效"，排查了数小时。

**规则**：
- 不要 `VOLUME ["/app"]` 这种整目录声明；数据卷只挂**数据目录**（如 `/app/data`）
- 旧匿名卷：`docker volume ls` + `docker volume rm <id>` 清理
- 检查容器挂载：`docker inspect <容器> --format '{{json .Mounts}}'`，发现 `volume -> /app` 即中招

### 2. ⛔ 部署后必须验证容器内的代码版本
**事故**：改完代码 `docker compose build` 后以为生效，实际容器里还是旧版。
**"已连接"日志正常、配置加载正常 ≠ 代码是最新的。**

**规则**（每次部署后必做）：
```bash
# 用新代码的特征字符串验证容器内二进制（函数名会被 strip，必须用字符串常量/中文文案）
docker exec qqbot sh -c "grep -a -c '收到消息' /app/qqbot"
# 期望输出 ≥1；输出 0 = 容器还在跑旧代码，立即停手排查
```

### 3. ⛔ 本机 Docker Desktop 的 compose build 构建缓存不可信
**现象**：同一份源码，手动 `docker build` 出来的镜像是对的，`docker compose build` 出来的是旧的，
连 `--no-cache` 都救不了（containerd snapshotter 对 Windows 文件变更的 context 缓存失效 bug）。

**规则**：
- 部署命令固定用手动构建：
  ```bash
  cd <仓库根目录>
  docker build -t deploy-qqbot:latest -f Dockerfile .
  cd deploy && docker compose up -d --force-recreate qqbot
  ```
- 构建后立即做第 2 条的验证

## B. 处罚功能与误伤（血案）

### 4. ⛔ 关键词匹配必须剔除 CQ 码
**事故**：`\d{9,}` 规则匹配原始消息文本，成员 @ 别人时 `[CQ:at,qq=12345678]`
里的 QQ 号命中正则 → **两人被误禁言 60 分钟**，群成员投诉。

**规则**：
- 匹配文本用 `extractText()`（已剔除 `[CQ:...]` 码），禁止直接匹配 `raw_message`
- 正则规则宁严勿宽：`\d{9,}` 这类纯数字正则极易误伤，优先用"关键词 + 语境"组合

### 5. ⛔ 处罚功能必须灰度上线
**顺序**：先开 `warn`（只警告）观察 ≥ 数天 → 确认无误伤 → 再开 `mute` → 最后 `kick`。
关键词规则分级升级（warn_limit/mute_limit）也要逐步放开。

### 6. 误伤应急流程（30 秒内执行）
1. 改 `deploy/config/config.yaml`：所有 `action: mute/kick` 改 `warn`，`warn_limit/mute_limit` 置 0，暂停 flood
2. `docker compose restart qqbot`
3. 解禁：`bin/unban.exe -group <群> -user <QQ1>,<QQ2>`
4. 查根因（如 CQ 码），修复代码后重建

## C. NapCat / QQ 侧

### 7. ⛔ 登录后不要重启 napcat 容器
QQ 检测到进程非正常退出 → 登录态作废 → 每次 `docker restart napcat` 都要重新扫码。
需要改 NapCat 配置时（如 WS 服务端），先备好二维码再重启，且尽量一次性改完。

### 8. NapCat 默认不启用 OneBot WS 服务端
必须手动在 `onebot11_<QQ>.json` 的 `websocketServers` 配置（见 DEPLOYMENT.md 第 5 步），
否则 3001 端口无监听，qqbot 永远连不上。

### 9. ⛔ 不要强杀用户正在使用的 QQ 进程
**事故**：为部署 NapCat 执行 `taskkill /F /IM QQ.exe`，用户日常登录的多个 QQ 会话全部被强制下线，
引发"文件损坏"恐慌（实际文件完好，是 QQ 安全机制）。

**规则**：涉及宿主 QQ 的操作必须先与用户确认；优先用 Docker 隔离方案（NapCat 在容器里）。

### 10. 事件收不到时先分清"推送"与"处理"
用 `cmd/wsmonitor` 对照：宿主机连 `127.0.0.1:3001` vs 容器内连 `napcat:3001`。
wsmonitor 能收到而 qqbot 收不到 → 查 qqbot 二进制版本（第 2 条）；两边都收不到 → 查 NapCat 配置。

## D. 环境与工具链

### 11. 大陆网络下 Docker 镜像拉取
- Docker Hub 直连不通（registry-1.docker.io 被墙）
- 公共镜像加速器大多失效（2026 实测仅少量可用且不稳定）
- **最可靠：用户自己的 HTTP 代理**，配进 Docker Desktop（DEPLOYMENT.md 第 1 步）
- 测速技巧：`curl -x http://127.0.0.1:7897 -o /dev/null -w '%{speed_download}' <大文件URL>`

### 12. git-bash 的 MSYS 路径转换
- `docker run ... /app/qqbot` 会被转成 `C:/Program Files/Git/app/qqbot`
- 解决：命令前加 `MSYS_NO_PATHCONV=1`，或把路径放在 `sh -c "..."` 字符串内

### 13. 下划线开头的 .go 文件会被 go 工具忽略
`_tmp_check.go` 用 `go run` 报 "no Go files"；临时文件用非下划线前缀（如 `ztmp_`）。

### 14. 排查顺序黄金法则：先看日志，再看版本，最后看配置
事件链路：NapCat 收到（napcat 日志"接收 <-"）→ WS 推送（wsmonitor）→ qqbot 处理（qqbot 日志）
→ 发送（napcat 日志"发送 ->"）。每一步都有日志可查，不要凭感觉猜。

### 15. ⛔ 用结构保证行为，不要用穷举检查
**事故**：帮助触发（`?`）最初在每个子命令入口手动判断，新增 `/cfg` 子命令时漏了检查，
`/cfg 社区群 ?` 报"未知子命令"被用户当场抓包。

**教训**：需要"任何位置都成立"的行为（帮助、权限、校验），用**架构**保证而不是在每个分支重复写判断：
- 指令解析重构为**指令树**（`internal/bot/tree.go`）：每个节点自带帮助文本，解析器走到任意节点遇到 `?` 即返回该节点帮助——结构上不可能漏
- 规则：行为要"全局成立"时，先问自己"这个行为能不能由数据/结构推导出来"，能则重构，不要继续打补丁
- 配套：穷举测试（如 `TestQuestionAnywhere` 12 种位置）只能防回归，不能替代结构保证

## 部署后检查清单（Checklist）

```bash
# 1. 容器与镜像
docker inspect qqbot --format '{{json .Mounts}}'        # 无 volume 覆盖 /app
docker exec qqbot sh -c "grep -a -c '<新特征字符串>' /app/qqbot"   # ≥1
# 2. 连接
docker logs qqbot | tail -3                             # "已连接 OneBot"
# 3. 功能
#    私聊机器人发 ?，应收到指令列表
# 4. 数据
ls deploy/data/                                         # aliases.json / runtime.json / audit.json 可写
```

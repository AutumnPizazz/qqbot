# qqbot

[![CI](https://github.com/AutumnPizazz/qqbot/actions/workflows/ci.yml/badge.svg)](https://github.com/AutumnPizazz/qqbot/actions/workflows/ci.yml)
[![Release](https://github.com/AutumnPizazz/qqbot/actions/workflows/release.yml/badge.svg)](https://github.com/AutumnPizazz/qqbot/actions/workflows/release.yml)

基于 **Go + OneBot v11 (NapCat)** 的 QQ 群管理机器人：**网页可视化规则引擎**（事件 → 条件组合 → 动作）
处理关键词/刷屏/新人欢迎/加群审批等行为，内置**网页管理后台**（配置 / 登录二维码 / 人工群管 / 审计），
全部能力网页化，无需私聊指令或邮件。

## 特性

- 🚀 轻量：单二进制部署（前端资源嵌入），内存 ~20MB
- 🧩 **规则引擎**：26 种条件 × 15 种动作可视化组合（AND/OR/取反、continue/break、升级处罚模板、计数器排障）
- 🛡 防骚扰：关键词/正则、刷屏/重复/图片/链接/@轰炸检测、时段宵禁、新人保护期、豁免规则
- 🚪 入群管理：欢迎语、加群审批、邀请审批、入群自动改名
- 🧾 操作审计：配置变更/人工操作/自动处罚分页查询
- 🔐 安全：Argon2id 密码、**邮箱两步验证**、setup token、CSRF、登录限流、凭据 AES-256-GCM 加密落盘
- 🔁 稳定：断线自动重连、发送限速防风控、配置原子落盘 + 即时热生效

## 快速开始（Docker 推荐）

> 部署只需安装 Docker（NapCat 协议端必须容器化），**无需 Go / Make / Python 等环境**。

```bash
cd deploy
openssl rand -hex 32 > keys/master.key     # 主密钥（与数据目录分开备份！）
docker compose up -d --build
docker logs qqbot | grep SETUP_TOKEN       # 取首次 setup token
```

浏览器打开 `http://<宿主机IP>:18091` → 粘贴 setup token → 设置密码 → 填 OneBot/NapCat 配置 → 添加群。
**配置保存后机器人自动启动**，之后所有管理都在网页完成。

> - 管理端口仅映射内网（127.0.0.1），请通过 VPN/反向代理访问，勿直接暴露公网
> - 机器人需在目标群拥有**管理员**权限
> - 二进制方式：`go build -o bin/qqbot . && ./bin/qqbot --data-dir data --master-key-file master.key`

## 预构建产物（GitHub Actions 自动发布）

**stable 分支**每次更新后自动构建发布，无需本地 Go 环境：

- **Docker 镜像**（linux/amd64 + arm64 多架构）推送到 GHCR，`latest` 始终跟随最新 stable：

  ```bash
  docker pull ghcr.io/autumnpizazz/qqbot:latest   # 或 :stable
  ```

  首次使用需在 GitHub → Packages 中把该镜像设为 **public** 才能免登录拉取；
  compose 改用预构建镜像：删掉 `build:` 段、加 `image: ghcr.io/autumnpizazz/qqbot:latest`（见 deploy/docker-compose.yml 注释）。
- **二进制**（Linux / Windows / macOS × amd64 / arm64，附 SHA256SUMS）发布在 [Releases](https://github.com/AutumnPizazz/qqbot/releases)，每个对应一个 `stable-<commit>` Release。

  运行：`./qqbot --data-dir data --master-key-file master.key`

## 网页后台速览

| 页面 | 能力 |
|---|---|
| 登录 / 初始化 | setup token、密码 + 邮箱验证码两步登录 |
| 总览 | OneBot/NapCat 状态、QQ 登录二维码、运行时间 |
| 群管理 | 群增删/启停、**规则列表**（AND/OR/取反组合、升级模板、排序、计数器排障）、白名单 |
| 人工群管 | 禁言/解禁/移出/全员禁言/撤回/名片（幂等防重复） |
| 审计 | 按群/来源/结果分页查询 |
| 系统设置 | OneBot/NapCat 凭据、**邮箱两步验证（SMTP）**、配置历史恢复、改密 |

## 配置与数据

- `data/control.json` 是**唯一配置源**（网页修改，事务写入 + revision 冲突检测 + 历史恢复）
- 敏感凭据（OneBot/NapCat token、SMTP 授权码）AES-256-GCM 加密落盘，由 `keys/master.key` 解密
- 旧版 `config.yaml` 用户：`--import-config` 一次性迁移（见下）；v1 → v2 配置启动时自动迁移

| 启动参数 / 环境变量 | 默认 | 说明 |
|---|---|---|
| `--admin-listen` / `QQBOT_ADMIN_LISTEN` | `127.0.0.1:8080` | 管理后台监听地址 |
| `--data-dir` / `QQBOT_DATA_DIR` | `data` | 数据目录 |
| `--master-key-file` / `QQBOT_MASTER_KEY_FILE` | 必填 | 主密钥（32 字节 hex/base64） |
| `--import-config` / `QQBOT_IMPORT_CONFIG` | 空 | 一次性迁移旧 config.yaml |

## 运维

```bash
qqbot config validate --data-dir data --master-key-file master.key   # 校验配置与密钥匹配
qqbot secrets rotate --data-dir data --old-key-file k1 --new-key-file k2  # 轮换主密钥（停机）
qqbot admin reset-password --data-dir data                          # 重置管理员密码（停机）
./deploy.sh                 # 构建并更新 Docker 服务（开发用；Linux/macOS 或 Git Bash）
pwsh ./deploy.ps1           # 同上（Windows 原生，PowerShell 7+；需 Docker Desktop）
```

## 迁移服务器 / 备份

迁移到新机器**必须携带**：`data/`（配置+加密凭据）、`keys/master.key`（主密钥，与 data 配对）、`.env`。
只丢主密钥 = 所有加密凭据（SMTP 授权码等）永久无法解密；建议 master.key 单独冷备。
完整清单与步骤见 [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) §4。

## 文档

| 文档 | 内容 |
|---|---|
| [`docs/WEB_ADMIN_DESIGN.md`](docs/WEB_ADMIN_DESIGN.md) | 网页后台设计基线、鉴权、API、验收记录 |
| [`docs/RULE_ENGINE_DESIGN.md`](docs/RULE_ENGINE_DESIGN.md) | 规则引擎设计（条件/动作清单、迁移映射、组合模式） |
| [`docs/NOTIFY.md`](docs/NOTIFY.md) | QQ 消息传信工具（AI agent 进度通报：`qqbot notify send`） |
| [`docs/DEPLOYMENT.md`](docs/DEPLOYMENT.md) | 云服务器公网部署、迁移清单、Caddy/安全组 |

## 开发

```bash
go run . --data-dir data --master-key-file master.key   # 本地运行（需先准备主密钥）
# 交叉编译到 Linux 服务器（或直接 docker build，见 Dockerfile）
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -trimpath -ldflags "-s -w" -o bin/qqbot-linux-amd64 .
go test ./...       # 测试（含 E2E：mock OneBot/NapCat 全生命周期）
go test -race ./... # 竞态检测
```

```
internal/
├── admin/    # HTTP 后台：鉴权/session/API/嵌入式前端
├── rules/    # 规则引擎：DTO/注册表/条件动作/计数器/迁移
├── state/    # control.json：加密/事务/迁移/历史/密钥轮换
├── bot/      # 事件管线（引擎执行）、ActionService、审计
├── onebot/   # OneBot v11 连接管理器
├── napcat/   # NapCat WebUI 客户端（二维码）
├── mailer/   # SMTP 发送器（登录验证码）
├── fsutil/   # 原子写盘
└── config/   # 旧 YAML 结构（迁移输入）
```

## 验收状态

`go test ./...`（含 E2E）、`-race`、`go vet` 全绿；Docker + 真实 NapCat 环境已验收：
规则引擎（升级处罚/刷屏/欢迎/审批）、人工群管全套、邮箱两步验证、二维码、配置热生效、迁移自动转换均正常。
真实环境验收记录见 [`docs/WEB_ADMIN_DESIGN.md`](docs/WEB_ADMIN_DESIGN.md)。

## 许可证

本项目基于 **GNU General Public License v3.0 (GPL-3.0)** 发布（`LICENSE` / `COPYING`，GNU 官网原文
<https://www.gnu.org/licenses/gpl-3.0.txt>）。

```text
Copyright (C) 2026 AutumnPizazz
This program is free software: you can redistribute it and/or modify
it under the terms of the GNU General Public License as published by
the Free Software Foundation, either version 3 of the License, or
(at your option) any later version.
```

## 免责声明

本项目仅供学习交流，按 GPL-3.0 条款以"现状"提供、不附带任何担保。
请遵守腾讯 QQ 平台规则与相关法律法规，控制使用频率，勿用于骚扰他人。

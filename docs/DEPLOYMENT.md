# 云服务器公网部署手册（网页化管理端）

> 适用：qqbot 阶段 5 后版本（网页后台唯一管理入口）。
> 本文档覆盖：安全拓扑、云安全组、Caddy HTTPS、数据迁移、初始化、日常运维。
> 本地部署（无公网）见 [README](../README.md)「快速开始」。

## 0. 安全拓扑（务必先读）

```
公网 ──► 云安全组（仅 80/443）──► Caddy（自动 HTTPS）──► qqbot 后台 8080（compose 内网）
                                                            │
                                    ┌───────────────────────┼────────────┐
                                    ▼                       ▼            ▼
                              NapCat 6099 (WebUI)      NapCat 3001 (OneBot WS, 有 token)
                              （不映射公网）             （不映射公网）
```

原则：
1. **公网只暴露 443**（+80 供证书申请）。管理后台必须走 HTTPS（Secure Cookie + 密码加密）。
2. **NapCat 的 3001/6099 不映射宿主机端口**，仅 compose 内网可达（qqbot 通过 `ws://napcat:3001` / `http://napcat:6099` 访问）。
3. 扫码登录机器人 QQ：登录管理后台 → 总览页二维码（后端生成，无需暴露 NapCat WebUI）。
4. 服务器本机维护 NapCat WebUI 用 SSH 隧道：`ssh -L 6099:127.0.0.1:6099 user@server`（临时把端口映射到 127.0.0.1 也可以）。

## 1. 前置准备

- 云服务器（Linux amd64，2C2G 起）、Docker + Compose 插件
- 一个域名（推荐；纯 IP 可用 Caddy 自签，浏览器需信任证书）
- 手机 QQ（机器人小号）、测试群

## 2. 云安全组清单

| 端口 | 协议 | 放行来源 | 用途 |
|---|---|---|---|
| 443 | TCP | 0.0.0.0/0 | HTTPS 管理后台（Caddy） |
| 80 | TCP | 0.0.0.0/0 | Let's Encrypt 证书申请（HTTP-01） |
| 22 | TCP | **仅你的 IP** | SSH 运维 |
| 3001/6099/8080/18091 | — | **不放行** | NapCat / qqbot 仅内网 |

## 3. 部署步骤

```bash
# 3.1 拉取项目并进入 deploy
git clone <你的仓库> qqbot && cd qqbot/deploy

# 3.2 配置
cp .env.example .env
vim .env                      # 填 QQBOT_HOST=你的域名；生成 NAPCAT_WEBUI_SECRET_KEY
openssl rand -hex 32 > keys/master.key && chmod 600 keys/master.key

# 3.3 启动
docker compose -f docker-compose.public.yml up -d --build

# 3.4 首次初始化
docker logs qqbot | grep SETUP_TOKEN     # 复制 token
# 浏览器打开 https://<域名> → 粘贴 token → 设置强密码 → 填写 OneBot/NapCat 配置 → 添加群
```

> OneBot 地址填 `ws://napcat:3001`；NapCat WebUI 填 `http://napcat:6099`，token 填 `.env` 里的 `NAPCAT_WEBUI_SECRET_KEY`。

## 4. 从旧环境迁移数据

### 4.1 完整迁移清单（缺一不可）

| 内容 | 位置 | 说明 | 丢失后果 |
|---|---|---|---|
| `data/` | 旧机器数据目录 | control.json（**加密凭据** + 全部配置）、auth.json（密码哈希）、audit.json（审计） | 配置丢失，需重新初始化 |
| `keys/master.key` | 主密钥文件 | AES-256-GCM 解密钥匙，**必须与 control.json 配对迁移** | SMTP 授权码 / OneBot / NapCat token **永久无法解密** |
| `.env` | deploy/ 下 | NapCat WebUI 密码固化（NAPCAT_WEBUI_SECRET_KEY） | NapCat 密码变随机，网页后台 token 失配 |
| `docker-compose*.yml` + `Dockerfile` | deploy/ 与仓库根目录 | 部署编排与镜像构建定义 | 无则无法启动 |
| NapCat 登录态 | docker volume `napcat-qq` | QQ 登录态（换机免扫码） | 需重新扫码登录（网页后台可生成二维码，**非必须迁移**） |

### 4.2 迁移步骤

```bash
# 1. 旧机器：打包（keys 与 data 一起，注意权限）
tar czf qqbot-migrate.tar.gz deploy/data deploy/keys deploy/.env deploy/docker-compose.yml Dockerfile

# 2. 新机器：解压到仓库结构相同位置
tar xzf qqbot-migrate.tar.gz

# 3. 验证主密钥与配置配对（关键！输出校验通过 = 凭据可解密）
docker run --rm -v $PWD/deploy/data:/data -v $PWD/deploy/keys:/keys:ro deploy-qqbot:latest   config validate --data-dir /data --master-key-file /keys/master.key

# 4. 启动
docker compose -f deploy/docker-compose.yml up -d --build

# 5. 上线检查：网页登录（含邮箱两步验证码）→ 总览页 OneBot 已连接 → 群规则列表完整
```

> 只迁移 `data/` 而丢了 `master.key`，或两者不配对（如只复制了其中一个），
> 启动会报"解密失败（主密钥与配置不匹配？）"，所有加密凭据需重新配置。
> 建议：`master.key` 单独冷备（密码管理器/离线存储），与 data 分开保存。

旧版 config.yaml 用户：首次启动加 `--import-config`（或迁移后复制 control.json）。

## 5. 密码策略（公网强制要求）

- 管理员密码：**至少 12 位混合字符**（公网环境不设下限，靠自觉强度）
- 登录限流已内置：单 IP 15 分钟内 10 次失败、全局 100 次
- 定期轮换；泄露立即改（改密使全部会话失效）
- 初始 setup token 只输出一次，用后即焚

## 6. 反向代理与真实 IP

Caddy 反代已配置（`deploy/Caddyfile`）：
- 透传 `X-Forwarded-For`，qqbot 侧通过 `QQBOT_TRUST_PROXY=172.16.0.0/12` 信任 compose 内网
- 登录限流/审计按**真实客户端 IP** 记录（非代理 IP）
- 非信任来源伪造 `X-Forwarded-For` 无效（只取 RemoteAddr）

## 7. 日常运维

```bash
docker compose -f docker-compose.public.yml logs -f qqbot   # 日志
docker compose -f docker-compose.public.yml restart qqbot   # 重启（配置/密码保留，会话失效）
docker compose -f docker-compose.public.yml up -d --build   # 更新代码后重建

# 停机维护 CLI（先停服务）
docker compose -f docker-compose.public.yml stop qqbot
docker run --rm -v $PWD/data:/data -v $PWD/keys:/keys:ro qqbot:accept \
  config validate --data-dir /data --master-key-file /keys/master.key
```

## 8. 上线验证清单

- [ ] `https://<域名>` 能打开登录页，HTTP 访问自动跳转 HTTPS
- [ ] setup 初始化完成，总览页 OneBot `connected`、NapCat 已登录
- [ ] 群内发关键词消息 → 自动处罚；网页动作正常
- [ ] 总览页二维码可显示/刷新（未登录场景）
- [ ] 改密后旧会话退出
- [ ] 服务器 `ss -tlnp | grep -E '3001|6099'` 无监听（仅容器内网）
- [ ] 安全组无多余端口（3001/6099/8080 未放行）
- [ ] 审计记录含真实 IP（非 172.16.x 代理 IP）

## 9. 故障排查

| 现象 | 检查 |
|---|---|
| 登录后跳回登录页 | 是否 HTTPS？Secure Cookie 在 HTTP 下不发送 |
| 证书申请失败 | 域名 DNS 解析到服务器？80 端口放行？ |
| OneBot 一直 connecting | NapCat 是否已扫码登录（未登录时 WS 服务不启动）；token 是否与 napcat 配置一致 |
| 限流误伤（自己也被限） | `QQBOT_TRUST_PROXY` 是否正确（含 Caddy 容器所在网段） |

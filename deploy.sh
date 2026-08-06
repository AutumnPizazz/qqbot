#!/usr/bin/env bash
# 构建并部署更新 qqbot Docker 服务（替代原 deploy.py，逻辑一致）
#
# 用法：
#   ./deploy.sh              # 构建镜像并更新 qqbot 容器（不动 napcat）
#   ./deploy.sh --no-cache   # 构建时不用 Docker 缓存
#   ./deploy.sh --logs       # 部署完成后跟踪容器日志（Ctrl+C 退出，不影响运行）
#
# Windows 用户请用 deploy.ps1（PowerShell 7+，无需 Git Bash / WSL）。
# 流程：docker build → docker compose up -d --force-recreate qqbot → 验证容器内代码版本。
# 依赖：本机已安装 Docker（Docker Desktop / Docker Engine）并已启动。
#
# ⚠️ 遵循 docs/LESSONS.md 第 1-3 条：
#   - 手动 docker build（compose build 在本机 Docker Desktop 存在构建缓存失效 bug）
#   - 镜像 tag 必须为 deploy-qqbot:latest（与 compose 默认镜像名 deploy-qqbot 一致，否则 up 会重新构建）
#   - 部署后必须验证容器内二进制为最新代码
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"
COMPOSE_FILE="$ROOT/deploy/docker-compose.yml"
MASTER_KEY="$ROOT/deploy/keys/master.key"
SERVICE="qqbot"
IMAGE="deploy-qqbot:latest"                       # 必须与 compose 默认镜像名匹配
# 容器内二进制验证特征字符串（函数名会被 strip，必须用字符串常量，见 docs/LESSONS.md 第 2 条）
VERSION_PROBE="收到消息"

# ---- 参数解析 ----
NO_CACHE=""
LOGS=""
for arg in "$@"; do
  case "$arg" in
    --no-cache) NO_CACHE="--no-cache" ;;
    --logs)     LOGS="1" ;;
    -h|--help)  sed -n '2,9p' "$0"; exit 0 ;;
    *) echo "未知参数: $arg（支持 --no-cache / --logs）" >&2; exit 1 ;;
  esac
done

# ---- 环境检查 ----
if ! command -v docker >/dev/null 2>&1; then
  echo "[错误] 未检测到 docker，请先安装并启动 Docker Desktop。" >&2
  exit 1
fi
if [ ! -f "$COMPOSE_FILE" ]; then
  echo "[错误] 未找到 $COMPOSE_FILE，请确认在项目根目录运行。" >&2
  exit 1
fi
if [ ! -f "$MASTER_KEY" ]; then
  echo "[警告] 未找到主密钥 $MASTER_KEY。"
  echo "       请先执行：openssl rand -hex 32 > deploy/keys/master.key"
  echo "       （首次启动后从 docker logs qqbot 获取 setup token 完成网页初始化）"
fi

compose=(docker compose -f "$COMPOSE_FILE")

# 1. 手动构建镜像（不信任 compose build，见 docs/LESSONS.md 第 3 条）
echo ">>> docker build $NO_CACHE -t $IMAGE -f Dockerfile ."
docker build $NO_CACHE -t "$IMAGE" -f "$ROOT/Dockerfile" "$ROOT"

# 2. 强制重建 qqbot 容器（NapCat 不受影响）
"${compose[@]}" up -d --force-recreate "$SERVICE"

# 3. 验证容器内二进制是最新代码（debian-slim 自带 sh/grep，直接在容器内查询）
echo ">>> 验证容器内代码版本..."
count="$(docker exec "$SERVICE" sh -c "grep -a -c '$VERSION_PROBE' /app/qqbot" 2>/dev/null || true)"
if [ "${count:-0}" -ge 1 ]; then
  echo "[OK] 容器内二进制包含特征字符串 '$VERSION_PROBE'（命中 $count 处），代码为最新版。"
else
  echo "[警告] 容器内二进制未检出特征字符串 '$VERSION_PROBE'。" >&2
  echo "       容器可能仍在运行旧代码！请检查：镜像构建是否包含最新源码、卷是否覆盖了 /app。" >&2
  exit 1
fi

# 4. 显示容器状态
"${compose[@]}" ps "$SERVICE"

# 5. 可选：跟踪日志
if [ -n "$LOGS" ]; then
  echo ">>> 跟踪 qqbot 日志（Ctrl+C 退出，容器继续运行）"
  "${compose[@]}" logs -f --tail 30 "$SERVICE"
fi

echo
echo "部署完成。常用命令："
echo "    docker compose -f $COMPOSE_FILE logs -f qqbot   # 查看日志"
echo "    docker compose -f $COMPOSE_FILE restart qqbot   # 重启服务"
echo "    docker compose -f $COMPOSE_FILE down            # 停止全部服务"

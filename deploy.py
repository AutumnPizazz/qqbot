#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""
快速构建并部署更新 qqbot Docker 服务。

用法：
    python deploy.py              # 构建镜像并更新 qqbot 容器（不动 napcat）
    python deploy.py --no-cache   # 构建时不用 Docker 缓存
    python deploy.py --logs       # 部署完成后跟踪容器日志（Ctrl+C 退出，不影响运行）

流程：docker build → docker compose up -d --force-recreate qqbot → 验证容器内代码版本。
依赖：本机已安装 Docker（Docker Desktop / Docker Engine）并已启动。

⚠️ 遵循 LESSONS.md 第 1-3 条：
  - 手动 docker build（compose build 在本机 Docker Desktop 存在构建缓存失效 bug）
  - 镜像 tag 必须为 deploy-qqbot:latest（与 compose 默认镜像名 deploy-qqbot 一致，否则 up 会重新构建）
  - 部署后必须验证容器内二进制为最新代码
"""
import argparse
import shutil
import subprocess
import sys
from pathlib import Path

ROOT = Path(__file__).resolve().parent
COMPOSE_FILE = ROOT / "deploy" / "docker-compose.yml"     # compose 文件在 deploy/ 下
MASTER_KEY = ROOT / "deploy" / "keys" / "master.key"  # 主密钥（openssl rand -hex 32 > keys/master.key，不入库）
SERVICE = "qqbot"
IMAGE = "deploy-qqbot:latest"                             # 必须与 compose 默认镜像名匹配
# 容器内二进制验证特征字符串（函数名会被 strip，必须用字符串常量，见 LESSONS.md 第 2 条）
VERSION_PROBE = "收到消息"


def run(cmd: list[str]) -> int:
    """执行命令并透传输出，返回退出码。"""
    print(f"\n>>> {' '.join(cmd)}", flush=True)
    return subprocess.run(cmd).returncode


def precheck() -> bool:
    """部署前环境检查。"""
    if shutil.which("docker") is None:
        print("[错误] 未检测到 docker，请先安装并启动 Docker Desktop。")
        return False
    if not COMPOSE_FILE.exists():
        print(f"[错误] 未找到 {COMPOSE_FILE}，请确认在项目根目录运行。")
        return False
    if not MASTER_KEY.exists():
        print(f"[警告] 未找到主密钥 {MASTER_KEY}。\n"
              f"       请先执行：openssl rand -hex 32 > deploy/keys/master.key\n"
              f"       （首次启动后从 docker logs qqbot 获取 setup token 完成网页初始化）")
    return True


def main() -> int:
    ap = argparse.ArgumentParser(description="构建并部署更新 qqbot Docker 服务")
    ap.add_argument("--no-cache", action="store_true", help="构建时禁用 Docker 缓存")
    ap.add_argument("--logs", action="store_true", help="部署完成后跟踪容器日志")
    args = ap.parse_args()

    if not precheck():
        return 1

    compose = ["docker", "compose", "-f", COMPOSE_FILE.as_posix()]

    # 1. 手动构建镜像（不信任 compose build，见 LESSONS.md 第 3 条）
    build_cmd = ["docker", "build", "-t", IMAGE, "-f", "Dockerfile", "."]
    if args.no_cache:
        build_cmd.append("--no-cache")
    if run(build_cmd) != 0:
        print("\n[错误] 镜像构建失败，请检查上方编译输出。")
        return 1

    # 2. 强制重建 qqbot 容器（NapCat 不受影响）
    if run([*compose, "up", "-d", "--force-recreate", SERVICE]) != 0:
        print("\n[错误] 容器启动失败，请检查配置或日志。")
        return 1

    # 3. 验证容器内二进制是最新代码（LESSONS.md 第 2 条，防止旧代码被卷/缓存覆盖）
    print("\n>>> 验证容器内代码版本...")
    probe = ["docker", "exec", SERVICE,
             "sh", "-c", f"grep -a -c '{VERSION_PROBE}' /app/qqbot"]
    r = subprocess.run(probe, capture_output=True, text=True)
    count = r.stdout.strip()
    if r.returncode == 0 and count.isdigit() and int(count) >= 1:
        print(f"[OK] 容器内二进制包含特征字符串 '{VERSION_PROBE}'（命中 {count} 处），代码为最新版。")
    else:
        print(f"[警告] 容器内二进制未检出特征字符串 '{VERSION_PROBE}'（grep 退出码 {r.returncode}）。\n"
              f"       容器可能仍在运行旧代码！请检查：镜像构建是否包含最新源码、卷是否覆盖了 /app。")
        return 1

    # 4. 显示容器状态
    run([*compose, "ps", SERVICE])

    # 5. 可选：跟踪日志
    if args.logs:
        print("\n>>> 跟踪 qqbot 日志（Ctrl+C 退出，容器继续运行）")
        return run([*compose, "logs", "-f", "--tail", "30", SERVICE])

    print("\n部署完成。常用命令：")
    print(f"    docker compose -f {COMPOSE_FILE.as_posix()} logs -f qqbot   # 查看日志")
    print(f"    docker compose -f {COMPOSE_FILE.as_posix()} restart qqbot   # 重启服务")
    print(f"    docker compose -f {COMPOSE_FILE.as_posix()} down            # 停止全部服务")
    return 0


if __name__ == "__main__":
    sys.exit(main())

<#
.SYNOPSIS
    构建并部署更新 qqbot Docker 服务（Windows 版，与 deploy.sh 功能等价）。
.DESCRIPTION
    流程：docker build → docker compose up -d --force-recreate qqbot → 验证容器内代码版本。
    ⚠️ 遵循 docs/LESSONS.md 第 1-3 条：
      - 手动 docker build（compose build 在本机 Docker Desktop 存在构建缓存失效 bug）
      - 镜像 tag 必须为 deploy-qqbot:latest（与 compose 默认镜像名一致，否则 up 会重新构建）
      - 部署后必须验证容器内二进制为最新代码
.PARAMETER NoCache
    构建时禁用 Docker 缓存。
.PARAMETER Logs
    部署完成后跟踪容器日志（Ctrl+C 退出，不影响运行）。
.EXAMPLE
    pwsh ./deploy.ps1
.EXAMPLE
    pwsh ./deploy.ps1 -NoCache -Logs
.NOTES
    需要 Docker Desktop 已安装并启动；若被 ExecutionPolicy 拦截，可用：
    powershell -ExecutionPolicy Bypass -File deploy.ps1
#>
[CmdletBinding()]
param(
    [switch]$NoCache,   # 构建时禁用 Docker 缓存
    [switch]$Logs       # 部署完成后跟踪容器日志
)

Set-StrictMode -Version Latest
$ErrorActionPreference = 'Stop'

$Root        = $PSScriptRoot
$ComposeFile = Join-Path $Root 'deploy' 'docker-compose.yml'
$MasterKey   = Join-Path $Root 'deploy' 'keys' 'master.key'
$Dockerfile  = Join-Path $Root 'Dockerfile'
$Service     = 'qqbot'
$Image       = 'deploy-qqbot:latest'  # 必须与 compose 默认镜像名匹配
# 容器内二进制验证特征字符串（函数名会被 strip，必须用字符串常量，见 docs/LESSONS.md 第 2 条）
$VersionProbe = '收到消息'

function Write-Step([string]$Message) {
    Write-Host "`n>>> $Message" -ForegroundColor Cyan
}

function Invoke-Native {
    # 执行原生命令并检查退出码，失败即抛错（$ErrorActionPreference 对原生命令无效，必须查 $LASTEXITCODE）
    param([scriptblock]$Command, [string]$What)
    & $Command
    if ($LASTEXITCODE -ne 0) { throw "$What 失败（退出码 $LASTEXITCODE）" }
}

# ---- 环境检查 ----
if (-not (Get-Command docker -ErrorAction SilentlyContinue)) {
    throw '未检测到 docker，请先安装并启动 Docker Desktop。'
}
if (-not (Test-Path $ComposeFile)) {
    throw "未找到 $ComposeFile，请确认在项目根目录运行。"
}
if (-not (Test-Path $MasterKey)) {
    Write-Host "[警告] 未找到主密钥 $MasterKey" -ForegroundColor Yellow
    Write-Host '       请先执行：openssl rand -hex 32 > deploy/keys/master.key'
    Write-Host '       （首次启动后从 docker logs qqbot 获取 setup token 完成网页初始化）'
}

$compose = @('compose', '-f', $ComposeFile)

# 1. 手动构建镜像（不信任 compose build，见 docs/LESSONS.md 第 3 条）
    $buildArgs = @('build')
    if ($NoCache) { $buildArgs += '--no-cache' }
    $buildArgs += @('-t', $Image, '-f', $Dockerfile, $Root)
    Write-Step "docker $($buildArgs -join ' ')"
    Invoke-Native { & docker @buildArgs } '镜像构建'

    # 2. 强制重建 qqbot 容器（NapCat 不受影响）
    Invoke-Native { & docker @compose up -d --force-recreate $Service } '容器启动'

    # 3. 验证容器内二进制是最新代码（debian-slim 自带 sh/grep，直接在容器内查询）
    Write-Step '验证容器内代码版本...'
    $count = (& docker exec $Service sh -c "grep -a -c '$VersionProbe' /app/qqbot" 2>$null | Out-String).Trim()
    if ($count -match '^\d+$' -and [int]$count -ge 1) {
        Write-Host "[OK] 容器内二进制包含特征字符串 '$VersionProbe'（命中 $count 处），代码为最新版。" -ForegroundColor Green
    }
    else {
        throw "容器内二进制未检出特征字符串 '$VersionProbe'。`n       容器可能仍在运行旧代码！请检查：镜像构建是否包含最新源码、卷是否覆盖了 /app。"
    }

    # 4. 显示容器状态
    Invoke-Native { & docker @compose ps $Service } 'docker compose ps'

    # 5. 可选：跟踪日志
    if ($Logs) {
        Write-Step '跟踪 qqbot 日志（Ctrl+C 退出，容器继续运行）'
        & docker @compose logs -f --tail 30 $Service
    }

    Write-Host "`n部署完成。常用命令：" -ForegroundColor Cyan
    Write-Host "    docker compose -f $ComposeFile logs -f qqbot   # 查看日志"
    Write-Host "    docker compose -f $ComposeFile restart qqbot   # 重启服务"
    Write-Host "    docker compose -f $ComposeFile down            # 停止全部服务"

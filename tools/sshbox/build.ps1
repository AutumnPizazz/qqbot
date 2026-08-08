<#
.SYNOPSIS
    构建 sshbox.exe —— 云服务器 SSH 连接工具箱（凭据内置版）。

.DESCRIPTION
    读取 server-info.txt（或环境变量），把 SSH 连接凭据以 base64 编码注入
    编译产物。源码与 git 仓库始终不含任何涉密信息；只有构建产物携带凭据，
    拿到 exe 即可连接服务器，使用方无需接触密码。

    ⚠️ 安全须知：exe 内凭据为可提取的明文（strings 命令可查），
       请仅分发给可信人员；若需更严格防护请改用私钥认证 + 构建后删除私钥。

.PARAMETER Conf
    server-info.txt 路径。默认自动探测：
    1. 环境变量 SSHBOX_HOST / SSHBOX_PASSWORD ...
    2. ./server-info.txt（本目录）
    3. ../deploy/server-info.txt（qqbot 项目结构）

.PARAMETER Out
    输出 exe 路径（默认 .\sshbox.exe）。

.PARAMETER NoStrip
    不剥离调试符号（产物更大，仅排障用）。

.EXAMPLE
    .\build.ps1                      # 自动探测配置并构建
    .\build.ps1 -Conf ..\..\deploy\server-info.txt
    .\build.ps1 -Out dist\sshbox.exe
#>
[CmdletBinding()]
param(
    [string]$Conf = "",
    [string]$Out = ".\sshbox.exe",
    [switch]$NoStrip
)

$ErrorActionPreference = 'Stop'
$Root = $PSScriptRoot

function Read-ServerInfo([string]$Path) {
    $cfg = @{}
    foreach ($line in Get-Content -Path $Path -Encoding UTF8) {
        $line = $line.Split('#')[0].Trim()
        if (-not $line -or -not $line.Contains('=')) { continue }
        $k, $v = $line.Split('=', 2)
        $cfg[$k.Trim()] = $v.Trim()
    }
    return $cfg
}

# ---- 探测配置来源 ----
$cfg = @{}
if ($Conf) {
    if (-not (Test-Path $Conf)) { throw "指定的配置文件不存在: $Conf" }
    $cfg = Read-ServerInfo $Conf
    Write-Host "[配置] 使用 $Conf"
} else {
    foreach ($cand in @((Join-Path $Root 'server-info.txt'),
                        (Join-Path $Root '..\..\deploy\server-info.txt'))) {
        if (Test-Path $cand) { $cfg = Read-ServerInfo $cand; Write-Host "[配置] 使用 $cand"; break }
    }
    if (-not $cfg.Count) { Write-Host "[配置] 未找到 server-info.txt，改用环境变量 SSHBOX_*" }
}

$host_   = if ($env:SSHBOX_HOST)      { $env:SSHBOX_HOST }      elseif ($cfg['SSH_HOST'])     { $cfg['SSH_HOST'] }     else { '' }
$port_   = if ($env:SSHBOX_PORT)      { $env:SSHBOX_PORT }      elseif ($cfg['SSH_PORT'])     { $cfg['SSH_PORT'] }     else { '22' }
$user_   = if ($env:SSHBOX_USER)      { $env:SSHBOX_USER }      elseif ($cfg['SSH_USER'])     { $cfg['SSH_USER'] }     else { 'root' }
$pass_   = if ($env:SSHBOX_PASSWORD)  { $env:SSHBOX_PASSWORD }  elseif ($cfg['SSH_PASSWORD']) { $cfg['SSH_PASSWORD'] } else { '' }

if (-not $host_)  { throw '缺少 SSH_HOST：请提供 server-info.txt 或设置环境变量 SSHBOX_HOST' }
if (-not $pass_)  { throw '缺少 SSH_PASSWORD：请提供 server-info.txt 或设置环境变量 SSHBOX_PASSWORD' }

# ---- base64 编码注入值（安全承载任意特殊字符）----
$b64 = { param($v) [Convert]::ToBase64String([Text.Encoding]::UTF8.GetBytes([string]$v)) }
$ldflags = @(
    "-X 'main.builtinHost=$(& $b64 $host_)'",
    "-X 'main.builtinPort=$(& $b64 $port_)'",
    "-X 'main.builtinUser=$(& $b64 $user_)'",
    "-X 'main.builtinPassword=$(& $b64 $pass_)'"
)
if (-not $NoStrip) { $ldflags += '-s -w' }
$ldflagStr = $ldflags -join ' '

Write-Host ""
Write-Host ">>> 构建 sshbox.exe"
Write-Host "    服务器: $host_`:$port_  用户: $user_  认证: 密码（已 base64 注入）" -ForegroundColor Cyan

Push-Location $Root
try {
    & go build -trimpath -ldflags $ldflagStr -o $Out .
    if ($LASTEXITCODE -ne 0) { throw "go build 失败（退出码 $LASTEXITCODE）" }
} finally {
    Pop-Location
}

Write-Host ""
Write-Host ">>> 验证内置凭据（不显示密码）"
& (Join-Path $Root $Out) info
if ($LASTEXITCODE -ne 0) { throw '注入验证失败' }

Write-Host ""
Write-Host "[OK] 构建完成: $Out" -ForegroundColor Green
Write-Host "分发方式：把 $Out 发给使用方即可，对方无需任何配置。"
Write-Host "⚠️ 提醒：exe 内含可提取的明文凭据，请仅分发给可信人员；凭据轮换后需重新构建。"

# QueenBee Windows 一键安装脚本
# 用法 (PowerShell): irm https://raw.githubusercontent.com/heyangguang/queenbee/main/install.ps1 | iex

$ErrorActionPreference = "Stop"
$Repo = "heyangguang/queenbee"
$Binary = "queenbee.exe"
$InstallDir = "$env:LOCALAPPDATA\queenbee"

Write-Host ""
Write-Host "  🐝 QueenBee Windows 安装程序" -ForegroundColor Cyan
Write-Host "  ─────────────────────────────" -ForegroundColor DarkGray
Write-Host ""

# ── 获取最新版本 ──
Write-Host "[INFO]  获取最新版本..." -ForegroundColor Cyan
try {
    $release = Invoke-RestMethod "https://api.github.com/repos/$Repo/releases/latest"
    $version = $release.tag_name -replace '^v', ''
} catch {
    Write-Host "[ERROR] 无法获取版本: $_" -ForegroundColor Red
    exit 1
}
Write-Host "[OK]    最新版本: v$version" -ForegroundColor Green

# ── 检测架构 ──
$arch = if ([Environment]::Is64BitOperatingSystem) {
    if ($env:PROCESSOR_ARCHITECTURE -eq "ARM64") { "arm64" } else { "amd64" }
} else { "386" }
Write-Host "[INFO]  检测到架构: windows/$arch" -ForegroundColor Cyan

# ── 下载 ──
$filename = "queenbee_${version}_windows_${arch}.zip"
$url = "https://github.com/$Repo/releases/download/v$version/$filename"
$tmpDir = Join-Path $env:TEMP "queenbee-install"
$zipPath = Join-Path $tmpDir $filename

if (Test-Path $tmpDir) { Remove-Item $tmpDir -Recurse -Force }
New-Item -ItemType Directory -Path $tmpDir | Out-Null

Write-Host "[INFO]  下载 $url" -ForegroundColor Cyan
try {
    Invoke-WebRequest -Uri $url -OutFile $zipPath -UseBasicParsing
} catch {
    Write-Host "[ERROR] 下载失败: $_" -ForegroundColor Red
    exit 1
}

# ── 解压安装 ──
Write-Host "[INFO]  解压中..." -ForegroundColor Cyan
Expand-Archive -Path $zipPath -DestinationPath $tmpDir -Force

if (-not (Test-Path $InstallDir)) {
    New-Item -ItemType Directory -Path $InstallDir | Out-Null
}
Copy-Item (Join-Path $tmpDir $Binary) $InstallDir -Force

# ── 添加到 PATH ──
$userPath = [Environment]::GetEnvironmentVariable("Path", "User")
if ($userPath -notlike "*$InstallDir*") {
    [Environment]::SetEnvironmentVariable("Path", "$userPath;$InstallDir", "User")
    Write-Host "[OK]    已添加到用户 PATH: $InstallDir" -ForegroundColor Green
    Write-Host "[WARN]  请重新打开终端使 PATH 生效" -ForegroundColor Yellow
} else {
    Write-Host "[OK]    PATH 中已包含 $InstallDir" -ForegroundColor Green
}

# ── 清理 ──
Remove-Item $tmpDir -Recurse -Force

Write-Host ""
Write-Host "[OK]    QueenBee v$version 已安装到 $InstallDir\$Binary" -ForegroundColor Green
Write-Host ""
Write-Host "  🐝 快速开始:" -ForegroundColor Cyan
Write-Host "     queenbee start          # 启动服务"
Write-Host "     queenbee setup          # 初始化配置"
Write-Host "     queenbee --help         # 查看帮助"
Write-Host ""

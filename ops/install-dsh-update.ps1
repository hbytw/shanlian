# 安装 DSH 更新（绕过「优雅退出」限制）
#
# 背景：DSH 装更新时走 requireGraceful 路径，要求宿主进程在收到 shutdown 通知后
#       10 秒内干净退出。如果超过 10 秒，更新直接中止并报：
#         "Host did not complete graceful task teardown (... graceful deadline exceeded true)"
#
# 本脚本的做法：先强制结束所有 DSH 进程，让文件锁释放，再直接运行已下载的安装包。
# 安装包是 electron-builder 产的 NSIS 安装器，不需要 DSH 配合。

param(
    [switch]$Silent,   # 静默安装（不显示界面）
    [switch]$Check     # 只检查状态，不安装
)

$ErrorActionPreference = 'Continue'

$PendingDir = Join-Path $env:LOCALAPPDATA '@deepseek-aidsh-desktop-updater\pending'
$InfoFile   = Join-Path $PendingDir 'update-info.json'
$LogFile    = Join-Path $env:TEMP 'dsh-update-install.log'

function Log($m) {
    $line = '[{0}] {1}' -f (Get-Date -Format 'HH:mm:ss'), $m
    Add-Content -Path $LogFile -Value $line -ErrorAction SilentlyContinue
    Write-Host $m
}

function IsAdmin {
    return ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole(
        [Security.Principal.WindowsBuiltInRole]::Administrator)
}

# ---- 检查 ----
if (-not (Test-Path $InfoFile)) {
    Write-Host "没有待安装的更新（找不到 $InfoFile）"
    Write-Host "请先在 DSH 里检查更新并等它下载完成。"
    exit 1
}

$info = Get-Content $InfoFile -Raw | ConvertFrom-Json
$installer = Join-Path $PendingDir $info.fileName

if (-not (Test-Path $installer)) {
    Write-Host "更新包不存在: $installer"
    exit 1
}

$sizeMB = [math]::Round((Get-Item $installer).Length / 1MB)
Write-Host "待安装更新: $($info.fileName)  ($sizeMB MB)"
Write-Host "路径: $installer"
Write-Host ""

if ($Check) {
    $procs = Get-Process -Name 'DeepSeek Harness' -ErrorAction SilentlyContinue
    Write-Host "当前 DSH 进程数: $(@($procs).Count)"
    if ($procs) { $procs | ForEach-Object { Write-Host "  PID $($_.Id)" } }
    Write-Host "安装器需要管理员: $($info.isAdminRightsRequired)"
    Write-Host "本会话是管理员: $(IsAdmin)"
    exit 0
}

# ---- 需要管理员 ----
if (-not (IsAdmin)) {
    Log "需要管理员权限，正在提权..."
    $argsList = @('-NoProfile','-ExecutionPolicy','Bypass','-File', $PSCommandPath)
    if ($Silent) { $argsList += '-Silent' }
    Start-Process -FilePath 'powershell.exe' -ArgumentList $argsList -Verb RunAs -Wait
    exit 0
}

Log "=== 开始安装更新 ==="
Log "更新包: $($info.fileName)  ($sizeMB MB)"

# ---- 1. 结束所有 DSH 进程 ----
$procs = Get-Process -Name 'DeepSeek Harness' -ErrorAction SilentlyContinue
if ($procs) {
    Log "发现 $(@($procs).Count) 个 DSH 进程，正在结束..."
    foreach ($p in $procs) {
        try { $p.Kill(); Log "  已结束 PID $($p.Id)" } catch { Log "  结束 PID $($p.Id) 失败: $($_.Exception.Message)" }
    }
    # 等待进程真正消失（最多 15 秒）
    $deadline = (Get-Date).AddSeconds(15)
    while ((Get-Date) -lt $deadline) {
        $still = Get-Process -Name 'DeepSeek Harness' -ErrorAction SilentlyContinue
        if (-not $still) { break }
        Start-Sleep -Milliseconds 500
    }
    $still = Get-Process -Name 'DeepSeek Harness' -ErrorAction SilentlyContinue
    if ($still) {
        Log "仍有进程存活，强制结束..."
        $still | ForEach-Object { try { $_.Kill() } catch {} }
        Start-Sleep -Seconds 2
    }
}

$left = Get-Process -Name 'DeepSeek Harness' -ErrorAction SilentlyContinue
if ($left) {
    Log "警告：仍有 $(@($left).Count) 个 DSH 进程未退出，安装可能失败"
} else {
    Log "所有 DSH 进程已结束"
}

# ---- 2. 运行安装器 ----
$installArgs = if ($Silent) { '/S' } else { '' }
Log "运行安装器（静默=$Silent）..."

try {
    if ($Silent) {
        $p = Start-Process -FilePath $installer -ArgumentList '/S' -Wait -PassThru -ErrorAction Stop
        Log "安装器退出码: $($p.ExitCode)"
    } else {
        Log "请在弹出的安装界面里完成安装（通常点「下一步」即可）。"
        $p = Start-Process -FilePath $installer -Wait -PassThru -ErrorAction Stop
        Log "安装器退出码: $($p.ExitCode)"
    }
} catch {
    Log "安装器启动失败: $($_.Exception.Message)"
    Log "你可以手动双击这个文件安装："
    Log "  $installer"
    exit 1
}

# ---- 3. 验证 ----
Start-Sleep -Seconds 2
$ver = $null
$verFile = 'D:\dsh\version'
if (Test-Path $verFile) { $ver = (Get-Content $verFile -Raw).Trim() }

Log "=== 完成 ==="
Log "当前 D:\dsh\version = $ver"
Log "如果版本没变，说明安装被取消或失败，请手动双击安装包。"
Log "日志: $LogFile"

Write-Host ""
Write-Host "现在可以重新打开 DeepSeek Harness 了。"

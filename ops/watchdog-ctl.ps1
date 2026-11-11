# SLX 网关看门狗 —— 控制脚本
#
# 用法（需要管理员权限，会自动提权）：
#   .\watchdog-ctl.ps1 -Status      查看状态
#   .\watchdog-ctl.ps1 -Restore     立即恢复走旁路由
#   .\watchdog-ctl.ps1 -Disable     临时停用（保留安装）
#   .\watchdog-ctl.ps1 -Enable      重新启用
#   .\watchdog-ctl.ps1 -Uninstall   完全卸载

param(
    [switch]$Status,
    [switch]$Restore,
    [switch]$Disable,
    [switch]$Enable,
    [switch]$Uninstall
)

$ErrorActionPreference = 'Continue'
$TaskName = 'SLX-GatewayWatchdog'
$BaseDir  = 'C:\ProgramData\slx'
$Script   = Join-Path $BaseDir 'gw-watchdog.ps1'
$Flag     = Join-Path $BaseDir 'disable'

function Assert-Admin {
    $isAdmin = ([Security.Principal.WindowsPrincipal][Security.Principal.WindowsIdentity]::GetCurrent()).IsInRole([Security.Principal.WindowsBuiltInRole]::Administrator)
    if (-not $isAdmin) {
        Write-Host "需要管理员权限，正在提权..."
        $argsList = @('-NoProfile','-ExecutionPolicy','Bypass','-File', $PSCommandPath)
        if ($Status)    { $argsList += '-Status' }
        if ($Restore)   { $argsList += '-Restore' }
        if ($Disable)   { $argsList += '-Disable' }
        if ($Enable)    { $argsList += '-Enable' }
        if ($Uninstall) { $argsList += '-Uninstall' }
        Start-Process -FilePath 'powershell.exe' -ArgumentList $argsList -Verb RunAs -Wait
        exit 0
    }
}

if ($Status) {
    Write-Host "=== 看门狗状态 ==="
    $q = schtasks /query /tn $TaskName /fo LIST 2>&1
    if ($q -match '找不到|ERROR') {
        Write-Host "计划任务: 未安装"
    } else {
        Write-Host "计划任务: 已安装"
        $q | Select-String -Pattern '模式|状态|下次运行' | ForEach-Object { "  " + $_.Line.Trim() }
    }
    Write-Host "禁用标志: $(Test-Path $Flag)"
    if (Test-Path $Script) {
        Write-Host ""
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Script -Status
    }
    $log = Join-Path $BaseDir 'gateway-watchdog.log'
    if (Test-Path $log) {
        Write-Host ""
        Write-Host "=== 最近日志 ==="
        Get-Content $log -Tail 10 | ForEach-Object { "  $_" }
    }
    exit 0
}

Assert-Admin

if ($Restore) {
    Write-Host "立即恢复走旁路由..."
    if (Test-Path $Script) {
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Script -Restore
    }
    exit 0
}

if ($Disable) {
    New-Item -ItemType File -Force -Path $Flag | Out-Null
    Write-Host "已停用（标志文件: $Flag）。看门狗不再动作，仍保留安装。"
    Write-Host "重新启用: .\watchdog-ctl.ps1 -Enable"
    # 停用后如果需要，顺手切回旁路由
    if (Test-Path $Script) {
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Script -Restore
    }
    exit 0
}

if ($Enable) {
    Remove-Item $Flag -Force -ErrorAction SilentlyContinue
    schtasks /run /tn $TaskName 2>&1 | Out-Null
    Write-Host "已重新启用。"
    exit 0
}

if ($Uninstall) {
    Write-Host "卸载看门狗..."
    schtasks /delete /tn $TaskName /f 2>&1 | Out-Null
    Get-CimInstance Win32_Process -Filter "Name='powershell.exe'" -ErrorAction SilentlyContinue |
        Where-Object { $_.CommandLine -like '*gw-watchdog*' } |
        ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    # 先把路由恢复原状，再删文件
    if (Test-Path $Script) {
        & powershell.exe -NoProfile -ExecutionPolicy Bypass -File $Script -Restore
    }
    Remove-Item $Script -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $BaseDir 'gateway-state.txt') -Force -ErrorAction SilentlyContinue
    Remove-Item (Join-Path $BaseDir 'primary-metric.txt') -Force -ErrorAction SilentlyContinue
    Write-Host "已卸载。日志保留在 $BaseDir 供追溯。"
    exit 0
}

Write-Host "用法: .\watchdog-ctl.ps1 -Status | -Restore | -Disable | -Enable | -Uninstall"

# SLX 网关看门狗
#
# 目的：当旁路由（192.168.1.254 / OpenWrt）失效时，自动把默认网关切到主路由
#       （192.168.1.1），保证本机的 ZeroTier 仍然在线 —— 远程还能连回来。
#       旁路由恢复后自动切回。
#
# 安全设计：
#   1. 切换后立即验证互联网；失败就把刚做的改动原样撤销。
#   2. 存在 disable 标志文件时什么都不做。
#   3. 记录原始 metric，恢复时按原值还原，不是猜一个数。
#   4. 开机后先等一段时间再判断，避免网络没就绪就误切。
#   5. 所有动作写日志。
#
# 手工恢复：
#   powershell -ExecutionPolicy Bypass -File gateway-watchdog.ps1 -Restore
# 临时停用：在 C:\ProgramData\slx 下建一个名为 disable 的空文件
# 查看状态：powershell -ExecutionPolicy Bypass -File gateway-watchdog.ps1 -Status

param(
    [switch]$Once,
    [switch]$DryRun,
    [switch]$Restore,
    [switch]$Status
)

$ErrorActionPreference = 'Continue'

$PrimaryGW    = '192.168.1.254'
$BackupGW     = '192.168.1.1'
$ProbeIP      = '223.5.5.5'
$IntervalSec  = 10
$FailLimit    = 3
$RestoreEvery = 6
$StartupGrace = 60

$BaseDir     = 'C:\ProgramData\slx'
$LogFile     = Join-Path $BaseDir 'gateway-watchdog.log'
$StateFile   = Join-Path $BaseDir 'gateway-state.txt'
$MetricFile  = Join-Path $BaseDir 'primary-metric.txt'
$DisableFlag = Join-Path $BaseDir 'disable'

if (-not (Test-Path $BaseDir)) {
    New-Item -ItemType Directory -Force -Path $BaseDir | Out-Null
}

function Write-Log {
    param([string]$Msg)
    $line = '[{0}] {1}' -f (Get-Date -Format 'yyyy-MM-dd HH:mm:ss'), $Msg
    try {
        Add-Content -Path $LogFile -Value $line -ErrorAction SilentlyContinue
        $fi = Get-Item $LogFile -ErrorAction SilentlyContinue
        if ($fi -and $fi.Length -gt 2MB) {
            Move-Item $LogFile ($LogFile + '.1') -Force -ErrorAction SilentlyContinue
        }
    } catch {}
}

function Write-Out {
    param([string]$Msg)
    Write-Log $Msg
    Write-Host $Msg
}

function Test-Internet {
    try {
        return (Test-Connection -ComputerName $ProbeIP -Count 2 -Quiet -ErrorAction Stop)
    } catch {
        return $false
    }
}

function Get-RouteTo {
    param([string]$NextHop)
    return Get-NetRoute -DestinationPrefix '0.0.0.0/0' -ErrorAction SilentlyContinue |
           Where-Object { $_.NextHop -eq $NextHop }
}

function Get-Mode {
    if (Test-Path $StateFile) {
        $m = (Get-Content $StateFile -Raw -ErrorAction SilentlyContinue).Trim()
        if ($m -eq 'BACKUP' -or $m -eq 'PRIMARY') { return $m }
    }
    return 'PRIMARY'
}

function Set-Mode {
    param([string]$Mode)
    try { Set-Content -Path $StateFile -Value $Mode -Force -ErrorAction SilentlyContinue } catch {}
}

function Switch-ToBackup {
    $primary = Get-RouteTo $PrimaryGW
    if (-not $primary) {
        Write-Out "找不到指向 $PrimaryGW 的默认路由，放弃切换"
        return $false
    }
    $p0 = $primary | Select-Object -First 1
    $oldMetric = $p0.RouteMetric
    $idx = $p0.InterfaceIndex
    Set-Content -Path $MetricFile -Value $oldMetric -Force -ErrorAction SilentlyContinue

    Write-Out "切换到主路由：$PrimaryGW metric $oldMetric -> 200，新增 $BackupGW metric 1"
    try {
        Set-NetRoute -DestinationPrefix '0.0.0.0/0' -NextHop $PrimaryGW -RouteMetric 200 -ErrorAction Stop
        Remove-NetRoute -DestinationPrefix '0.0.0.0/0' -NextHop $BackupGW -Confirm:$false -ErrorAction SilentlyContinue
        New-NetRoute -DestinationPrefix '0.0.0.0/0' -NextHop $BackupGW -InterfaceIndex $idx -RouteMetric 1 -ErrorAction Stop | Out-Null
    } catch {
        Write-Out "切换失败: $($_.Exception.Message)"
        Restore-Primary | Out-Null
        return $false
    }

    Start-Sleep -Seconds 2
    if (-not (Test-Internet)) {
        Write-Out "切换后仍然上不了网 —— 立即回退"
        Restore-Primary | Out-Null
        return $false
    }
    Set-Mode 'BACKUP'
    Write-Out "已切到主路由，互联网恢复"
    return $true
}

function Restore-Primary {
    $savedMetric = $null
    if (Test-Path $MetricFile) {
        $t = (Get-Content $MetricFile -Raw -ErrorAction SilentlyContinue).Trim()
        if ($t -match '^\d+$') { $savedMetric = [int]$t }
    }
    if ($null -eq $savedMetric) { $savedMetric = 0 }

    Write-Out "恢复旁路由为默认出口：$PrimaryGW metric -> $savedMetric"
    try {
        Remove-NetRoute -DestinationPrefix '0.0.0.0/0' -NextHop $BackupGW -Confirm:$false -ErrorAction SilentlyContinue
        Set-NetRoute -DestinationPrefix '0.0.0.0/0' -NextHop $PrimaryGW -RouteMetric $savedMetric -ErrorAction Stop
    } catch {
        Write-Out "恢复失败: $($_.Exception.Message)"
        return $false
    }
    Start-Sleep -Seconds 2
    if (Test-Internet) {
        Set-Mode 'PRIMARY'
        Write-Out "已恢复走旁路由，互联网正常"
        return $true
    }

    Write-Out "恢复后互联网不通 —— 退回主路由兜底（宁可没分流也不能没网）"
    $primary = Get-RouteTo $PrimaryGW
    if ($primary) {
        $idx = ($primary | Select-Object -First 1).InterfaceIndex
        try {
            New-NetRoute -DestinationPrefix '0.0.0.0/0' -NextHop $BackupGW -InterfaceIndex $idx -RouteMetric 1 -ErrorAction SilentlyContinue | Out-Null
            Set-Mode 'BACKUP'
        } catch {}
    }
    return $false
}

if ($Status) {
    Write-Host "看门狗模式: $(Get-Mode)"
    Write-Host "互联网:     $(if (Test-Internet) { '通' } else { '不通' })"
    Write-Host "禁用标志:   $(Test-Path $DisableFlag)"
    Write-Host ""
    Write-Host "旁路由($PrimaryGW) 默认路由:"
    $r1 = Get-RouteTo $PrimaryGW
    if ($r1) { $r1 | Format-Table DestinationPrefix, NextHop, RouteMetric, InterfaceIndex -AutoSize | Out-String | Write-Host }
    else { Write-Host "  (无)" }
    Write-Host "主路由($BackupGW) 默认路由:"
    $r2 = Get-RouteTo $BackupGW
    if ($r2) { $r2 | Format-Table DestinationPrefix, NextHop, RouteMetric, InterfaceIndex -AutoSize | Out-String | Write-Host }
    else { Write-Host "  (无)" }
    exit 0
}

if ($Restore) {
    Write-Out "收到手工恢复请求"
    Restore-Primary | Out-Null
    exit 0
}

if (Test-Path $DisableFlag) {
    Write-Host "检测到 disable 标志，看门狗不工作。"
    exit 0
}

if ($StartupGrace -gt 0 -and -not $Once) {
    Write-Out "启动宽限期 ${StartupGrace}s（等网络就绪）"
    Start-Sleep -Seconds $StartupGrace
}

Write-Out "看门狗运行中（周期 ${IntervalSec}s，阈值 $FailLimit 次，兜底 $BackupGW）"

$failCount = 0
$cycle = 0

while ($true) {
    $cycle++
    $mode = Get-Mode

    if ($mode -eq 'PRIMARY') {
        if (Test-Internet) {
            $failCount = 0
        } else {
            $failCount++
            if ($failCount -lt $FailLimit) {
                Write-Log "互联网不通（$failCount/$FailLimit）"
            } else {
                Write-Out "互联网连续 $FailLimit 次不通，准备切换"
                if ($DryRun) {
                    Write-Out "[dry-run] 本应切换到主路由 $BackupGW"
                } else {
                    Switch-ToBackup | Out-Null
                }
                $failCount = 0
            }
        }
    } else {
        if (-not (Test-Internet)) {
            Write-Log "兜底模式下互联网也不通，请检查主路由 $BackupGW"
        }
        if ($cycle % $RestoreEvery -eq 0) {
            Write-Out "尝试恢复走旁路由"
            if ($DryRun) {
                Write-Out "[dry-run] 本应尝试恢复"
            } else {
                Restore-Primary | Out-Null
            }
        }
    }

    if ($Once) { exit 0 }
    Start-Sleep -Seconds $IntervalSec
}

# Build static binaries for all target architectures (Windows host).
# Equivalent to scripts/build-all.sh
#
# Usage: .\scripts\build-all.ps1 [-Version 1.0.0]
#
# NOTE: This file is intentionally ASCII-only. Windows PowerShell 5.1 reads
# .ps1 files without a BOM as ANSI, which corrupts non-ASCII text and can
# break parsing. Keep it ASCII.
param(
    [string]$Version = "0.0.0-dev"
)

$ErrorActionPreference = 'Stop'
$root = Split-Path -Parent (Split-Path -Parent $MyInvocation.MyCommand.Path)
Push-Location $root

try {
    $commit = (git rev-parse --short HEAD 2>$null)
    if (-not $commit) { $commit = 'unknown' }
    $buildTime = (Get-Date).ToUniversalTime().ToString('yyyy-MM-ddTHH:mm:ssZ')

    $ldflags = "-s -w -X slx/internal/buildinfo.Version=$Version -X slx/internal/buildinfo.Commit=$commit -X slx/internal/buildinfo.BuildTime=$buildTime"

    $out = Join-Path $root 'dist'
    if (Test-Path $out) { Remove-Item $out -Recurse -Force }
    New-Item -ItemType Directory -Path $out -Force | Out-Null

    $env:CGO_ENABLED = '0'
    $env:GOOS = 'linux'

    $targets = @(
        @{ Arch = 'amd64';   Extra = @{} },
        @{ Arch = 'arm64';   Extra = @{} },
        @{ Arch = 'arm';     Extra = @{ GOARM = '7' } },
        @{ Arch = 'mipsle';  Extra = @{ GOMIPS = 'softfloat' } },
        @{ Arch = 'riscv64'; Extra = @{} }
    )

    $results = @()
    foreach ($t in $targets) {
        $env:GOARCH = $t.Arch
        foreach ($k in @('GOARM', 'GOMIPS')) {
            if ($t.Extra.ContainsKey($k)) { Set-Item -Path "Env:$k" -Value $t.Extra[$k] }
            elseif (Test-Path "Env:$k") { Remove-Item "Env:$k" }
        }
        $name = "slx-linux-$($t.Arch)"
        $path = Join-Path $out $name
        $label = '  linux/{0,-9}' -f $t.Arch
        Write-Host -NoNewline $label
        $r = go build -trimpath -ldflags $ldflags -o $path ./cmd/slx 2>&1
        if ($LASTEXITCODE -eq 0) {
            $kb = [math]::Round((Get-Item $path).Length / 1KB, 0)
            Write-Host "OK    $kb KB"
            $results += $path
        } else {
            Write-Host "FAIL"
            Write-Host ($r -join "`n")
            throw "build failed for linux/$($t.Arch)"
        }
    }

    $lines = @()
    foreach ($f in $results) {
        $h = (Get-FileHash -Path $f -Algorithm SHA256).Hash.ToLower()
        $leaf = Split-Path $f -Leaf
        $lines += "$h  $leaf"
    }
    $lines | Set-Content -Path (Join-Path $out 'SHA256SUMS') -Encoding ASCII

    Write-Host ""
    Write-Host "Artifacts in dist/:"
    Get-ChildItem $out -File | Format-Table Name, @{N='KB';E={[math]::Round($_.Length/1KB,0)}} -AutoSize
    Write-Host "SHA256SUMS:"
    Get-Content (Join-Path $out 'SHA256SUMS')
}
finally {
    Pop-Location
    Remove-Item Env:CGO_ENABLED, Env:GOOS, Env:GOARCH, Env:GOARM, Env:GOMIPS -ErrorAction SilentlyContinue
}

$ErrorActionPreference = "Continue"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$logs = Join-Path $root "logs"
$simPidFile = Join-Path $logs "simulator.pid"
$urlFile = Join-Path $env:USERPROFILE "Desktop\SIMULYATOR-URL.txt"

Write-Host "=== Simulyator OCHIRILMOQDA ==="

if (Test-Path $simPidFile) {
    $procId = Get-Content $simPidFile -ErrorAction SilentlyContinue
    if ($procId -and (Get-Process -Id $procId -ErrorAction SilentlyContinue)) {
        Stop-Process -Id $procId -Force -ErrorAction SilentlyContinue
    }
    Remove-Item $simPidFile -Force -ErrorAction SilentlyContinue
}

Get-Process toir-telemetry-simulator -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.Id -Force }
Get-Process cloudflared -ErrorAction SilentlyContinue | ForEach-Object { Stop-Process -Id $_.Id -Force }

try {
    $conn = Get-NetTCPConnection -LocalPort 8091 -State Listen -ErrorAction SilentlyContinue | Select-Object -First 1
    if ($conn) { Stop-Process -Id $conn.OwningProcess -Force -ErrorAction SilentlyContinue }
} catch {}

Set-Content -Path $urlFile -Value @("OCHIRILGAN", "", "Qayta yoqish: Desktop\Simulyator-YOQISH.bat") -Encoding ASCII
Write-Host "OCHIRILDI."
pause
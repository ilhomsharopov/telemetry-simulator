param(
    [switch]$Quiet,
    [switch]$Rebuild
)

$ErrorActionPreference = "Continue"
$root = Split-Path -Parent $MyInvocation.MyCommand.Path
$pgBin = "C:\Program Files\PostgreSQL\18\bin"
$pgData = "C:\Users\User\Desktop\toir-telemetry-pgdata"
$go = "C:\Program Files\Go\bin\go.exe"
$exe = Join-Path $root "toir-telemetry-simulator.exe"
$logs = Join-Path $root "logs"
$simPidFile = Join-Path $logs "simulator.pid"
$urlFile = Join-Path $env:USERPROFILE "Desktop\SIMULYATOR-URL.txt"
$startLogFile = Join-Path $logs "last-start.txt"

New-Item -ItemType Directory -Force -Path $logs | Out-Null
Set-Location $root
$env:GOTOOLCHAIN = "local"

function Write-Log([string]$msg) {
    $line = "$(Get-Date -Format 'HH:mm:ss') $msg"
    Write-Host $line
    Add-Content -Path $startLogFile -Value $line -Encoding UTF8
}

function Test-SimulatorHealth {
    try {
        $r = Invoke-WebRequest -Uri "http://127.0.0.1:8091/healthz" -UseBasicParsing -TimeoutSec 2
        return $r.StatusCode -eq 200
    } catch {
        return $false
    }
}

function Get-LanIp {
    $candidates = @(
        Get-NetIPAddress -AddressFamily IPv4 -ErrorAction SilentlyContinue |
            Where-Object { $_.IPAddress -like "192.168.*" } |
            Select-Object -ExpandProperty IPAddress
    )
    foreach ($ip in $candidates) {
        try {
            $r = Invoke-WebRequest -Uri ("http://{0}:8091/healthz" -f $ip) -UseBasicParsing -TimeoutSec 1
            if ($r.StatusCode -eq 200) { return $ip }
        } catch {}
    }
    if ($candidates.Count -gt 0) {
        foreach ($ip in $candidates) {
            if ($ip -like "192.168.0.*") { return $ip }
        }
        return $candidates[0]
    }
    return "192.168.0.196"
}

Set-Content -Path $startLogFile -Value ("START " + (Get-Date)) -Encoding UTF8
Write-Log "=== Simulyator YOQILMOQDA (LAN) ==="

Get-Process cloudflared -ErrorAction SilentlyContinue | ForEach-Object {
    Stop-Process -Id $_.Id -Force -ErrorAction SilentlyContinue
}

Write-Log "[1/2] Baza..."
if (-not (Test-Path "$pgBin\pg_isready.exe")) {
    Write-Log "PostgreSQL topilmadi: $pgBin"
    if (-not $Quiet) { pause }
    exit 1
}

& "$pgBin\pg_isready.exe" -h 127.0.0.1 -p 5434 -U postgres 2>$null | Out-Null
if ($LASTEXITCODE -ne 0) {
    $pidFile = Join-Path $pgData "postmaster.pid"
    if (Test-Path $pidFile) {
        $oldPid = (Get-Content $pidFile -TotalCount 1).Trim()
        if (-not (Get-Process -Id $oldPid -ErrorAction SilentlyContinue)) {
            Remove-Item $pidFile -Force -ErrorAction SilentlyContinue
            Write-Log "Eski postmaster.pid olib tashlandi"
        }
    }
    $pgLog = Join-Path $env:USERPROFILE "Desktop\toir-telemetry-pg-start.log"
    Write-Log "Postgres start..."
    $pgStart = & "$pgBin\pg_ctl.exe" -D $pgData -l $pgLog -o "-p 5434" start 2>&1 | Out-String
    if ($pgStart) { Write-Log $pgStart.Trim() }
    $dbOk = $false
    for ($i = 0; $i -lt 90; $i++) {
        Start-Sleep -Seconds 1
        & "$pgBin\pg_isready.exe" -h 127.0.0.1 -p 5434 -U postgres 2>$null | Out-Null
        if ($LASTEXITCODE -eq 0) { $dbOk = $true; break }
    }
    if (-not $dbOk) {
        Write-Log "Baza ochilmadi. Log: $pgLog"
        if (Test-Path $pgLog) { Get-Content $pgLog -Tail 15 | ForEach-Object { Write-Log $_ } }
        if (-not $Quiet) { pause }
        exit 1
    }
}
Write-Log "Baza OK"

Write-Log "[2/2] Simulyator..."
$needBuild = $Rebuild -or -not (Test-Path $exe)
if (-not $needBuild -and (Test-Path $go)) {
    $exeTime = (Get-Item $exe).LastWriteTime
    $newerSource = Get-ChildItem -Path (Join-Path $root "cmd"), (Join-Path $root "internal") -Recurse -Filter *.go -ErrorAction SilentlyContinue |
        Where-Object { $_.LastWriteTime -gt $exeTime } |
        Select-Object -First 1
    if ($newerSource) {
        $needBuild = $true
        Write-Log ("Kod yangilangan: " + $newerSource.Name)
    }
}
if ($needBuild) {
    if (-not (Test-Path $go)) {
        Write-Log "go.exe topilmadi va exe yangilanmadi"
        if (-not $Quiet) { pause }
        exit 1
    }
    Write-Log "Eski jarayon to'xtatiladi, keyin go build..."
    if (Test-Path $simPidFile) {
        $oldSim = Get-Content $simPidFile -ErrorAction SilentlyContinue
        if ($oldSim) { Stop-Process -Id $oldSim -Force -ErrorAction SilentlyContinue }
    }
    Get-Process toir-telemetry-simulator -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1
    & $go build -buildvcs=false -o $exe .\cmd\server
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path $exe)) {
        Write-Log "go build xato"
        if (-not $Quiet) { pause }
        exit 1
    }
}

if (-not (Test-SimulatorHealth)) {
    if (Test-Path $simPidFile) {
        $oldSim = Get-Content $simPidFile -ErrorAction SilentlyContinue
        if ($oldSim) { Stop-Process -Id $oldSim -Force -ErrorAction SilentlyContinue }
    }
    Get-Process toir-telemetry-simulator -ErrorAction SilentlyContinue | Stop-Process -Force -ErrorAction SilentlyContinue
    Start-Sleep -Seconds 1

    $simOut = Join-Path $logs "simulator-out.txt"
    $simErr = Join-Path $logs "simulator-err.txt"
    Remove-Item $simOut, $simErr -ErrorAction SilentlyContinue

    # WindowStyle Hidden: bo'sh qora terminal oynasi chiqmasin
    $sim = Start-Process -FilePath $exe -WorkingDirectory $root -PassThru -WindowStyle Hidden `
        -RedirectStandardOutput $simOut -RedirectStandardError $simErr
    Set-Content -Path $simPidFile -Value $sim.Id -Encoding ASCII
    Write-Log ("Simulyator PID " + $sim.Id + " (yashirin oyna)")

    $ok = $false
    for ($i = 0; $i -lt 25; $i++) {
        Start-Sleep -Seconds 1
        if (Test-SimulatorHealth) { $ok = $true; break }
        if ($sim.HasExited) {
            Write-Log ("Simulyator yopildi. Exit=" + $sim.ExitCode)
            if (Test-Path $simErr) { Get-Content $simErr | ForEach-Object { Write-Log $_ } }
            if (Test-Path $simOut) { Get-Content $simOut -Tail 10 | ForEach-Object { Write-Log $_ } }
            if (-not $Quiet) { pause }
            exit 1
        }
    }
    if (-not $ok) {
        Write-Log "Simulyator 8091 da ochilmadi"
        if (Test-Path $simErr) { Get-Content $simErr | ForEach-Object { Write-Log $_ } }
        if (-not $Quiet) { pause }
        exit 1
    }
} else {
    Write-Log "Simulyator allaqachon ishlayapti"
}

try {
    $rule = Get-NetFirewallRule -DisplayName "TOIR Telemetry Simulator 8091" -ErrorAction SilentlyContinue
    if (-not $rule) {
        New-NetFirewallRule -DisplayName "TOIR Telemetry Simulator 8091" -Direction Inbound -Protocol TCP -LocalPort 8091 -Action Allow -Profile Private,Domain -ErrorAction SilentlyContinue | Out-Null
    }
} catch {}

$lanIp = Get-LanIp
Set-Content -Path $urlFile -Value @(
    "REJIM: LAN",
    "Lokal: http://127.0.0.1:8091",
    "WiFi:  http://$lanIp:8091",
    "healthz: http://$lanIp:8091/healthz",
    "assets:  http://$lanIp:8091/api/v1/assets",
    "",
    "OCHIRISH: Desktop\Simulyator-OCHIRISH.bat"
) -Encoding ASCII

Write-Log "TAYYOR"
Write-Log ("Lokal: http://127.0.0.1:8091/healthz")
Write-Log ("WiFi:  http://{0}:8091/healthz" -f $lanIp)
if (-not $Quiet) {
    Start-Process notepad.exe $urlFile
    Write-Host ""
    Write-Host "YOQISH oynasini yopsangiz ham simulyator ishlayveradi."
    Write-Host "Enter bosib yoping..."
    pause
}
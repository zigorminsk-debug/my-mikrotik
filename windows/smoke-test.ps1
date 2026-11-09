param([Parameter(Mandatory=$true)][string]$Installer)
$ErrorActionPreference = 'Stop'
$base = Join-Path $env:RUNNER_TEMP ("mti-smoke-" + [guid]::NewGuid().ToString('N'))
$oldLocalAppData = $env:LOCALAPPDATA
$env:LOCALAPPDATA = Join-Path $base 'local'
$installDir = Join-Path $base 'installed'
$logPath = Join-Path $env:LOCALAPPDATA 'MikroTik Inspector\inspector.log'
$app = $null
New-Item -ItemType Directory -Force $env:LOCALAPPDATA | Out-Null
try {
    $setup = Start-Process -FilePath $Installer -ArgumentList @('/VERYSILENT', '/SUPPRESSMSGBOXES', '/NORESTART', "/DIR=$installDir") -PassThru -Wait
    if ($setup.ExitCode -ne 0) { throw "Установщик вернул код $($setup.ExitCode)" }
    $exe = Join-Path $installDir 'MikroTikInspector.exe'
    if (-not (Test-Path $exe)) { throw 'EXE не найден после установки' }
    $app = Start-Process -FilePath $exe -PassThru
    $deadline = (Get-Date).AddSeconds(60)
    $url = $null
    $window = $null
    while ((Get-Date) -lt $deadline) {
        $app.Refresh()
        if ($app.HasExited) { throw "Приложение завершилось при запуске: $($app.ExitCode)" }
        if ((Test-Path $logPath) -and -not $url) {
            $log = Get-Content -Raw -Encoding UTF8 $logPath
            if ($log -match 'URL: (http://127\.0\.0\.1:\d+)') { $url = $Matches[1] }
        }
        $browser = Get-CimInstance Win32_Process -Filter "Name='msedge.exe'" |
            Where-Object { $_.CommandLine -and $_.CommandLine.Contains($env:LOCALAPPDATA) }
        foreach ($candidate in $browser) {
            $process = Get-Process -Id $candidate.ProcessId -ErrorAction SilentlyContinue
            if ($process -and $process.MainWindowHandle -ne 0 -and $process.MainWindowTitle -like '*MikroTik Inspector*') { $window = $process; break }
        }
        if ($url -and $window) {
            try {
                $response = Invoke-WebRequest -UseBasicParsing -TimeoutSec 3 -Uri ($url + '/')
                if ($response.StatusCode -eq 200 -and $response.Content.Contains('<title>MikroTik Inspector</title>')) { break }
            } catch { }
        }
        Start-Sleep -Milliseconds 500
    }
    if (-not $url -or -not $window -or (Get-Date) -ge $deadline) { throw 'Окно приложения или локальная страница не открылись за 60 секунд' }
    Write-Host "Окно и HTML открылись: $url"
    if (-not $window.CloseMainWindow()) { throw 'Не удалось закрыть окно приложения' }
    if (-not $app.WaitForExit(20000)) { throw 'Приложение не завершилось после закрытия окна' }
    try {
        Invoke-WebRequest -UseBasicParsing -TimeoutSec 2 -Uri ($url + '/') | Out-Null
        throw 'HTTP-сервер продолжает работать после закрытия окна'
    } catch {
        if ($_.Exception.Message -eq 'HTTP-сервер продолжает работать после закрытия окна') { throw }
    }
    Write-Host 'Приложение и локальный сервер остановились после закрытия окна'
} catch {
    if (Test-Path $logPath) { Write-Host '--- Журнал приложения ---'; Get-Content -Encoding UTF8 $logPath }
    throw
} finally {
    if ($app -and -not $app.HasExited) { & taskkill.exe /PID $app.Id /T /F 2>$null | Out-Null }
    Get-CimInstance Win32_Process -Filter "Name='msedge.exe'" | Where-Object { $_.CommandLine -and $_.CommandLine.Contains($env:LOCALAPPDATA) } | ForEach-Object { Stop-Process -Id $_.ProcessId -Force -ErrorAction SilentlyContinue }
    $env:LOCALAPPDATA = $oldLocalAppData
    Remove-Item -Recurse -Force $base -ErrorAction SilentlyContinue
}

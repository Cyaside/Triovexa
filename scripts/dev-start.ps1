param(
    [switch]$SkipDatabase
)

$ErrorActionPreference = "Stop"

$repoRoot = Resolve-Path (Join-Path $PSScriptRoot "..")

if (-not $SkipDatabase) {
    & (Join-Path $PSScriptRoot "dev-up.ps1")
}

$databaseUrl = $env:DATABASE_URL
if (-not $databaseUrl) {
    $databaseUrl = "postgres://postgres:postgres@localhost:5433/triovexa?sslmode=disable"
}

$demoScriptPath = Join-Path $repoRoot "scripts\dev-run-demo.ps1"
$serverScriptPath = Join-Path $repoRoot "scripts\dev-run-server.ps1"

$demoCommand = "& '$demoScriptPath'"
$serverCommand = "`$env:DATABASE_URL = '$databaseUrl'; & '$serverScriptPath'"

Start-Process powershell -ArgumentList @("-NoExit", "-Command", $demoCommand) -WorkingDirectory $repoRoot
Start-Process powershell -ArgumentList @("-NoExit", "-Command", $serverCommand) -WorkingDirectory $repoRoot

Write-Host "Started local development processes in new PowerShell windows."
Write-Host "Demo service: http://localhost:8090"
Write-Host "App server:   http://localhost:8080"
Write-Host "Incident UI:  http://localhost:8080/ui/incidents"

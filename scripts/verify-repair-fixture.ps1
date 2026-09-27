[CmdletBinding()]
param(
    [string]$ProjectName = 'triovexa-repair-fixture',
    [string]$EvidenceRoot = 'artifacts/code-repair',
    [switch]$SkipBuild,
    [switch]$StopAfter
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot
$env:REPAIR_FIXTURE_REVISION = (git rev-parse HEAD).Trim()
$composeArgs = @('compose', '-f', 'docker-compose.yml', '-f', 'compose.repair-fixture.yaml', '-p', $ProjectName)

function Invoke-Compose {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = & docker @composeArgs @Arguments 2>&1
        $exit = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previous }
    if ($exit -ne 0) { throw "docker compose $($Arguments -join ' ') failed: $($output | Out-String)" }
    return $output
}

function Get-WorkloadMetrics {
    $raw = Invoke-Compose exec -T workload-supervisor wget -qO- 'http://127.0.0.1:8091/metrics'
    $values = @{}
    foreach ($line in ($raw | Out-String) -split "`n") {
        if ($line -match '^(triovexa_[a-z_]+)(?:\{[^}]+\})?\s+([0-9]+)') {
            $values[$matches[1]] = [int64]$matches[2]
        }
    }
    foreach ($required in @('triovexa_queue_backlog', 'triovexa_jobs_processed_total', 'triovexa_worker_healthy', 'triovexa_worker_generation')) {
        if (-not $values.ContainsKey($required)) { throw "Missing workload metric: $required" }
    }
    return $values
}

function Wait-For {
    param([scriptblock]$Probe, [int]$TimeoutSec, [string]$Description)
    $until = (Get-Date).AddSeconds($TimeoutSec)
    while ((Get-Date) -lt $until) {
        try {
            $value = & $Probe
            if ($null -ne $value -and $value -ne $false) { return $value }
        } catch { $script:lastProbeError = $_.Exception.Message }
        Start-Sleep -Seconds 2
    }
    throw "Timed out waiting for $Description. Last error: $script:lastProbeError"
}

function Get-FiringBacklogAlert {
    $raw = Invoke-Compose exec -T prometheus wget -qO- 'http://127.0.0.1:9090/api/v1/alerts'
    $response = ($raw | Out-String) | ConvertFrom-Json
    return @($response.data.alerts | Where-Object {
        $_.labels.alertname -eq 'TriovexaQueueBacklogHigh' -and $_.state -eq 'firing'
    } | Select-Object -First 1)
}

try {
    & docker info --format '{{.ServerVersion}}' *> $null
    if ($LASTEXITCODE -ne 0) { throw 'Docker Desktop is unavailable; this script did not touch any running project.' }

    $serviceArgs = @('redis', 'workload-supervisor', 'producer', 'prometheus', 'alertmanager')
    if (-not $SkipBuild) { Invoke-Compose build workload-supervisor producer | Out-Null }
    Invoke-Compose up -d --no-build @serviceArgs | Out-Null

    $before = Wait-For -TimeoutSec 90 -Description 'fixture backlog above 20 with an unhealthy worker' -Probe {
        $metrics = Get-WorkloadMetrics
        if ($metrics.triovexa_queue_backlog -gt 20 -and $metrics.triovexa_worker_healthy -eq 0) { $metrics }
    }
    $alert = Wait-For -TimeoutSec 90 -Description 'a real Prometheus queue backlog alert' -Probe {
        $matches = @(Get-FiringBacklogAlert)
        if ($matches.Count -gt 0) { $matches[0] }
    }

    $operationID = 'repair-fixture-' + [guid]::NewGuid().ToString('N')
    $request = @{ operation_id = $operationID; operation = 'restart_worker'; target = 'queue-worker'; requested_by = 'fixture-verification' } | ConvertTo-Json -Compress
    $restartRaw = Invoke-Compose exec -T workload-supervisor wget -qO- '--header=Authorization: Bearer local-control-token' '--header=Content-Type: application/json' "--post-data=$request" 'http://127.0.0.1:8091/operations'
    $restart = ($restartRaw | Out-String) | ConvertFrom-Json
    if ($restart.status -ne 'succeeded') { throw "Supervisor did not accept restart: $($restartRaw | Out-String)" }
    Start-Sleep -Seconds 12
    $after = Get-WorkloadMetrics
    if ($after.triovexa_worker_generation -le $before.triovexa_worker_generation) { throw 'Worker generation did not increase after restart.' }
    if ($after.triovexa_worker_healthy -ne 0) { throw 'Fixture unexpectedly recovered after restart.' }
    if ($after.triovexa_jobs_processed_total -ne $before.triovexa_jobs_processed_total) { throw 'Fixture unexpectedly processed jobs after restart.' }
    if ($after.triovexa_queue_backlog -le $before.triovexa_queue_backlog) { throw 'Backlog did not continue to grow after restart.' }

    $directory = Join-Path $repoRoot $EvidenceRoot
    New-Item -ItemType Directory -Force -Path $directory | Out-Null
    $evidence = [ordered]@{
        schema_version = 1
        scenario = 'source-bug-survives-worker-restart'
        recorded_at = (Get-Date).ToUniversalTime().ToString('o')
        project = $ProjectName
        source_revision = $env:REPAIR_FIXTURE_REVISION
        fixture_schema = 2
        prometheus_alert = $alert.labels.alertname
        before_restart = $before
        restart_operation_id = $operationID
        after_restart = $after
        result = 'passed'
    }
    $path = Join-Path $directory ('fixture-' + (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ') + '.json')
    $evidence | ConvertTo-Json -Depth 12 | Set-Content -LiteralPath $path -Encoding utf8
    Write-Host "Fixture verified: alert fired, restart happened, worker stayed unhealthy, backlog grew. Evidence: $path" -ForegroundColor Green
} finally {
    if ($StopAfter) {
        try { Invoke-Compose down | Out-Null } catch { Write-Warning $_.Exception.Message }
    }
}

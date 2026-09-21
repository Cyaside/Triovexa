[CmdletBinding()]
param(
    [string]$ProjectName = 'triovexa-e2e',
    [string]$BaseUrl = 'http://127.0.0.1:8080',
    [int]$GrafanaPort = 3300,
    [string]$EvidenceRoot = 'artifacts/e2e',
    [switch]$Reset,
    [switch]$SkipBuild,
    [switch]$StopAfter,
    [switch]$RequireProvider
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot
$env:GRAFANA_HOST_PORT = $GrafanaPort.ToString()

function Invoke-Compose {
    param([Parameter(ValueFromRemainingArguments = $true)][string[]]$Arguments)
    $preference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        & docker compose -p $ProjectName @Arguments
        $exitCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $preference }
    if ($exitCode -ne 0) { throw "docker compose failed: $($Arguments -join ' ')" }
}

function Invoke-Triovexa {
    param([string]$Path, [string]$Method = 'GET', [object]$Body, [int]$TimeoutSec = 30)
    $parameters = @{ Uri = $BaseUrl.TrimEnd('/') + $Path; Method = $Method; TimeoutSec = $TimeoutSec }
    if ($null -ne $Body) {
        $parameters.ContentType = 'application/json'
        $parameters.Body = $Body | ConvertTo-Json -Depth 20 -Compress
    }
    Invoke-RestMethod @parameters
}

function Wait-ForValue {
    param([scriptblock]$Probe, [int]$TimeoutSec, [string]$Description, [int]$IntervalSec = 2)
    $deadline = (Get-Date).AddSeconds($TimeoutSec)
    $lastError = $null
    while ((Get-Date) -lt $deadline) {
        try {
            $value = & $Probe
            if ($null -ne $value -and $value -ne $false) { return $value }
        } catch { $lastError = $_.Exception.Message }
        Start-Sleep -Seconds $IntervalSec
    }
    if ($lastError) { throw "Timed out waiting for $Description. Last error: $lastError" }
    throw "Timed out waiting for $Description."
}

function Get-PrometheusAlerts {
    $preference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $raw = & docker compose -p $ProjectName exec -T prometheus wget -qO- 'http://127.0.0.1:9090/api/v1/alerts' 2>$null
        $exitCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $preference }
    if ($exitCode -ne 0) { throw 'Prometheus alert query failed.' }
    ($raw | Out-String) | ConvertFrom-Json
}

function Get-AlertmanagerAlerts {
    $preference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $raw = & docker compose -p $ProjectName exec -T alertmanager wget -qO- 'http://127.0.0.1:9093/api/v2/alerts' 2>$null
        $exitCode = $LASTEXITCODE
    } finally { $ErrorActionPreference = $preference }
    if ($exitCode -ne 0) { throw 'Alertmanager alert query failed.' }
    @(($raw | Out-String) | ConvertFrom-Json)
}

$runId = (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ')
$runDirectory = Join-Path $repoRoot (Join-Path $EvidenceRoot $runId)
New-Item -ItemType Directory -Force -Path $runDirectory | Out-Null
$steps = [System.Collections.Generic.List[object]]::new()
$startedAt = (Get-Date).ToUniversalTime()
$result = [ordered]@{
    schema_version = 1
    run_id = $runId
    scenario = 'worker-stall-alert-approval-restart-recovery'
    started_at = $startedAt.ToString('o')
    finished_at = $null
    status = 'running'
    error = $null
    project = $ProjectName
    git_commit = (git rev-parse HEAD).Trim()
    steps = $steps
}

function Add-EvidenceStep {
    param([string]$Id, [string]$Title, [object]$Evidence)
    $steps.Add([ordered]@{
        id = $Id
        title = $Title
        status = 'passed'
        observed_at = (Get-Date).ToUniversalTime().ToString('o')
        evidence = $Evidence
    })
    Write-Host "[$Id] $Title" -ForegroundColor Green
}

try {
    cmd /c "docker info 1>nul 2>nul"
    if ($LASTEXITCODE -ne 0) { throw 'Docker Desktop is not reachable.' }

    if ($Reset) {
        $preference = $ErrorActionPreference
        $ErrorActionPreference = 'Continue'
        try { & docker compose -p $ProjectName down --volumes --remove-orphans *> $null } finally { $ErrorActionPreference = $preference }
    }

    foreach ($binding in @(
        @{ Port = 8080; Service = 'triovexa' },
        @{ Port = $GrafanaPort; Service = 'grafana' }
    )) {
        $listeners = Get-NetTCPConnection -State Listen -LocalPort $binding.Port -ErrorAction SilentlyContinue
        if ($listeners) {
            $preference = $ErrorActionPreference
            $ErrorActionPreference = 'Continue'
            try { $existing = & docker compose -p $ProjectName ps --services --status running 2>$null } finally { $ErrorActionPreference = $preference }
            if ($existing -notcontains $binding.Service) {
                throw "Port $($binding.Port) is already occupied by a process outside this Compose project. It was left untouched."
            }
        }
    }

    if (-not $SkipBuild) {
        Invoke-Compose build triovexa
        Invoke-Compose build workload-supervisor
        Invoke-Compose build producer
    }
    Invoke-Compose up --detach --no-build
    $health = Wait-ForValue -TimeoutSec 180 -Description 'Triovexa readiness' -Probe {
        $candidate = Invoke-Triovexa -Path '/health'
        if ($candidate.status -eq 'ok') { $candidate }
    }
    Add-EvidenceStep 'E2E-01' 'Compose stack passed dependency-aware readiness' $health

    $reasoningConfig = Invoke-Triovexa -Path '/api/v1/connections/reasoning/config'
    $runtimeSettings = Invoke-Triovexa -Path '/api/v1/settings'
    $providerEvidence = [ordered]@{
        mode = $runtimeSettings.runtime.reasoning
        provider = $reasoningConfig.provider
        model = $reasoningConfig.model
        credential_available = $reasoningConfig.credential_available
        credential_source = $reasoningConfig.credential_source
    }
    if ($RequireProvider) {
        if ($providerEvidence.mode -ne 'llm' -or -not $providerEvidence.credential_available -or [string]::IsNullOrWhiteSpace([string]$providerEvidence.model)) {
            throw 'A connected OpenAI-compatible provider in llm mode is required for this demo.'
        }
        $providerTest = Wait-ForValue -TimeoutSec 330 -IntervalSec 3 -Description 'a valid JSON response from the reasoning provider' -Probe {
            $candidate = Invoke-Triovexa -Path '/api/v1/connections/reasoning/test' -Method POST -Body @{} -TimeoutSec 310
            if ($candidate.status -eq 'connected') { $candidate }
        }
        $providerEvidence['test'] = $providerTest
    }
    $result['reasoning'] = $providerEvidence

    Invoke-Triovexa -Path '/api/v1/playground/faults' -Method POST -Body @{ mode = 'healthy' } | Out-Null
    $baseline = Wait-ForValue -TimeoutSec 45 -Description 'a healthy queue baseline' -Probe {
        $candidate = Invoke-Triovexa -Path '/api/v1/playground'
        if ($candidate.enabled -and $candidate.state.worker_healthy -and [int]$candidate.state.queue_backlog -le 5) { $candidate.state }
    }
    $clearedPipeline = Wait-ForValue -TimeoutSec 180 -Description 'the previous backlog alert to clear from Prometheus and Alertmanager' -Probe {
        $prometheus = Get-PrometheusAlerts
        $prometheusMatch = @($prometheus.data.alerts | Where-Object { $_.labels.alertname -eq 'TriovexaQueueBacklogHigh' -and $_.state -eq 'firing' })
        $alertmanager = Get-AlertmanagerAlerts
        $alertmanagerMatch = @($alertmanager | Where-Object { $_.labels.alertname -eq 'TriovexaQueueBacklogHigh' -and $_.status.state -eq 'active' })
        if ($prometheusMatch.Count -eq 0 -and $alertmanagerMatch.Count -eq 0) {
            [ordered]@{ prometheus_firing = 0; alertmanager_active = 0 }
        }
    }
    Add-EvidenceStep 'E2E-02' 'Real Redis Streams worker established a healthy baseline' $baseline
    $result['alert_pipeline_baseline'] = $clearedPipeline

    $generationBefore = [int64]$baseline.generation
    $faultStartedAt = (Get-Date).ToUniversalTime()
    Invoke-Triovexa -Path '/api/v1/playground/faults' -Method POST -Body @{ mode = 'stall' } | Out-Null
    $stalled = Wait-ForValue -TimeoutSec 60 -Description 'queue backlog above the alert threshold' -Probe {
        $candidate = (Invoke-Triovexa -Path '/api/v1/playground').state
        if ([int]$candidate.queue_backlog -gt 20) { $candidate }
    }
    Add-EvidenceStep 'E2E-03' 'Bounded worker-stall fault created a real queue backlog' $stalled

    $prometheusAlert = Wait-ForValue -TimeoutSec 75 -Description 'Prometheus backlog alert firing' -Probe {
        $alerts = Get-PrometheusAlerts
        $match = @($alerts.data.alerts | Where-Object { $_.labels.alertname -eq 'TriovexaQueueBacklogHigh' -and $_.state -eq 'firing' })
        if ($match.Count -gt 0) { $match[0] }
    }
    $incident = Wait-ForValue -TimeoutSec 45 -Description 'Alertmanager delivery to Triovexa' -Probe {
        $page = Invoke-Triovexa -Path '/api/v1/incidents?service=queue-worker&page_size=100'
        $candidate = @($page.items | Where-Object {
            ([datetime]$_.CreatedAt).ToUniversalTime() -ge $faultStartedAt -and
            $_.State -notin @('resolved', 'failed_remediation', 'rolled_back', 'escalated', 'closed')
        } | Sort-Object CreatedAt -Descending | Select-Object -First 1)
        if ($candidate.Count -gt 0) { $candidate[0] }
    }
    Add-EvidenceStep 'E2E-04' 'Prometheus fired and Alertmanager delivered the incident' ([ordered]@{
        alert = $prometheusAlert
        incident = $incident
    })

    $reasoningWaitSeconds = if ($RequireProvider) { 660 } else { 45 }
    $detail = Wait-ForValue -TimeoutSec $reasoningWaitSeconds -Description 'restart recommendation awaiting approval' -Probe {
        $candidate = Invoke-Triovexa -Path "/api/v1/incidents/$($incident.ID)"
        $restart = @($candidate.candidate_actions | Where-Object { $_.ActionType -eq 'restart_worker' -and $_.TargetResource -eq 'queue-worker' -and $_.Status -eq 'awaiting_approval' })
        if ($restart.Count -gt 0) { $candidate }
    }
    $action = @($detail.candidate_actions | Where-Object { $_.ActionType -eq 'restart_worker' -and $_.TargetResource -eq 'queue-worker' })[0]
    $reasoningTitle = 'Configured reasoning path proposed an allowlisted worker restart'
    if ($RequireProvider) {
        $notes = [string]$detail.triage.ConfidenceNotes
        if ($notes -match 'llm_fallback=true' -or $notes -notmatch [regex]::Escape("model=$($providerEvidence.model)")) {
            throw "Triage was not generated by $($providerEvidence.model)."
        }
        if ([string]$action.ApprovalHint -notmatch [regex]::Escape("model=$($providerEvidence.model)")) {
            throw "Remediation was not generated by $($providerEvidence.model)."
        }
        $reasoningTitle = "$($providerEvidence.model) proposed an allowlisted worker restart"
    }
    Add-EvidenceStep 'E2E-05' $reasoningTitle ([ordered]@{
        triage = $detail.triage
        action = $action
        evidence = $detail.evidence
    })

    Invoke-Triovexa -Path "/api/v1/actions/$($action.ID)/approve" -Method POST -Body @{} | Out-Null
    Invoke-Triovexa -Path "/api/v1/actions/$($action.ID)/execute" -Method POST -Body @{} -TimeoutSec 180 | Out-Null
    $afterExecution = Invoke-Triovexa -Path "/api/v1/incidents/$($incident.ID)"
    $finalAction = @($afterExecution.candidate_actions | Where-Object { $_.ID -eq $action.ID })[0]
    $execution = @($afterExecution.execution_records | Where-Object { $_.CandidateActionID -eq $action.ID } | Select-Object -Last 1)[0]
    $stateAfter = (Invoke-Triovexa -Path '/api/v1/playground').state
    if ([int64]$stateAfter.generation -le $generationBefore) { throw 'Supervisor generation did not increase after restart.' }
    if ($finalAction.Status -ne 'succeeded' -or $execution.Status -ne 'succeeded') { throw 'The bounded restart execution did not succeed.' }
    Add-EvidenceStep 'E2E-06' 'Approved execution restarted the real supervisor worker' ([ordered]@{
        generation_before = $generationBefore
        generation_after = [int64]$stateAfter.generation
        action = $finalAction
        execution = $execution
    })

    $finalDetail = Wait-ForValue -TimeoutSec 30 -Description 'resolved incident with successful verification' -Probe {
        $candidate = Invoke-Triovexa -Path "/api/v1/incidents/$($incident.ID)"
        $verification = @($candidate.verification_results | Select-Object -Last 1)
        if ($candidate.incident.State -eq 'resolved' -and $verification.Count -gt 0 -and $verification[0].Status -eq 'success') { $candidate }
    }
    $verificationResult = @($finalDetail.verification_results | Select-Object -Last 1)[0]
    $verificationEvidence = $verificationResult.EvidenceJSON | ConvertFrom-Json
    if (@($verificationEvidence.observations).Count -lt 3) { throw 'Verification did not persist three recovery observations.' }
    $finalState = (Invoke-Triovexa -Path '/api/v1/playground').state
    if (-not $finalState.worker_healthy -or [int]$finalState.queue_backlog -ge [int]$stalled.queue_backlog) { throw 'Workload telemetry did not show recovery.' }
    Add-EvidenceStep 'E2E-07' 'Three consecutive observations verified backlog recovery' ([ordered]@{
        incident = $finalDetail.incident
        verification = $verificationResult
        verification_evidence = $verificationEvidence
        final_workload_state = $finalState
    })

    $result.status = 'passed'
} catch {
    $result.status = 'failed'
    $result.error = $_.Exception.Message
    $steps.Add([ordered]@{
        id = 'ERROR'
        title = 'Scenario failed'
        status = 'failed'
        observed_at = (Get-Date).ToUniversalTime().ToString('o')
        evidence = @{ message = $_.Exception.Message }
    })
    throw
} finally {
    $result.finished_at = (Get-Date).ToUniversalTime().ToString('o')
    $evidencePath = Join-Path $runDirectory 'evidence.json'
    $result | ConvertTo-Json -Depth 40 | Set-Content -Encoding utf8 $evidencePath
    $preference = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        try { Invoke-Triovexa -Path '/api/v1/playground/faults' -Method POST -Body @{ mode = 'healthy' } -TimeoutSec 15 | Out-Null } catch {}
        & docker compose -p $ProjectName ps --format json 2>&1 | Set-Content -Encoding utf8 (Join-Path $runDirectory 'compose-ps.jsonl')
        & docker compose -p $ProjectName logs --no-color --tail 500 2>&1 | Set-Content -Encoding utf8 (Join-Path $runDirectory 'compose.log')
    } finally { $ErrorActionPreference = $preference }
    Copy-Item -Force $evidencePath (Join-Path (Split-Path $runDirectory -Parent) 'latest.json')
    Write-Host "Evidence: $evidencePath" -ForegroundColor Cyan
    if ($StopAfter) {
        $ErrorActionPreference = 'Continue'
        try { & docker compose -p $ProjectName down --remove-orphans | Out-Null } finally { $ErrorActionPreference = $preference }
    }
}

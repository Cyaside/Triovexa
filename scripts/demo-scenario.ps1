param(
    [Parameter(Mandatory = $true)]
    [ValidateSet("timeout-after-deploy", "worker-stall", "error-rate-spike")]
    [string]$Scenario,

    [string]$ServerBaseUrl = "http://localhost:8080",
    [string]$DemoBaseUrl = "http://localhost:8090",
    [string]$ServiceName = "checkout-service",
    [string]$Environment = "staging",
    [string]$Severity = "critical"
)

$scenarioMap = @{
    "timeout-after-deploy" = @{
        SimulatePath = "/simulate/timeout-after-deploy"
        Title = "checkout timeout after deploy"
        Summary = "checkout timeout alert"
        Fingerprint = "demo-timeout-after-deploy"
    }
    "worker-stall" = @{
        SimulatePath = "/simulate/worker-stall"
        Title = "worker stall on checkout consumer"
        Summary = "checkout worker stalled"
        Fingerprint = "demo-worker-stall"
    }
    "error-rate-spike" = @{
        SimulatePath = "/simulate/error-rate-spike"
        Title = "checkout error rate spike"
        Summary = "checkout error rate spike"
        Fingerprint = "demo-error-rate-spike"
    }
}

$selected = $scenarioMap[$Scenario]
if (-not $selected) {
    throw "Unknown scenario: $Scenario"
}

$demoUri = ($DemoBaseUrl.TrimEnd("/")) + $selected.SimulatePath
$serverUri = ($ServerBaseUrl.TrimEnd("/")) + "/webhooks/grafana"

Write-Host "Triggering demo service scenario '$Scenario'..." -ForegroundColor Cyan
$demoResponse = Invoke-RestMethod -Method Post -Uri $demoUri

$timestamp = (Get-Date).ToUniversalTime().ToString("o")
$payload = @{
    title = $selected.Title
    commonLabels = @{
        service = $ServiceName
        environment = $Environment
        severity = $Severity
    }
    alerts = @(
        @{
            status = "firing"
            fingerprint = $selected.Fingerprint
            startsAt = $timestamp
            labels = @{
                service = $ServiceName
                environment = $Environment
                severity = $Severity
            }
            annotations = @{
                summary = $selected.Summary
            }
        }
    )
}

Write-Host "Sending Grafana-compatible webhook to Triovexa..." -ForegroundColor Cyan
$incidentResponse = Invoke-RestMethod -Method Post -Uri $serverUri -ContentType "application/json" -Body ($payload | ConvertTo-Json -Depth 6)

Write-Host ""
Write-Host "Scenario triggered successfully." -ForegroundColor Green
Write-Host "Incident ID : $($incidentResponse.incident_id)"
Write-Host "State       : $($incidentResponse.state)"
Write-Host "UI          : $($ServerBaseUrl.TrimEnd('/'))/ui/incidents/$($incidentResponse.incident_id)"
Write-Host ""
Write-Host "Demo snapshot summary:" -ForegroundColor Yellow
$demoResponse | ConvertTo-Json -Depth 6
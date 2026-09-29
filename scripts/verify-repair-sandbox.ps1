[CmdletBinding()]
param(
    [string]$Image = 'triovexa-repair-sandbox:gate2',
    [string]$EvidenceRoot = 'artifacts/code-repair',
    [switch]$SkipBuild
)

$ErrorActionPreference = 'Stop'
$repoRoot = Split-Path -Parent $PSScriptRoot
Set-Location $repoRoot

function Invoke-Sandbox {
    param([string]$Request)
    $previous = $ErrorActionPreference
    $ErrorActionPreference = 'Continue'
    try {
        $output = $Request | & docker run --rm -i --network none --read-only --cap-drop ALL `
            --security-opt no-new-privileges --pids-limit 128 --memory 2g --cpus 2 `
            --tmpfs /tmp:rw,exec,nosuid,nodev,size=1g `
            -v "${repoRoot}:/workspace:ro" $Image 2>&1
        $exit = $LASTEXITCODE
    } finally { $ErrorActionPreference = $previous }
    return @{ ExitCode = $exit; Output = ($output | Out-String).Trim() }
}

& docker info --format '{{.ServerVersion}}' *> $null
if ($LASTEXITCODE -ne 0) { throw 'Docker Desktop is unavailable.' }
if (-not $SkipBuild) {
    & docker build -q -f Dockerfile.repair-sandbox -t $Image . | Out-Null
    if ($LASTEXITCODE -ne 0) { throw 'Repair sandbox image build failed.' }
}

$binding = [ordered]@{
    ID = 'binding-1'
    ServiceName = 'queue-worker'
    Environment = 'staging'
    RepositoryURL = 'https://github.com/Cyaside/Triovexa'
    BaseRef = 'main'
    AllowedPaths = @('internal/workload')
    TestRecipes = @('go-test-workload')
    PolicyVersion = 'repair-v1'
    Enabled = $true
}
$request = [ordered]@{ operation = 'run_allowed_test'; recipe_id = 'go-test-workload'; binding = $binding }
$sourceFile = Join-Path $repoRoot 'internal/workload/repair_fixture.go'
if (-not (Test-Path -LiteralPath $sourceFile)) { throw 'Repair fixture source is missing.' }
$beforeHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceFile).Hash

$baseline = Invoke-Sandbox -Request ($request | ConvertTo-Json -Compress -Depth 5)
if ($baseline.ExitCode -ne 0) { throw "Sandbox invocation failed: $($baseline.Output)" }
$result = $baseline.Output | ConvertFrom-Json
if ($result.recipe_id -ne 'go-test-workload' -or $result.exit_code -ne 1 -or $result.timed_out -or $result.truncated -or
    $result.output -notmatch 'TestRepairFixtureAcceptsSchemaTwo' -or
    $result.output -notmatch 'unsupported job schema version 2') {
    throw "Sandbox did not reproduce the expected source bug: $($baseline.Output)"
}
$afterHash = (Get-FileHash -Algorithm SHA256 -LiteralPath $sourceFile).Hash
if ($afterHash -ne $beforeHash) { throw 'The read-only sandbox changed the fixture source.' }

$rejected = @()
foreach ($attack in @(
    @{ Name = 'shell operation'; Operation = 'run_shell'; RecipeID = 'go-test-workload' },
    @{ Name = 'arbitrary recipe'; Operation = 'run_allowed_test'; RecipeID = '../../bin/sh' },
    @{ Name = 'injected recipe'; Operation = 'run_allowed_test'; RecipeID = 'go-test-workload;curl example.invalid' }
)) {
    $badRequest = [ordered]@{ operation = $attack.Operation; recipe_id = $attack.RecipeID; binding = $binding }
    $response = Invoke-Sandbox -Request ($badRequest | ConvertTo-Json -Compress -Depth 5)
    if ($response.ExitCode -ne 2) { throw "$($attack.Name) was not rejected: $($response.Output)" }
    $rejected += $attack.Name
}
$request['command'] = 'sh -c id'
$unknownField = Invoke-Sandbox -Request ($request | ConvertTo-Json -Compress -Depth 5)
if ($unknownField.ExitCode -ne 2) { throw "Injected command field was not rejected: $($unknownField.Output)" }
$rejected += 'command field'
$request.Remove('command')

$evidenceDir = Join-Path $repoRoot $EvidenceRoot
New-Item -ItemType Directory -Force -Path $evidenceDir | Out-Null
$evidence = [ordered]@{
    schema_version = 1
    checked_at_utc = (Get-Date).ToUniversalTime().ToString('o')
    git_revision = (& git rev-parse HEAD).Trim()
    image = $Image
    recipe = 'go-test-workload'
    fixture_test_exit_code = $result.exit_code
    fixture_failure = 'unsupported job schema version 2'
    sandbox = @('network:none', 'rootfs:read-only', 'workspace:read-only', 'capabilities:none', 'no-new-privileges', 'uid:10001', 'bounded-pids-memory-cpu')
    source_unchanged = $true
    rejected_inputs = $rejected
}
$evidenceFile = Join-Path $evidenceDir ('sandbox-' + (Get-Date).ToUniversalTime().ToString('yyyyMMddTHHmmssZ') + '.json')
$evidence | ConvertTo-Json -Depth 5 | Set-Content -LiteralPath $evidenceFile -Encoding utf8
Write-Output "Repair sandbox gate passed. Evidence: $evidenceFile"

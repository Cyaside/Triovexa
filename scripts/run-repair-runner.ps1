[CmdletBinding()]
param(
    [string]$DatabaseURL = 'postgres://postgres:postgres@127.0.0.1:5433/triovexa?sslmode=disable',
    [string]$SandboxImage = 'triovexa-repair-sandbox:local'
)

$ErrorActionPreference = 'Stop'
$repoRoot = (Resolve-Path -LiteralPath (Join-Path $PSScriptRoot '..')).Path
$tempRoot = [IO.Path]::GetFullPath([IO.Path]::GetTempPath())
$credentialDirectory = Join-Path $tempRoot ('triovexa-repair-credentials-' + [guid]::NewGuid().ToString('N'))
$credentialDirectory = [IO.Path]::GetFullPath($credentialDirectory)
if (-not $credentialDirectory.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase)) {
    throw 'Temporary credential directory resolved outside the expected root.'
}

Push-Location $repoRoot
try {
    & docker info --format '{{.ServerVersion}}' *> $null
    if ($LASTEXITCODE -ne 0) { throw 'Docker Desktop is unavailable.' }
    & docker image inspect $SandboxImage *> $null
    if ($LASTEXITCODE -ne 0) {
        & docker build -q -f Dockerfile.repair-sandbox -t $SandboxImage .
        if ($LASTEXITCODE -ne 0) { throw 'Repair sandbox build failed.' }
    }
    $container = (& docker compose ps -q triovexa).Trim()
    if ($LASTEXITCODE -ne 0 -or [string]::IsNullOrWhiteSpace($container)) {
        throw 'Start the Triovexa Compose server before running the repair worker.'
    }
    New-Item -ItemType Directory -Path $credentialDirectory -Force | Out-Null
    $keyPath = Join-Path $credentialDirectory 'credential.key'
    & docker cp "${container}:/app/runtime/credential.key" $keyPath
    if ($LASTEXITCODE -ne 0 -or -not (Test-Path -LiteralPath $keyPath)) {
        throw 'The server credential key is unavailable. Configure the reasoning connection in the UI first.'
    }
    $env:DATABASE_URL = $DatabaseURL
    $env:CREDENTIAL_KEY_PATH = $keyPath
    $env:REPAIR_SANDBOX_IMAGE = $SandboxImage
    $env:APP_ENV = 'local'
    & go run ./cmd/repair-runner
    if ($LASTEXITCODE -ne 0) { throw 'Repair runner stopped with an error.' }
} finally {
    Pop-Location
    if ((Test-Path -LiteralPath $credentialDirectory) -and
        $credentialDirectory.StartsWith($tempRoot, [StringComparison]::OrdinalIgnoreCase) -and
        (Split-Path -Leaf $credentialDirectory) -like 'triovexa-repair-credentials-*') {
        Remove-Item -LiteralPath $credentialDirectory -Recurse -Force
    }
}

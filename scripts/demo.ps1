[CmdletBinding()]
param([switch]$Reset, [switch]$SkipBuild, [switch]$StopAfter, [int]$GrafanaPort = 3300)

if ($Reset) { throw 'Reset removes the persisted provider credential. Run the development E2E script directly when a clean-volume test is required.' }
& (Join-Path $PSScriptRoot 'run-compose-e2e.ps1') -SkipBuild:$SkipBuild -StopAfter:$StopAfter -GrafanaPort $GrafanaPort -RequireProvider
exit $LASTEXITCODE

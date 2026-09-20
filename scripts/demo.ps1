[CmdletBinding()]
param([switch]$Reset, [switch]$SkipBuild, [switch]$StopAfter, [int]$GrafanaPort = 3300)

& (Join-Path $PSScriptRoot 'run-compose-e2e.ps1') -Reset:$Reset -SkipBuild:$SkipBuild -StopAfter:$StopAfter -GrafanaPort $GrafanaPort
exit $LASTEXITCODE

param(
    [Parameter(Mandatory = $true)][string]$DatabaseUrl,
    [Parameter(Mandatory = $true)][string]$BackupFile
)

$ErrorActionPreference = 'Stop'
$parent = Split-Path -Parent $BackupFile
if ($parent) { New-Item -ItemType Directory -Path $parent -Force | Out-Null }
& pg_dump --format=custom --no-owner --no-privileges --file $BackupFile $DatabaseUrl
if ($LASTEXITCODE -ne 0) { throw 'pg_dump failed' }
& pg_restore --list $BackupFile | Out-Null
if ($LASTEXITCODE -ne 0) { throw 'backup verification failed' }
Write-Output "backup verified: $BackupFile"

param(
    [Parameter(Mandatory = $true)][string]$DatabaseUrl,
    [Parameter(Mandatory = $true)][string]$BackupFile
)

$ErrorActionPreference = 'Stop'
& pg_restore --clean --if-exists --no-owner --no-privileges --dbname $DatabaseUrl $BackupFile
if ($LASTEXITCODE -ne 0) { throw 'pg_restore failed' }
& psql $DatabaseUrl -v ON_ERROR_STOP=1 -c 'SELECT version FROM schema_migrations ORDER BY version;'
if ($LASTEXITCODE -ne 0) { throw 'restored schema verification failed' }
Write-Output "restore verified: $DatabaseUrl"

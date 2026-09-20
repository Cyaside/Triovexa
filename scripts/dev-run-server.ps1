$ErrorActionPreference = "Stop"

Set-Location (Resolve-Path (Join-Path $PSScriptRoot ".."))

if (-not $env:DATABASE_URL) {
    $env:DATABASE_URL = "postgres://postgres:postgres@localhost:5433/triovexa?sslmode=disable"
}

go run ./cmd/server

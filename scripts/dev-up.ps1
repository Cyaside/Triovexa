$ErrorActionPreference = "Stop"

cmd /c "docker info 1>nul 2>nul"
if ($LASTEXITCODE -ne 0) {
    throw "Docker daemon is not reachable. Start Docker Desktop first, then rerun this script."
}

$composeArgs = @("compose", "up", "-d", "postgres")
docker @composeArgs

Write-Host "Waiting for PostgreSQL to become ready..."

$maxAttempts = 30
for ($attempt = 1; $attempt -le $maxAttempts; $attempt++) {
    docker compose exec -T postgres pg_isready -U postgres -d triovexa | Out-Null
    if ($LASTEXITCODE -eq 0) {
        Write-Host "PostgreSQL is ready."
        Write-Host ""
        Write-Host "Next steps:"
        Write-Host "1. Ensure DATABASE_URL is set to:"
        Write-Host "   postgres://postgres:postgres@localhost:5433/triovexa?sslmode=disable"
        Write-Host "2. Run the demo service:"
        Write-Host "   go run ./cmd/demo-service"
        Write-Host "3. Run the app server:"
        Write-Host "   go run ./cmd/server"
        exit 0
    }

    Start-Sleep -Seconds 2
}

throw "PostgreSQL did not become ready after $maxAttempts attempts."

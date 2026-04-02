# PostgreSQL Setup

This document describes the PostgreSQL baseline used by the project for persistence.

## Why PostgreSQL

- closer to a production-like architecture
- incident, audit, and triage data are more realistically exercised in a server-based database
- reduces the need for a large persistence migration later

## Minimum Configuration

The server uses `DATABASE_URL` as the main configuration input. Example:

```env
DATABASE_URL=postgres://postgres:postgres@localhost:5432/triovexa?sslmode=disable
```

## Local Setup

The easiest local option is the Docker Compose automation already included in the repository.

## Running Local Automation

1. Make sure Docker Desktop is running.
2. Run:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-up.ps1
```

Or start the database, demo service, and application server in one step:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-start.ps1
```

3. To inspect status:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-status.ps1
```

4. To shut down the local environment:

```powershell
powershell -ExecutionPolicy Bypass -File .\scripts\dev-down.ps1
```

This automation starts local PostgreSQL with the following defaults:

- database: `triovexa`
- user: `postgres`
- password: `postgres`
- port: `5432`
- requirement: the Docker daemon must be running and reachable from the terminal
- `dev-start.ps1` opens two additional PowerShell windows for the local services

## Development Strategy

- the application applies baseline schema migrations at startup
- HTTP and orchestration tests do not need a real PostgreSQL instance all the time
- the main runtime assumes PostgreSQL by default
- local bootstrap is automated through `docker-compose.yml` and PowerShell scripts

## Notes

- if no PostgreSQL instance is available, the main server cannot start until a valid `DATABASE_URL` exists
- if the `postgres:17-alpine` image is not present, Docker will pull it during the first setup
- if the Docker daemon is not running, `dev-up.ps1` and `dev-start.ps1` stop early with a clear error

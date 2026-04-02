# Runbook: Refresh Demo Cache

## Goal

Restore a stale or mildly corrupted non-critical cache without affecting the critical path.

## When To Use It

- responses still serve stale data
- a cache miss spike or light cache corruption is detected
- the refresh is safe for the demo service

## Manual Steps

1. Verify that the issue originates from the cache layer.
2. Identify a specific cache key if available.
3. Run a cache refresh against the safe target.
4. Watch latency and error rate after the refresh.

## Success Signals

- fresh data is served correctly
- latency does not worsen
- errors do not increase

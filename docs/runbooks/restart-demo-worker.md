# Runbook: Restart Demo Worker

## Goal

Restore a non-critical demo worker that stopped processing jobs or is experiencing transient failures.

## When To Use It

- queue backlog increases
- the worker stops taking new jobs
- worker errors rise without signs of data corruption

## Manual Steps

1. Confirm that the incident only affects a non-critical worker.
2. Check whether a recent deployment touched the worker.
3. Verify that the worker logs show transient errors or repeated crashes.
4. Restart the target worker.
5. Watch backlog, error rate, and health signals.

## Success Signals

- backlog starts decreasing
- the worker resumes job processing
- error logs calm down

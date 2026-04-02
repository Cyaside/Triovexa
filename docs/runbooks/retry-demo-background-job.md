# Runbook: Retry Demo Background Job

## Goal

Retry a demo background job that failed because of a transient error.

## When To Use It

- the job failed due to a temporary timeout
- an external dependency was briefly unavailable
- retrying is safe and will not cause harmful duplication

## Manual Steps

1. Identify the failed `job_id`.
2. Confirm that the job belongs to a safe-to-retry category.
3. Check whether the dependency that failed has recovered.
4. Retry the job.
5. Verify that the job completes without introducing new errors.

## Success Signals

- the job completes successfully
- the job queue returns to normal
- no follow-up error spike appears

# Postmortem: Error Rate Spike After Release

## Summary

The demo service release increased error rate across both workers and the API at the same time. The incident worsened quickly because operators needed time to gather logs, metrics, and deployment context manually.

## Suspected Causes

- the release change was not compatible with older job payloads
- workers entered a crash loop after receiving a specific payload
- cache state became inconsistent after the release

## Typical Evidence

- an error-rate spike across multiple components
- repeated worker restarts
- a growing job backlog

## Lessons

- restart-worker and retry-job runbooks are valuable as an initial response
- evidence, inference, and action should stay clearly separated
- approval-gated remediation helps reduce panic actions

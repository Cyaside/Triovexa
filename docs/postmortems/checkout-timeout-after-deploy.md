# Postmortem: Checkout Timeout After Deploy

## Summary

After deploying a new version of `checkout-service`, timeout errors spiked during the first seven minutes. The alert was triggered by a jump in error rate and increased latency on the main checkout endpoint.

## Suspected Causes

- the payment dependency became slower to respond
- internal timeout settings were too aggressive
- the deployment change increased uncontrolled retry volume

## Typical Evidence

- an error-rate spike immediately after deployment
- timeout logs in the integration layer
- elevated P95 and P99 latency

## Lessons

- deployment context should always be included during triage
- rollback or feature-flag rollback should be available in production
- early remediation actions should remain constrained and approval-gated

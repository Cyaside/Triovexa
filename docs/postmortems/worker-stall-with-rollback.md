# Worker Stall With Rollback

## Summary

A high-backlog incident on the checkout consumer once became worse because the consumer stayed paused for too long. A fast rollback that resumed the consumer proved to be a safe guardrail.

## Key Lessons

- medium-risk actions still require operator approval
- rollback plans must be explicit before execution
- verification must be fast and evidence-based

## Why This Matters In Triovexa

This document reinforces why `pause_demo_queue_consumer` must never run without a rollback plan and why automatic rollback should be prioritized when the first outcome is poor.

# Resume Demo Queue Consumer

## When To Use It

Use this as the rollback for `pause_demo_queue_consumer` after verification shows that the pause made things worse or provided no benefit.

## Steps

1. Resume the consumer.
2. Check worker health.
3. Compare backlog before and after the resume.
4. Confirm that the incident moves to `rolled_back` when rollback succeeds.

## Success Signals

- the consumer is no longer paused
- error rate, latency, or backlog returns closer to baseline
- operators can continue manual investigation from a more stable system

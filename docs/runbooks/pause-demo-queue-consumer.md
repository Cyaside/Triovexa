# Pause Demo Queue Consumer

## When To Use It

Use only when queue backlog keeps rising and downstream workers cannot keep up with the incoming message rate.

## Safety Notes

- this is a medium-risk action
- operator approval is required
- it is only allowed on the `demo-queue-consumer` target
- the paired rollback is `resume_demo_queue_consumer`

## Steps

1. Confirm that backlog is still climbing.
2. Validate the dependency map and the downstream systems being protected.
3. Approve the action only if the rollback plan is ready.
4. Run verification immediately after execution.

## Success Signals

- blast radius stops expanding
- backlog grows more slowly than before
- operators gain time to stabilize the system

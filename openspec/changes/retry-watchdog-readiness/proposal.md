## Why

LuCI retains an inline mode-validation error after the backend reports that the independent watchdog is ready again. Separately, a confirmed read-only readiness refusal before lease arm can still become a permanent ApplyBlocked latch. The current router is healthy; this repair handles temporary startup/recovery availability and stale form validation without bypassing readiness.

## What Changes

- Type only confirmed watchdog availability failures: missing readiness file, expired valid readiness, and a vanished watchdog process.
- Convert those failures to deferred application only at known read-only pre-arm checks. Keep all unknown, corrupt, identity, recovery-state and persistence failures blocked.
- Retire only the exact legacy production ENOENT readiness latch; do not broadly match watchdog/stale messages.
- Revalidate LuCI's existing mode widget when status refresh updates its availability, preserving unsaved values and UCI.
- Release controller and LuCI packages as 0.1.3-r1 and validate the upgrade on the same target.

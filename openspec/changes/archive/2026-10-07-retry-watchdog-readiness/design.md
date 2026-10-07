# Safe watchdog availability and current form validation

The user explicitly requested that the watchdog.ready ENOENT case be handled as part of the permanent repair. The agreed behavior is to wait without writing rules, then resume only after real readiness is confirmed. There is no bypass, synthetic readiness file, extended freshness age or watchdog restart loop.

## Backend boundary

Recovery.Ready retains its real-file/age/PID/command checks and emits a distinct availability error only for missing record, expired otherwise valid record, and os.ErrNotExist reading the recorded process. Invalid PID, negative/future uptime, corrupt JSON, wrong process identity, permission/I/O/unknown errors and uptime-read failure remain hard errors. Check identity before describing an old record as a recoverable expiry; old combined stale wording is not sufficient for migration.

Adapter converts this typed read-only refusal to DeferredApplyError at its initial readiness check, locked readiness check and Ready-specific failure from Recovery.Arm. Arm's heartbeat-stop, lease validity/policy/restore-request, heartbeat write and lease write failures are never reclassified. The existing next-tick retry and diagnostic rules then apply unchanged, with no immediate retries, extra measurements or writes while readiness is absent.

Retire only the exact historical production message `watchdog readiness: open /var/run/mwan3-autobalancer/watchdog.ready: no such file or directory`, known to originate before arm in prior releases. Preserve every other latch and all state/settings/quota. A healthy new check and all ordinary ownership/configuration/lease gates are still required before a write.

## Frontend

After refresh assigns the latest report and updates automatic-option disabled/aria-disabled state, invoke the existing mode widget's public triggerValidation method. LuCI's cached validity/error/tooltip must follow false→true→false and updated reasons. Keep the same widget, unsaved selected value and all other inputs; do not reset/re-render the form or write UCI. Existing English/Russian templates suffice, with no catalog wording change.

## Acceptance

- Missing/expired/vanished watchdog before any lease write gives zero mutating rule/lease writes and no persistent latch. A later ordinary tick with fresh, genuine readiness resumes. Repeated absence stays fail-closed without immediate looping/probes.
- Invalid/corrupt/wrong-PID/future-time/read-permission errors, restoration requests, stopped owner heartbeats, invalid leases and persistence/possibly committed failures remain hard-blocked. In particular, do not classify all Arm failures or all stale strings as retryable.
- Exact old ENOENT latch migration preserves data and cannot bypass current unreadiness. Near-matching/other messages retain their latch.
- A persistent LuCI widget with cached invalid state/error/tooltip clears on a ready response, updates on a new refusal, preserves selection/unsaved inputs and makes no UCI/RPC write or probe. Test English and Russian, using native validation methods or a faithful stateful substitute rather than a newly created widget per lookup.
- Go baseline/second version, race/vet, frontend/package/recovery checks and exact-head official SDK succeed; controller/LuCI packages become 0.1.3-r1.
- Before device tests provide emergency/remove/downgrade commands and stage immutable previous 0.1.2 backend/0.1.1 UI. Preserve current automatic/3h/1GiB/Russian configuration, budgets and schedules. Confirm live hashes, ready watchdog/lease and loaded LuCI. No manual probes, state injection, hardware disconnect or weakening readiness on the router. Simulated missing-readiness/validation races are established in regression tests; current live watchdog is healthy.

# Design and acceptance

## Accepted scope

The user approved a permanent fix after the diagnosis: retry after a safe precommit refusal, retaining validation and fail-closed behavior for uncertain writes/conflicts. No additional design approval is required for this focused repair.

## Error boundary

Introduce a typed/sentinel deferred-apply error emitted only before Recovery.Arm and the mutating iptables-restore. Distinguish current incompatibility from generation mismatch so an unsupported backend is never mislabeled. Generation mismatch at either validation point and a changed current apply opt-in/configuration are safe precommit deferrals. Do not classify unknown errors, policy chain changes/conflicts, validation failures, recovery readiness failures or errors after lease arm as deferred.

Reconcile records the transient reason without assigning ApplyBlocked, does not commit stale rules or update LastApply/AppliedGeneration, and makes no in-call retry. The next normal tick discovers and calculates again; normal watchdog, configuration, lock, topology, leaf ownership, opt-in, hysteresis and minimum-apply checks still apply. Successful application/equivalent-rule reconciliation clears transient errors without clearing unrelated persistent blockers.

Before classifying a leaf as foreign, Reconcile revalidates discovery and the current leaf under the stock shared lock. A real WAN transition plus native hotplug rebuild must defer safely; an unchanged-generation foreign leaf still blocks. Release this guard lock before Adapter.Apply reacquires it for the full transaction. This also prevents a stale initial read from misclassifying a leaf that has since become legitimate stock/owned.

## Upgrade

On loading state, retire only exact old latch strings `generation changed during transaction validation` and `stale generation or incompatible current configuration`; both originate before any write in v0.1.0-r2. Retain every other blocker and all samples/schedules/counters. No manual state/budget deletion. The next Apply still verifies the current chain and recovery gates; migration does not bypass them.

## Acceptance

1. A generation change injected during --test results in zero mutating writes, then automatic application succeeds on a later stable tick without rollback/restart/mode toggling.
2. A stale initial snapshot is also retryable, including a real offline WAN plus stock leaf rebuild; incompatible snapshots remain fail-closed and accurately diagnosed. Conflict classification is based on a current locked snapshot, not an older caller snapshot.
3. Repeated topology changes do not form an immediate retry loop or trigger extra probes. Observe/disabled mode cannot apply after a concurrent opt-in change.
4. Persisted legacy safe latch loads without that latch and resumes under ordinary gates, preserving samples and schedules; unknown, conflict and postcommit failure latches survive reload.
5. Existing postcommit rollback, ownership conflict, watchdog, concurrency and budget tests continue to pass.
6. Upgrade the controller package to 0.1.2-r1; keep the unchanged LuCI 0.1.1-r1 artifact. Device validation preserves user mode=automatic, interval=10800 and daily budget=1073741824, currently modified by the user, and verifies fresh readiness and actual selected-policy weights without manual probes/USB resets.

## Deployment and recovery

Use official SDK output for the reviewed commit. Before device tests provide existing emergency disable/service-delete/mwan3-restart and package-removal commands. Stage the exact previous controller IPK and retain a private state/config/budget snapshot for downgrade. Do not reboot, disconnect USB, restart network or overwrite base mwan3 settings. Read back installed package version/hash, compatibility, watchdog, mode/config/budget preservation and runtime weights. Publish reviewed source and release assets in the existing authorized public GitHub project.

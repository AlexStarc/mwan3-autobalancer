# Implementation Plan

**Goal:** Recover safely from read-only watchdog availability refusals and refresh cached LuCI validation.

**Architecture:** Narrow availability error classification before arm; ordinary next-tick retry; existing readiness/ownership/recovery protections; public native widget revalidation.

**Tech Stack:** Go 1.23+, native LuCI JS validation, existing fixtures and official SDK.

- [ ] Review design/plan and current proof, then implement backend typed availability, pre-arm conversion and exact legacy migration with meaningful red→green tests. Preserve all hard safety failures and quota/schedule behavior.
- [ ] Implement frontend mode revalidation and persistent-widget EN/RU regression tests; prove the old inline-error cache fails the scenario. Preserve unsaved values and avoid UCI/RPC writes.
- [ ] Bump both package versions to 0.1.3-r1 and update version fixtures. Run Go/full/race/vet, native UI/package/recovery tests and OpenSpec strict validation.
- [ ] Independently review spec compliance then safety/quality, resolve findings, reread final diff and publish/attach PR. Complete exact-head CI and SDK artifact validation.
- [ ] Stage previous immutable backend/UI packages and private baseline; give rollback/removal/downgrade instructions before installation. Upgrade only owned packages, preserve all current settings/state/quota; verify real readiness and native widget without simulated live watchdog loss.
- [ ] Publish verified release/source and validation limits, reread publication, archive completed OpenSpec change and report actual outcome.

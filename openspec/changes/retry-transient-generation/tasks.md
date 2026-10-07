# Implementation Plan

> For agentic workers: use subagent-driven-development and independent spec/quality review. The design is design.md in this change.

**Goal:** Retry safe precommit changes without permanently disabling automatic weights.

**Architecture:** Typed precommit deferral, normal tick retry, narrow legacy latch migration, existing safety gates unchanged.

**Tech Stack:** Go 1.23+, existing controller fixtures, OpenWrt SDK, ARM64 router.

- [ ] Write regression tests in internal/controller covering during-validation change, stale snapshot, next stable tick, persisted latch migration, opt-in changes and hard failure retention. Demonstrate the real reproduction fails on the previous implementation.
- [ ] Implement deferred error classification in adapter.go (or one focused helper), and Engine error handling/state migration. Keep precommit/postcommit boundary explicit.
- [ ] Run regression tests, go test ./..., go test -race ./... and go vet ./... with supported baseline Go. Verify no additional measurement scheduling/budget effects.
- [ ] Bump package/mwan3-autobalancer/Makefile to 0.1.2-r1 and adjust only relevant package/version fixtures or checks. Update concise user documentation of automatic retry and remaining limitations.
- [ ] Independently review spec compliance, then correctness/safety. Resolve material findings, reread final diff and rerun affected checks.
- [ ] Commit, push, create/attach PR; complete exact-head CI and official SDK build. Merge reviewed PR and publish v0.1.2 using verified SDK controller plus unchanged released UI.
- [ ] Provide rollback commands before device validation. Stage exact old package and snapshot private config/state/budgets; upgrade only backend, preserving current settings.
- [ ] Confirm old latch recovers via upgraded code and automatic selected-policy weights apply with no manual probe/state reset, other rules/config/budget unchanged. Report verified results and outstanding physical USB fault separately.

# Implementation Plan

> For agentic workers: use subagent-driven-development and independent spec/quality review. The design is design.md in this change.

**Goal:** Retry safe precommit changes without permanently disabling automatic weights.

**Architecture:** Typed precommit deferral, normal tick retry, narrow legacy latch migration, existing safety gates unchanged.

**Tech Stack:** Go 1.23+, existing controller fixtures, OpenWrt SDK, ARM64 router.

- [x] Write regression tests in internal/controller covering during-validation change, stale snapshot, next stable tick, persisted latch migration, opt-in changes and hard failure retention. Demonstrate the real reproduction fails on the previous implementation.
- [x] Implement deferred error classification in adapter.go (or one focused helper), and Engine error handling/state migration. Keep precommit/postcommit boundary explicit.
- [x] Run regression tests, go test ./..., go test -race ./... and go vet ./... with supported baseline Go. Verify no additional measurement scheduling/budget effects.
- [x] Bump package/mwan3-autobalancer/Makefile to 0.1.2-r1 and adjust only relevant package/version fixtures or checks. Update concise user documentation of automatic retry and remaining limitations.
- [x] Independently review spec compliance, then correctness/safety. Resolve material findings, reread final diff and rerun affected checks.
- [x] Commit, push, create/attach PR; complete exact-head CI and official SDK build. Merge reviewed PR and publish v0.1.2 using verified SDK controller plus unchanged released UI.
- [x] Provide rollback commands before device validation. Stage exact old package and snapshot private config/state/budgets; upgrade only backend, preserving current settings.
- [x] Confirm normal automatic selected-policy reconciliation after upgrade, with other rules/config/budget unchanged and no manual probe/state reset. Legacy-latch migration is covered by regression tests: the live latch had already cleared before installation. Report this limit and pre-existing USB/probe faults separately.

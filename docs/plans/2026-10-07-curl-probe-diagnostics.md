# Curl probe diagnostics implementation plan

> Execute inline with systematic debugging, independent review, and verification-before-completion. The user authorized the fix, installation, and existing GitHub publication workflow.

**Goal:** Preserve the real curl failure instead of masking it with invalid JSON.

**Architecture:** Use curl's native JSON write-out under the existing minimum version gate. Preserve runner causes when parsing fails. Keep bounded successful and duration-capped measurement behavior, quotas, and routing gates.

**Tech Stack:** Go 1.23+, curl >= 8.4, OpenWrt 24.10.8 SDK, mwan3 2.11.16-r5.

## Task 1: Regressions

Files: create `internal/controller/probe_test.go`; reuse the existing `makeFixture` in `safety_test.go`.

- [x] Extract the production `--write-out` argument in a fixture runner and invoke real curl on a nonexistent local file. Require transfer error identity, valid zero-code statistics, no speed sample, one request, and retained budget charge.
- [x] Exercise malformed/missing statistics with runner failure, malformed success statistics, timeout before response/body, valid capped HTTP 200/206 transfers, successful HTTP 206, non-success HTTP, and other curl failures.
- [x] Run the focused tests and record expected failures against the existing implementation.

## Task 2: Minimal fix

Files: `internal/controller/probe.go`.

- [x] Replace the custom statistics template with `%{json}\n`.
- [x] On parse failure with a runner error, return `errors.Join(fmt.Errorf("probe transfer failed: %w", runErr), err)`.
- [x] Require HTTP 200/206 and nonzero body bytes before considering a duration-limited curl exit 28 a sample; retain all later checks.
- [x] Run focused regressions green and review the diff.

## Task 3: Validate and package

Files: backend `package/mwan3-autobalancer/Makefile` version 0.1.4 only; document verified results.

- [x] Validate OpenSpec, run Go 1.23.12 and 1.27.1 tests/race/vet, OpenWrt recovery/package/UI fixtures, and independent read-only review.
- [x] Commit/push the exact reviewed change in a GitHub PR, attach it, wait for exact-head CI, and verify the SDK artifact digest and static ARM64 payload.
- [x] Provide rollback/disable/remove commands before device tests; stage the old backend package and capture a fresh baseline.
- [x] Upgrade backend, read back installed hashes/configuration/quotas/automatic readiness, queue one regular bounded observation run, and verify actual failure text or valid rates without resetting budgets.
- [x] Merge, publish backend 0.1.4 with unchanged UI 0.1.3, verify remote release metadata and asset digests, and archive the OpenSpec change after completion.

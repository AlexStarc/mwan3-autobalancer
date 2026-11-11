# Automatic Yandex probe discovery Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use subagent-driven-development to execute this plan with spec and quality review.

**Goal:** Obtain Yandex download targets automatically, avoiding manual replacement when CDN addresses rotate.

**Architecture:** Add an opt-in `probe_provider=yandex`; the existing `url` provider remains the installation default. Resolve through each WAN using the existing bounded curl runner. Store validated targets in a private runtime cache keyed by interface/device/source address. Refresh after 24 hours or three target failures, with a ten-minute discovery retry cooldown. Failed refresh retains a usable cached target. Dynamic targets do not enter measurement fingerprints. Reserve metadata traffic within the persistent WAN budget. Never modify routing as part of discovery.

**Tech Stack:** Go 1.23+, OpenWrt curl/mwan3, LuCI JavaScript, EN/RU gettext.

## Task 1: Backend discovery and regression coverage

- [x] Add `ProbeProvider` parsing/validation in `internal/controller/config.go`, default `url`; allow Yandex measurements without a manual URL.
- [x] Add `internal/controller/yandex.go` and tests: bounded source-bound metadata request to `https://yandex.ru/internet/api/v0/get-probes`, strict HTTPS Yandex CDN download target validation, per-WAN private persistent runtime cache, TTL/failure refresh/cooldown and malformed metadata handling.
- [x] Integrate in `probe.go` and `engine.go`: account metadata budget, preserve route/source/generation checks and payload limits; count target failures only; preserve sample history across discovery refresh. A discovery cooldown must defer the next attempt without consuming calibration attempts; extend the calibration deadline to permit the scheduled retry, while bounding actual failed network attempts to the existing five-attempt limit. Cover recovery in on-change mode and legacy URL behavior.
- [ ] Run focused Go tests, then full tests/race/vet with supported local toolchains.

## Task 2: Minimal UI, configuration and documentation

- [x] Add provider selector to `openwrt/luci/view/network/mwan3-autobalancer.js`, default/manual config in `openwrt/config/mwan3_autobalancer`, EN/RU descriptions/catalogs and `tests/test_ui.js` coverage.
- [x] Document Yandex behavior, cache timing, budgets and undocumented API limitation in README; bump both packages and fixtures to 0.1.5.
- [x] Run OpenWrt/package/UI fixtures and strict OpenSpec validation.

## Task 3: Review, release and router validation

- [ ] Independent spec compliance review, then code quality review; resolve findings and rerun relevant checks.
- [ ] Publish branch/PR; require exact-head CI including actual OpenWrt SDK package build before merge/release.
- [ ] Stage verified previous packages and configuration backup; show concrete rollback/removal commands before installing or testing on router.
- [ ] Install verified new packages, enable Yandex provider retaining manual URL and all other settings; read state back and run bounded native probes on all online channels.
- [ ] Verify per-WAN cache, accepted samples, watchdog readiness and unchanged routing configuration; publish release and record actual results.

# Go N-WAN Controller Implementation Plan

> For agentic workers: use subagent-driven-development for independent implementation/review tasks. Follow the accepted design and recovery contract. Do not reuse KIT source or copy private router configuration into this public repository.

**Goal:** Deliver a Go controller with minimal LuCI UI, meaningful tests, an ARM64 build and recoverable observation-mode installation on the user's OpenWrt router.

**Architecture:** Pure-Go controller orchestrates bounded curl probes through stock mwan3, discovers members through validated UCI/ubus data, persists probe budgets and updates only one existing IPv4 policy chain atomically under the stock procd lock. LuCI JS uses a narrow rpcd CLI bridge and existing ACL/authentication. An independent shell watchdog and stock mwan3 recovery remain usable if Go fails.

**Tech stack:** Go 1.24+ standard library, CGO_ENABLED=0 release profile, OpenWrt mwan3 2.11.16/iptables-legacy, curl, procd/rpcd/LuCI.

## File boundaries and shared contract

- `go.mod`, `cmd/mwan3-autobalancer/`, `internal/`: controller, engine, numeric model, validated platform adapter and tests.
- `openwrt/`: UCI defaults, procd service, independent restore/watchdog scripts, rpcd executable/ACL/menu and one LuCI JS view.
- `package/`: OpenWrt package Makefiles, dependencies and uninstall contract.
- `scripts/`, `.github/workflows/`: reproducible local checks, cross-build/artifact checks and CI.
- `docs/RECOVERY.ru.md`: commands already provided to user; keep service/package names identical.

Status JSON: `policy`, `mode`, `compatible`, `compatibility_error`, `busy`, `last_error`, `lease_active`, `channels[]`. Each channel includes `member`, `interface`, `device`, `online`, `baseline_weight`, `proposed_weight`, `applied_weight`, nullable `speed_mbps`, nullable `age_seconds`, `valid_samples`, `probe_state`, `probe_error`, `budget_used_bytes`, `budget_limit_bytes`. Additive fields are allowed, but these names/semantics must remain stable.

CLI: `status` prints one JSON object; `probe` queues one asynchronous dry-run probe cycle; `once --dry-run` performs bounded measurement/calculate without policy change; `once --apply` is gated on supported adapter and explicit opt-in; `daemon` maintains heartbeat and accepts bounded local Unix-socket control messages; `rollback` uses independent restore. No new HTTP listener. UI and CLI share status/state.

UCI package `mwan3_autobalancer`, section `main`: `enabled=1`, `mode=observe`, `policy=balanced`, empty `probe_url`, `interval_seconds=3600`, `probe_bytes=33554432`, `daily_budget_bytes=268435456` per logical WAN, `timeout_seconds=15`, `minimum_seconds=2`, `minimum_bytes=262144`, `alpha=0.25`, `max_age_seconds=86400`, `hysteresis_percent=5`, `minimum_apply_seconds=60`. Empty URL disables automatic probes; installation does not turn on applying. Validate all fields in backend, regardless of UI validation.

Optional UCI sections `wan` identify a logical `interface` and override `interval_seconds`, `probe_bytes` and `daily_budget_bytes`; unspecified fields inherit explicitly configured main settings. Validate duplicate/unknown interface overrides and numeric bounds. UI exposes these per-WAN overrides on the same page. Probe scheduling and persistent quota are keyed by logical interface, never the transient Linux device.

Runtime directory `/var/run/mwan3-autobalancer`; persistent budgets `/etc/mwan3-autobalancer/budgets`. Lease and heartbeat are armed before any transaction. Policy names/identifiers are strict validated strings, never arbitrary shell input. Snapshot generation covers UCI digest, member/device/address/metric/online state and marks; a changed generation cancels the apply.

Every applying entry point, including once --apply, fails closed unless an independent watchdog readiness record is fresh (15-second maximum age) and its recorded PID still belongs to the expected watchdog script. Watchdog writes its readiness every 5 seconds from Linux uptime. Controller heartbeat also uses uptime. A lease alone is insufficient. Readiness is rechecked under the shared lock immediately before arming/applying. Expose apply_ready and apply_unavailable_reason in status for the UI.

## Task 1: Go engine, platform adapter and tests

- [ ] Add tests first for N=1,2,3,60; full 80/40/10 shares, partial 80/40/unknown shares, zero/invalid/stale values, priority groups and duplicate WAN rejection.
- [ ] Implement independent model with EMA, two-valid-sample requirement, unknown share reservation, largest-remainder positive weights summing 1000 and hysteresis.
- [ ] Test and implement atomic budget reservation with file locking, same-filesystem rename, sync, UTC rollover, reboot persistence and backwards-clock protection. Reserve maximum bytes before any curl invocation, including failed probes.
- [ ] Implement bounded command runner with child process-group termination/reaping and bounded stdout/stderr. Curl probes use mwan3 use plus explicit device/source validation; verify HTTP code, actual payload and transfer time; do not count redirects/error pages as speed.
- [ ] Implement UCI/ubus discovery and strict 2.11.16/iptables-legacy compatibility gating. Unknown/unsupported states remain visible and never trigger apply.
- [ ] Test and implement only-selected-chain transaction generation, stock lock `/var/lock/procd_mwan3.lock`, re-snapshot, iptables-restore --test/--noflush, readback and independent rollback. Never write measured weights to `/etc/config/mwan3`, restart mwan3, flush conntrack or touch LAN/firewall/Wi-Fi config.
- [ ] Verify independent watchdog readiness in all applying paths; absent, stale, wrong-PID or failed watchdog must leave the chain unchanged. Test differing per-WAN intervals and budgets and duplicate override rejection.
- [ ] Add daemon/CLI JSON status and Unix-socket controls. Serialize cycles and reject duplicate probe work; heartbeat must continue during bounded measurements; clean up child processes and socket on shutdown.
- [ ] Run `go test ./...`, `go test -race ./...`, `go vet ./...`, Linux ARM64 cross-build and inspect ELF. Review against spec before code-quality review; address findings.

## Task 2: Minimal authenticated UI, service and packaging

- [ ] Add procd service `mwan3-autobalancer` with limited fast respawns, finite termination timeout, controller and independent watchdog instances. Observation default, no automatic probes with empty URL.
- [ ] Add shell restore script using installed mwan3 library under its shared lock, and watchdog that restores lease policy after missing heartbeat without using Go. Validate lease input. On stop/remove restore current stock policy, not an outdated backup.
- [ ] Add rpcd object `mwan3.autobalancer`: status (read), probe and rollback (write), no caller-supplied command. CLI probes are asynchronous; UI repeats do not create concurrent jobs.
- [ ] Add one LuCI page Network → Auto-balancer: responsive WAN table, actual/unknown/stale/progress/error/budget states, policy and probe settings, observation/automatic mode selector, buttons Probe and Restore. Existing login/ACL; no raw command fields.
- [ ] On the same page, expose per-logical-WAN override sections for interval, probe payload and daily budget; show inherited settings when no override exists. Disable automatic mode when apply_ready is false and show the reason.
- [ ] Add `mwan3-autobalancer` and `luci-app-mwan3-autobalancer` package definitions, supported dependencies and uninstall scripts matching recovery commands. Check JS syntax, JSON ACL/menu, shell syntax and package file manifest.
- [ ] Add unit/integration fixtures for RPC validation, UI unknown values, duplicate jobs, failed startup, expired lease, unsafe policy names and bounded respawn behavior. Review spec first, then quality.

## Task 3: Device validation and publication

- [ ] Publish recovery guide before device installation (already provided in chat); read stock service delete and restore capabilities, snapshot current UCI privately on the router.
- [ ] Build ARM64 artifact with SHA256 and check architecture/static profile. Install owned packages in observation mode without changes to base network configuration. Read back package/service/RPC status.
- [ ] Check LuCI UI through authenticated local browser where available; RPC/JS checks alone are not visual evidence. If browser access unavailable, clearly report visual verification gap.
- [ ] Run limited dry-run measurements, verify egress/generation and budget/state. Do not enable recurring tests without explicit URL/budget settings.
- [ ] Perform one controlled policy update and read back only its changed chain, unchanged permanent UCI, other chains and active connections. Roll back afterward.
- [ ] Kill controller to verify independent watchdog recovery; simulate startup failure only in owned service/artifact and verify stock routing survives and recovery commands work. Never reboot/factory-reset the router to test crash handling.
- [ ] Run required local checks and review full change. Commit, push to public GitHub and read back exact head/content. Leave persistent automatic applying disabled pending the user's chosen traffic settings; document verified versus unverified behavior.

Go toolchain for this session is downloaded and SHA256-verified under workspace `work/toolchain/go`; set writable GOCACHE/GOPATH before local builds. No compiler or interpreter should be installed on the router.

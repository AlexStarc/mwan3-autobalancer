package controller

import (
	"context"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
	"time"
)

func TestWatchdogMissingWaitsForGenuineReadinessOnNormalTick(t *testing.T) {
	x, e, _ := calibratedAutomaticEngine(t)
	if err := os.Remove(filepath.Join(x.dir, "watchdog.ready")); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Budgets.Reserve(context.Background(), "a", x.now, 47, 123); err != nil {
		t.Fatal(err)
	}
	quota, _ := os.ReadFile(e.Budgets.Path)
	samples, schedule := persistedJSON(t, e.state.Samples), persistedJSON(t, e.state.Schedule)
	for i := 0; i < 4; i++ {
		if err := e.Tick(context.Background()); err == nil {
			t.Fatal("missing readiness was accepted")
		}
		if e.state.ApplyBlocked != "" {
			t.Fatalf("missing watchdog readiness permanently latched: %q", e.state.ApplyBlocked)
		}
		if e.state.LastError == "" || e.state.ApplyDeferred == "" || x.runner.count("iptables-restore") != 0 || x.runner.count("mwan3") != 0 || x.cfg.Values["main"].Text("mode") != "automatic" {
			t.Fatal("missing readiness wrote rules, probed, paused, or lost diagnostic", e.state)
		}
		for _, file := range []string{"lease.json", x.adapter.Recovery.HeartbeatName()} {
			if _, err := os.Stat(filepath.Join(x.dir, file)); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("unready tick wrote recovery ownership", file, err)
			}
		}
		x.now = x.now.Add(20 * time.Second)
	}
	x.ready(t)
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	currentQuota, _ := os.ReadFile(e.Budgets.Path)
	if mutatingRestoreCount(x) != 1 || e.state.ApplyBlocked != "" || e.state.ApplyDeferred != "" || e.state.LastError != "" || x.runner.count("mwan3") != 0 || string(quota) != string(currentQuota) || persistedJSON(t, e.state.Samples) != samples || persistedJSON(t, e.state.Schedule) != schedule {
		t.Fatal("genuine readiness did not resume automatic applying with preserved calibration/quota", e.state)
	}
}

func writeWatchdogRecord(t *testing.T, x *fixture, pid int, uptime float64) {
	t.Helper()
	if err := AtomicJSON(filepath.Join(x.dir, "watchdog.ready"), WatchdogReady{PID: pid, Uptime: uptime}); err != nil {
		t.Fatal(err)
	}
}

func TestWatchdogReadinessAvailabilityBoundary(t *testing.T) {
	for _, kind := range []string{
		"fresh", "age-limit", "missing", "expired", "vanished",
		"invalid-pid", "negative-time", "future-time", "wrong-command", "expired-wrong-command", "expired-invalid-pid",
		"process-permission", "expired-process-permission", "process-io", "process-unknown", "uptime-error", "uptime-nan", "uptime-infinite",
		"corrupt-record", "null-record", "missing-uptime", "null-uptime", "extra-json", "record-io", "record-permission",
	} {
		t.Run(kind, func(t *testing.T) {
			x := makeFixture(t)
			x.ready(t)
			path := filepath.Join(x.dir, "watchdog.ready")
			var wantIs error
			switch kind {
			case "age-limit":
				writeWatchdogRecord(t, x, 987, x.up-15)
			case "missing":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				wantIs = os.ErrNotExist
			case "expired":
				writeWatchdogRecord(t, x, 987, x.up-15.01)
			case "vanished":
				wantIs = os.ErrNotExist
				x.adapter.Recovery.ReadProc = func(int) ([]byte, error) {
					return nil, &os.PathError{Op: "open", Path: "/proc/987/cmdline", Err: os.ErrNotExist}
				}
			case "invalid-pid", "expired-invalid-pid":
				writeWatchdogRecord(t, x, 1, 0)
			case "negative-time":
				writeWatchdogRecord(t, x, 987, -1)
			case "future-time":
				writeWatchdogRecord(t, x, 987, x.up+1)
			case "wrong-command", "expired-wrong-command":
				if kind == "expired-wrong-command" {
					writeWatchdogRecord(t, x, 987, 0)
				}
				x.adapter.Recovery.ReadProc = func(int) ([]byte, error) {
					return []byte("/bin/sh\x00/usr/libexec/mwan3-autobalancer/watchdog-imposter\x00"), nil
				}
			case "process-permission", "expired-process-permission":
				if kind == "expired-process-permission" {
					writeWatchdogRecord(t, x, 987, 0)
				}
				wantIs = os.ErrPermission
				x.adapter.Recovery.ReadProc = func(int) ([]byte, error) { return nil, os.ErrPermission }
			case "process-io":
				wantIs = syscall.EIO
				x.adapter.Recovery.ReadProc = func(int) ([]byte, error) { return nil, syscall.EIO }
			case "process-unknown":
				wantIs = errors.New("unknown proc inspection failure")
				x.adapter.Recovery.ReadProc = func(int) ([]byte, error) { return nil, wantIs }
			case "uptime-error":
				wantIs = os.ErrNotExist
				x.adapter.Recovery.Now = func() (float64, error) { return 0, wantIs }
			case "uptime-nan":
				x.adapter.Recovery.Now = func() (float64, error) { return math.NaN(), nil }
			case "uptime-infinite":
				x.adapter.Recovery.Now = func() (float64, error) { return math.Inf(1), nil }
			case "corrupt-record", "null-record", "missing-uptime", "null-uptime", "extra-json":
				data := map[string]string{
					"corrupt-record": "not JSON", "null-record": "null", "missing-uptime": `{"pid":987}`,
					"null-uptime": `{"pid":987,"uptime":null}`, "extra-json": `{"pid":987,"uptime":100} {"pid":987,"uptime":100}`,
				}[kind]
				if err := os.WriteFile(path, []byte(data), 0600); err != nil {
					t.Fatal(err)
				}
			case "record-io":
				if err := os.Remove(path); err != nil {
					t.Fatal(err)
				}
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			case "record-permission":
				if err := os.Chmod(path, 0000); err != nil {
					t.Fatal(err)
				}
				defer os.Chmod(path, 0600)
				if _, err := os.ReadFile(path); err == nil {
					t.Skip("reader can bypass file permissions")
				}
				wantIs = os.ErrPermission
			}
			err := x.adapter.Recovery.Ready()
			if kind == "fresh" || kind == "age-limit" {
				if err != nil {
					t.Fatal("genuine fresh watchdog rejected", err)
				}
				return
			}
			available := kind == "missing" || kind == "expired" || kind == "vanished"
			var unavailable *WatchdogUnavailableError
			if err == nil || errors.As(err, &unavailable) != available {
				t.Fatal("readiness boundary misclassified", kind, err)
			}
			converted := deferUnavailableWatchdog(err)
			var deferred *DeferredApplyError
			if errors.As(converted, &deferred) != available || converted.Error() != err.Error() {
				t.Fatal("adapter readiness conversion changed classification/reason", converted, err)
			}
			if available && !errors.As(fmt.Errorf("outer: %w", converted), &unavailable) {
				t.Fatal("typed readiness was lost through wrapping", converted)
			}
			if wantIs != nil && !errors.Is(converted, wantIs) {
				t.Fatal("underlying read failure was lost", converted, wantIs)
			}
		})
	}
}

func TestWatchdogAvailabilityLossAtEveryApplyReadinessGate(t *testing.T) {
	for _, stage := range []struct {
		name string
		gate int
	}{{"initial", 1}, {"locked", 2}, {"arm", 3}} {
		for _, kind := range []string{"missing", "expired", "vanished"} {
			t.Run(stage.name+"/"+kind, func(t *testing.T) {
				x, e, s := calibratedAutomaticEngine(t)
				path := filepath.Join(x.dir, "watchdog.ready")
				now, proc := x.adapter.Recovery.Now, x.adapter.Recovery.ReadProc
				calls := 0
				switch kind {
				case "missing":
					if stage.gate == 1 {
						if err := os.Remove(path); err != nil {
							t.Fatal(err)
						}
					} else {
						x.adapter.Recovery.ReadProc = func(pid int) ([]byte, error) {
							calls++
							if calls == stage.gate-1 {
								if err := os.Remove(path); err != nil {
									t.Fatal(err)
								}
							}
							return proc(pid)
						}
					}
				case "expired":
					x.adapter.Recovery.Now = func() (float64, error) {
						calls++
						if calls >= stage.gate {
							return x.up + 16, nil
						}
						return now()
					}
				case "vanished":
					x.adapter.Recovery.ReadProc = func(pid int) ([]byte, error) {
						calls++
						if calls >= stage.gate {
							return nil, os.ErrNotExist
						}
						return proc(pid)
					}
				}
				var deferred *DeferredApplyError
				var unavailable *WatchdogUnavailableError
				err := e.Reconcile(context.Background(), s, false)
				if !errors.As(err, &deferred) || !errors.As(err, &unavailable) || e.state.ApplyBlocked != "" || mutatingRestoreCount(x) != 0 || x.cfg.Values["main"].Text("mode") != "automatic" {
					t.Fatal("availability failure was not a typed pre-arm refusal", err, e.state)
				}
				tests := 0
				if stage.gate == 3 {
					tests = 1
				}
				if x.runner.count("iptables-restore") != tests {
					t.Fatal("readiness gate was skipped or retried immediately")
				}
				for _, file := range []string{"lease.json", x.adapter.Recovery.HeartbeatName()} {
					if _, err = os.Stat(filepath.Join(x.dir, file)); !errors.Is(err, os.ErrNotExist) {
						t.Fatal("unready apply wrote recovery ownership", file, err)
					}
				}
				x.adapter.Recovery.Now, x.adapter.Recovery.ReadProc = now, proc
				x.ready(t)
				if err = e.Tick(context.Background()); err != nil || mutatingRestoreCount(x) != 1 || e.state.ApplyDeferred != "" || e.state.ApplyBlocked != "" || x.runner.count("mwan3") != 0 {
					t.Fatal("later genuine readiness did not resume a normal automatic tick", err, e.state)
				}
			})
		}
	}
}

func TestWatchdogArmFailuresRemainHard(t *testing.T) {
	for _, kind := range []string{
		"heartbeat-stopped", "restore-requested", "wrong-policy", "corrupt-lease", "lease-read-io",
		"heartbeat-write", "lease-write", "heartbeat-uptime", "lease-uptime", "heartbeat-unavailable-shaped-error", "lease-unavailable-shaped-error",
	} {
		t.Run(kind, func(t *testing.T) {
			x, e, s := calibratedAutomaticEngine(t)
			leasePath := filepath.Join(x.dir, "lease.json")
			switch kind {
			case "heartbeat-stopped":
				x.adapter.Recovery.HeartbeatStopped.Store(true)
			case "restore-requested", "wrong-policy":
				lease := Lease{Policy: "balanced", PID: 1234, Session: strings.Repeat("a", 32), HeartbeatFile: "heartbeat.1234.json", RestoreRequested: kind == "restore-requested"}
				if kind == "wrong-policy" {
					lease.Policy = "previous"
				}
				if err := AtomicJSON(leasePath, lease); err != nil {
					t.Fatal(err)
				}
			case "corrupt-lease":
				if err := os.WriteFile(leasePath, []byte("corrupt JSON"), 0600); err != nil {
					t.Fatal(err)
				}
			case "lease-read-io":
				if err := os.Mkdir(leasePath, 0700); err != nil {
					t.Fatal(err)
				}
			case "heartbeat-write":
				if err := os.Mkdir(filepath.Join(x.dir, x.adapter.Recovery.HeartbeatName()), 0700); err != nil {
					t.Fatal(err)
				}
			case "lease-write", "heartbeat-uptime", "lease-uptime", "heartbeat-unavailable-shaped-error", "lease-unavailable-shaped-error":
				now := x.adapter.Recovery.Now
				calls := 0
				x.adapter.Recovery.Now = func() (float64, error) {
					calls++
					// Initial, locked and Arm Ready checks consume calls 1..3.
					// Heartbeat reads uptime at 4, and the lease timestamp at 5.
					if calls == 4 {
						if kind == "lease-write" {
							if err := os.Mkdir(leasePath, 0700); err != nil {
								t.Fatal(err)
							}
						}
						if kind == "heartbeat-uptime" {
							return 0, os.ErrNotExist
						}
						if kind == "heartbeat-unavailable-shaped-error" {
							return 0, &WatchdogUnavailableError{Cause: errors.New("unknown heartbeat uptime failure")}
						}
					}
					if calls == 5 {
						if kind == "lease-uptime" {
							return 0, os.ErrNotExist
						}
						if kind == "lease-unavailable-shaped-error" {
							return 0, &WatchdogUnavailableError{Cause: errors.New("unknown lease uptime failure")}
						}
					}
					return now()
				}
			}
			leaseBefore, _ := os.ReadFile(leasePath)
			var deferred *DeferredApplyError
			err := e.Reconcile(context.Background(), s, false)
			if err == nil || errors.As(err, &deferred) || e.state.ApplyBlocked == "" || e.state.ApplyDeferred != "" || mutatingRestoreCount(x) != 0 || x.runner.count("iptables-restore") != 1 || x.runner.count("mwan3") != 0 {
				t.Fatal("Arm failure was reclassified or wrote policy rules", kind, err, e.state)
			}
			leaseAfter, _ := os.ReadFile(leasePath)
			if string(leaseBefore) != string(leaseAfter) {
				t.Fatal("failed Arm changed a preexisting lease", kind)
			}
		})
	}
}

func TestWatchdogPostArmFailuresRemainHardAndRecover(t *testing.T) {
	for _, typed := range []bool{false, true} {
		t.Run(fmt.Sprintf("typed=%v", typed), func(t *testing.T) {
			x, e, s := calibratedAutomaticEngine(t)
			base := x.runner.fn
			x.runner.fn = func(args []string, input string) (string, error) {
				out, err := base(args, input)
				if args[0] == "iptables-restore" && args[1] == "--noflush" {
					if typed {
						return out, &WatchdogUnavailableError{Cause: os.ErrNotExist}
					}
					return out, errors.New("watchdog readiness expired")
				}
				return out, err
			}
			var deferred *DeferredApplyError
			err := e.Reconcile(context.Background(), s, false)
			if err == nil || errors.As(err, &deferred) || e.state.ApplyBlocked == "" || e.state.ApplyDeferred != "" || mutatingRestoreCount(x) != 1 || x.runner.count("/usr/libexec/mwan3-autobalancer/restore") != 1 || x.save != fixtureStockLeaf() || x.cfg.Values["main"].Text("mode") != "observe" {
				t.Fatal("possibly committed failure was not hard-blocked and independently restored", err, e.state)
			}
		})
	}
}

func TestWatchdogExactLegacyLatchMigrationWaitsForReadiness(t *testing.T) {
	const legacy = "watchdog readiness: open /var/run/mwan3-autobalancer/watchdog.ready: no such file or directory"
	for _, blocker := range []string{
		legacy,
		"watchdog readiness is stale",
		legacy + ": unknown failure",
		"watchdog readiness: open /different/watchdog.ready: no such file or directory",
		"watchdog readiness: open /var/run/mwan3-autobalancer/watchdog.ready: permission denied",
		"watchdog PID is not the independent watchdog",
	} {
		t.Run(blocker, func(t *testing.T) {
			x, e, _ := calibratedAutomaticEngine(t)
			e.state.ApplyBlocked, e.state.LastError = blocker, blocker
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if err := os.Remove(filepath.Join(x.dir, "watchdog.ready")); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Budgets.Reserve(context.Background(), "a", x.now, 47, 123); err != nil {
				t.Fatal(err)
			}
			quota, _ := os.ReadFile(e.Budgets.Path)
			settings, _ := x.adapter.ReadFile("/etc/config/mwan3_autobalancer")
			reloaded := makeEngine(t, x)
			want := e.state
			if blocker == legacy {
				want.ApplyBlocked, want.ApplyDeferred = "", blocker
			}
			if persistedJSON(t, reloaded.state) != persistedJSON(t, want) {
				t.Fatal("legacy readiness migration changed unrelated state or misclassified latch", reloaded.state, want)
			}
			var deferred *DeferredApplyError
			err := reloaded.Tick(context.Background())
			if blocker == legacy {
				if !errors.As(err, &deferred) || reloaded.state.ApplyBlocked != "" {
					t.Fatal("migrated readiness still latched", err, reloaded.state)
				}
			} else if err != nil || reloaded.state.ApplyBlocked != blocker {
				t.Fatal("migration retired another hard readiness error", err, reloaded.state)
			}
			if x.runner.count("iptables-restore") != 0 || x.runner.count("mwan3") != 0 {
				t.Fatal("migration bypassed real readiness or added a probe")
			}
			if _, err = os.Stat(filepath.Join(x.dir, "lease.json")); !errors.Is(err, os.ErrNotExist) {
				t.Fatal("migration armed a lease without genuine readiness", err)
			}
			x.ready(t)
			if err = reloaded.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if blocker == legacy {
				if mutatingRestoreCount(x) != 1 || reloaded.state.ApplyDeferred != "" || reloaded.state.LastError != "" {
					t.Fatal("genuine readiness did not resume upgraded state", reloaded.state)
				}
			} else if mutatingRestoreCount(x) != 0 || reloaded.state.ApplyBlocked != blocker {
				t.Fatal("genuine readiness cleared an unrelated hard latch", reloaded.state)
			}
			currentQuota, _ := os.ReadFile(e.Budgets.Path)
			currentSettings, _ := x.adapter.ReadFile("/etc/config/mwan3_autobalancer")
			if string(quota) != string(currentQuota) || string(settings) != string(currentSettings) || persistedJSON(t, reloaded.state.Samples) != persistedJSON(t, e.state.Samples) || persistedJSON(t, reloaded.state.Schedule) != persistedJSON(t, e.state.Schedule) || x.runner.count("mwan3") != 0 {
				t.Fatal("readiness migration altered quota, settings or calibration")
			}
		})
	}
}

func TestWatchdogUnavailablePreservesExistingRecoveryOwnership(t *testing.T) {
	x, e, s := calibratedAutomaticEngine(t)
	lines, _ := chainLines(x.save, "mwan3_policy_balanced")
	if err := x.adapter.Recovery.Arm("balanced", ruleHash(lines)); err != nil {
		t.Fatal(err)
	}
	leasePath := filepath.Join(x.dir, "lease.json")
	heartbeatPath := filepath.Join(x.dir, x.adapter.Recovery.HeartbeatName())
	lease, _ := os.ReadFile(leasePath)
	heartbeat, _ := os.ReadFile(heartbeatPath)
	if err := os.Remove(filepath.Join(x.dir, "watchdog.ready")); err != nil {
		t.Fatal(err)
	}
	var deferred *DeferredApplyError
	if err := e.Reconcile(context.Background(), s, false); !errors.As(err, &deferred) || e.state.ApplyBlocked != "" {
		t.Fatal("owned lease did not safely await watchdog", err, e.state)
	}
	currentLease, _ := os.ReadFile(leasePath)
	currentHeartbeat, _ := os.ReadFile(heartbeatPath)
	if string(lease) != string(currentLease) || string(heartbeat) != string(currentHeartbeat) || x.adapter.Recovery.HeartbeatStopped.Load() || x.runner.count("iptables-restore") != 0 || x.runner.count("uci") != 0 || e.report.ApplyReady {
		t.Fatal("read-only availability refusal changed existing recovery protection")
	}
}

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func calibratedAutomaticEngine(t *testing.T) (*fixture, *Engine, Snapshot) {
	t.Helper()
	x := makeFixture(t)
	x.ready(t)
	x.cfg.Values["main"]["mode"] = "automatic"
	e := makeEngine(t, x)
	s, err := e.refresh(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range s.Channels {
		sc := e.state.Schedule[c.Interface]
		sc.Complete = true
		sc.Next = x.now.Add(6 * time.Hour)
		e.state.Schedule[c.Interface] = sc
		e.state.Samples[c.Interface] = Sample{Speed: float64(80 / (i + 1)), Count: 2, At: x.now, Generation: sc.Epoch}
	}
	return x, e, s
}

func mutatingRestoreCount(x *fixture) int {
	x.runner.mu.Lock()
	defer x.runner.mu.Unlock()
	n := 0
	for _, args := range x.runner.calls {
		if len(args) > 1 && args[0] == "iptables-restore" && args[1] == "--noflush" {
			n++
		}
	}
	return n
}

// Compare every persisted field. JSON preserves timestamps but does not preserve
// time.Time's internal Local/UTC location identity across a reload.
func persistedJSON(t *testing.T, value any) string {
	t.Helper()
	data, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	return string(data)
}

func TestDeferredGenerationRecoversOnLaterAutomaticTick(t *testing.T) {
	x, e, s := calibratedAutomaticEngine(t)
	base := x.runner.fn
	x.runner.fn = func(args []string, input string) (string, error) {
		out, err := base(args, input)
		if args[0] == "iptables-restore" && args[1] == "--test" {
			x.raw = "configuration generation changed during validation"
		}
		return out, err
	}
	before := e.state
	var deferred *DeferredApplyError
	if err := e.Reconcile(context.Background(), s, false); !errors.As(err, &deferred) {
		t.Fatal("stale validation did not produce a typed deferral", err)
	}
	if e.state.ApplyBlocked != "" {
		t.Fatalf("safe precommit generation change permanently latched: %q", e.state.ApplyBlocked)
	}
	if mutatingRestoreCount(x) != 0 || x.runner.count("iptables-restore") != 1 || x.save != fixtureStockLeaf() || e.state.LastApply != before.LastApply || e.state.AppliedGeneration != before.AppliedGeneration || !reflect.DeepEqual(e.state.Weights, before.Weights) {
		t.Fatal("deferral wrote rules, retried immediately, or changed applied state")
	}
	if e.state.LastError == "" {
		t.Fatal("missing deferred diagnostic")
	}
	if _, err := os.Stat(filepath.Join(x.dir, "lease.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("safe precommit refusal armed a lease", err)
	}
	x.runner.fn = base
	if err := e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if mutatingRestoreCount(x) != 1 || e.state.ApplyBlocked != "" || e.state.LastError != "" || !reflect.DeepEqual(e.state.Weights, []int{667, 333}) {
		t.Fatal("stable automatic tick did not recover", e.state)
	}
	if x.runner.count("mwan3") != 0 {
		t.Fatal("safe retry triggered extra probes")
	}
}

func TestDeferredStaleInitialSnapshot(t *testing.T) {
	x, e, s := calibratedAutomaticEngine(t)
	x.raw = "changed before locked discovery"
	var deferred *DeferredApplyError
	if err := e.Reconcile(context.Background(), s, false); !errors.As(err, &deferred) || e.state.ApplyBlocked != "" {
		t.Fatal("stale initial snapshot was not deferred", err, e.state.ApplyBlocked)
	}
	if x.runner.count("iptables-restore") != 0 || mutatingRestoreCount(x) != 0 {
		t.Fatal("stale initial snapshot reached transaction validation")
	}
	if err := e.Tick(context.Background()); err != nil || mutatingRestoreCount(x) != 1 || x.runner.count("mwan3") != 0 {
		t.Fatal("stable automatic tick did not recover without probes", err)
	}
}

func TestDeferredRepeatedChangesWaitForNormalTicks(t *testing.T) {
	x, e, _ := calibratedAutomaticEngine(t)
	base := x.runner.fn
	changes := 0
	x.runner.fn = func(args []string, input string) (string, error) {
		out, err := base(args, input)
		if args[0] == "iptables-restore" && args[1] == "--test" {
			changes++
			x.raw = fmt.Sprintf("configuration generation %d", changes)
		}
		return out, err
	}
	for i := 1; i <= 4; i++ {
		var deferred *DeferredApplyError
		if err := e.Tick(context.Background()); !errors.As(err, &deferred) || e.state.ApplyBlocked != "" {
			t.Fatal("change did not remain deferred", err, e.state)
		}
		if changes != i || x.runner.count("iptables-restore") != i || mutatingRestoreCount(x) != 0 || x.runner.count("mwan3") != 0 {
			t.Fatal("tick retried immediately or added a probe", i, changes)
		}
		if !e.state.LastApply.IsZero() || e.state.AppliedGeneration != "" {
			t.Fatal("deferral changed application state")
		}
		persisted := makeEngine(t, x)
		if persisted.state.ApplyBlocked != "" || persisted.state.ApplyDeferred == "" || persisted.state.LastError != e.state.LastError {
			t.Fatal("deferred diagnostic was not persisted", persisted.state)
		}
		x.now = x.now.Add(20 * time.Second)
	}
	x.runner.fn = base
	if err := e.Tick(context.Background()); err != nil || mutatingRestoreCount(x) != 1 || e.state.LastError != "" || e.state.ApplyDeferred != "" {
		t.Fatal("later stable automatic tick did not clear transient error", err, e.state)
	}
}

func TestDeferredConcurrentOptInChanges(t *testing.T) {
	for _, change := range []struct{ option, value string }{{"mode", "observe"}, {"enabled", "0"}, {"interval_seconds", "10800"}} {
		for _, stage := range []string{"validation", "final-config"} {
			t.Run(change.option+"/"+stage, func(t *testing.T) {
				x, e, s := calibratedAutomaticEngine(t)
				base := x.runner.fn
				autoReads := 0
				x.runner.fn = func(args []string, input string) (string, error) {
					if args[0] == "ubus" && args[2] == "uci" && strings.Contains(args[4], "mwan3_autobalancer") {
						autoReads++
						if stage == "final-config" && autoReads == 3 {
							x.cfg.Values["main"][change.option] = change.value
						}
					}
					out, err := base(args, input)
					if stage == "validation" && args[0] == "iptables-restore" && args[1] == "--test" {
						x.cfg.Values["main"][change.option] = change.value
					}
					return out, err
				}
				var deferred *DeferredApplyError
				if err := e.Reconcile(context.Background(), s, false); !errors.As(err, &deferred) || e.state.ApplyBlocked != "" || mutatingRestoreCount(x) != 0 {
					t.Fatal("concurrent opt-in/config change not safely deferred", err, e.state)
				}
				x.runner.fn = base
				if err := e.Tick(context.Background()); err != nil {
					t.Fatal(err)
				}
				want := 0
				if change.option == "interval_seconds" {
					want = 1
				}
				if mutatingRestoreCount(x) != want || x.runner.count("mwan3") != 0 || x.cfg.Values["main"].Text(change.option) != change.value {
					t.Fatal("later tick ignored current opt-in/config or added probes")
				}
			})
		}
	}
}

func TestDeferredClassificationPreservesHardFailures(t *testing.T) {
	for _, kind := range []string{"snapshot-incompatible", "fresh-incompatible", "validation-incompatible", "unknown-test-error", "changed-leaf", "post-arm-failure", "watchdog-invalid"} {
		t.Run(kind, func(t *testing.T) {
			x, e, s := calibratedAutomaticEngine(t)
			base := x.runner.fn
			validated := false
			if kind == "snapshot-incompatible" {
				s.Compatible = false
				s.CompatibilityError = "apply requires iptables legacy"
			}
			if kind == "watchdog-invalid" {
				if err := AtomicJSON(filepath.Join(x.dir, "watchdog.ready"), WatchdogReady{PID: 1, Uptime: x.up}); err != nil {
					t.Fatal(err)
				}
			}
			x.runner.fn = func(args []string, input string) (string, error) {
				if args[0] == "iptables" && (kind == "fresh-incompatible" || kind == "validation-incompatible" && validated) {
					return "iptables v1.8.10 (nf_tables)", nil
				}
				if args[0] == "iptables-restore" && args[1] == "--test" && kind == "unknown-test-error" {
					return "", errors.New("generation changed during transaction validation: unknown execution failure")
				}
				out, err := base(args, input)
				if args[0] == "iptables-restore" && args[1] == "--test" {
					validated = true
					if kind == "validation-incompatible" {
						x.raw = "changed generation plus unsupported backend"
					}
					if kind == "changed-leaf" {
						x.save = strings.Replace(x.save, "0.50000000000", "0.50100000000", 1)
					}
				}
				if args[0] == "iptables-restore" && args[1] == "--noflush" && kind == "post-arm-failure" {
					return out, errors.New("post-commit status lost")
				}
				return out, err
			}
			var deferred *DeferredApplyError
			err := e.Reconcile(context.Background(), s, false)
			if err == nil || errors.As(err, &deferred) || e.state.ApplyBlocked == "" || e.state.ApplyDeferred != "" {
				t.Fatal("hard failure classified as retryable", err, e.state)
			}
			if strings.Contains(kind, "incompatible") && !strings.Contains(err.Error(), "apply requires iptables legacy") {
				t.Fatal("incompatibility diagnostic obscured", err)
			}
			if kind == "post-arm-failure" {
				if mutatingRestoreCount(x) != 1 || x.runner.count("/usr/libexec/mwan3-autobalancer/restore") != 1 || x.cfg.Values["main"].Text("mode") != "observe" {
					t.Fatal("post-arm failure did not retain independent recovery")
				}
			} else if mutatingRestoreCount(x) != 0 {
				t.Fatal("precommit hard failure wrote rules")
			}
		})
	}
}

func TestDeferredLegacyLatchMigrationPreservesStateAndQuota(t *testing.T) {
	for _, blocker := range []string{
		"generation changed during transaction validation",
		"stale generation or incompatible current configuration",
		"policy leaf conflict; automatic mode paused: <nil>",
		"possibly committed transaction failed: timeout",
		"owner heartbeat failed: I/O error",
		"generation changed during transaction validation: unknown execution failure",
	} {
		t.Run(blocker, func(t *testing.T) {
			x, e, s := calibratedAutomaticEngine(t)
			e.state.ApplyBlocked = blocker
			e.state.LastError = blocker
			e.state.Weights = []int{667, 333}
			e.state.LastApply = x.now.Add(-time.Minute)
			e.state.AppliedGeneration = s.Generation
			e.state.ProbeErrors["b"] = "server diagnostic"
			if err := e.save(); err != nil {
				t.Fatal(err)
			}
			if _, err := e.Budgets.Reserve(context.Background(), "a", x.now, 123, 456); err != nil {
				t.Fatal(err)
			}
			quota, _ := os.ReadFile(e.Budgets.Path)
			settings, _ := x.adapter.ReadFile("/etc/config/mwan3_autobalancer")
			reloaded := makeEngine(t, x)
			want := e.state
			safe := blocker == "generation changed during transaction validation" || blocker == "stale generation or incompatible current configuration"
			if safe {
				want.ApplyBlocked = ""
				want.ApplyDeferred = blocker
			}
			if persistedJSON(t, reloaded.state) != persistedJSON(t, want) {
				t.Fatal("upgrade changed unrelated state or incorrectly migrated blocker", reloaded.state, want)
			}
			if err := reloaded.Tick(context.Background()); err != nil {
				t.Fatal(err)
			}
			if safe {
				if mutatingRestoreCount(x) != 1 || reloaded.state.LastError != "" || reloaded.state.ApplyDeferred != "" {
					t.Fatal("safe legacy latch did not resume automatically", reloaded.state)
				}
			} else if reloaded.state.ApplyBlocked != blocker || mutatingRestoreCount(x) != 0 {
				t.Fatal("upgrade removed hard latch", reloaded.state)
			}
			currentQuota, _ := os.ReadFile(e.Budgets.Path)
			currentSettings, _ := x.adapter.ReadFile("/etc/config/mwan3_autobalancer")
			if string(quota) != string(currentQuota) || string(settings) != string(currentSettings) || x.runner.count("mwan3") != 0 || persistedJSON(t, reloaded.state.Samples) != persistedJSON(t, e.state.Samples) || persistedJSON(t, reloaded.state.Schedule) != persistedJSON(t, e.state.Schedule) {
				t.Fatal("migration/retry changed quota, settings, samples, schedule or added probes")
			}
		})
	}
}

func TestDeferredSuccessOnlyClearsMatchingDiagnostic(t *testing.T) {
	for _, equivalent := range []bool{false, true} {
		for _, unrelated := range []bool{false, true} {
			for _, automaticTick := range []bool{false, true} {
				t.Run(fmt.Sprintf("equivalent=%v/unrelated=%v/tick=%v", equivalent, unrelated, automaticTick), func(t *testing.T) {
					x, e, s := calibratedAutomaticEngine(t)
					if equivalent {
						if err := e.Reconcile(context.Background(), s, false); err != nil {
							t.Fatal(err)
						}
					}
					before := mutatingRestoreCount(x)
					e.state.ApplyDeferred = "generation changed during transaction validation"
					e.state.LastError = e.state.ApplyDeferred
					if unrelated {
						e.state.LastError = "probe server unavailable"
					}
					var err error
					if automaticTick {
						err = e.Tick(context.Background())
					} else {
						err = e.Reconcile(context.Background(), s, false)
					}
					if err != nil {
						t.Fatal(err)
					}
					wantError := ""
					if unrelated {
						wantError = "probe server unavailable"
					}
					if e.state.ApplyDeferred != "" || e.state.LastError != wantError || equivalent && mutatingRestoreCount(x) != before {
						t.Fatal("successful reconciliation erased unrelated error or rewrote equivalent rules", e.state)
					}
				})
			}
		}
	}
}

func TestDeferredDiagnosticSurvivesMinimumApplyWait(t *testing.T) {
	x, e, s := calibratedAutomaticEngine(t)
	e.state.ApplyDeferred = "generation changed during transaction validation"
	e.state.LastError = e.state.ApplyDeferred
	e.state.Weights = []int{500, 500}
	e.state.LastApply = x.now
	e.state.AppliedGeneration = s.Generation
	if err := e.Reconcile(context.Background(), s, false); err != nil {
		t.Fatal(err)
	}
	if mutatingRestoreCount(x) != 0 || e.state.LastError != e.state.ApplyDeferred || e.state.LastError == "" {
		t.Fatal("minimum apply wait wrote rules or cleared pending diagnostic", e.state)
	}
	x.now = x.now.Add(s.Config.MinimumApply)
	if err := e.Reconcile(context.Background(), s, false); err != nil {
		t.Fatal(err)
	}
	if mutatingRestoreCount(x) != 1 || e.state.ApplyDeferred != "" || e.state.LastError != "" {
		t.Fatal("successful apply after minimum wait did not clear diagnostic", e.state)
	}
}

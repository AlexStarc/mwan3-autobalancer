package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"
)

func disconnectFixtureWAN(x *fixture) {
	read := x.adapter.ReadFile
	x.adapter.ReadFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "iface_state/a") {
			return []byte("offline"), nil
		}
		return read(path)
	}
	base := x.runner.fn
	x.runner.fn = func(args []string, input string) (string, error) {
		if args[0] == "ubus" && args[2] == "network.interface.a" {
			return `{"up":false,"l3_device":"","ipv4-address":[]}`, nil
		}
		return base(args, input)
	}
	// Installed stock mwan3 clears the old active group when b is the sole online WAN.
	x.save = "*mangle\n:mwan3_policy_balanced - [0:0]\n:mwan3_policy_other - [0:0]\n-A mwan3_policy_other -j RETURN\n" +
		`-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m comment --comment "b 1 1" -j MARK --set-xmark 0x300/0x3f00` + "\nCOMMIT\n"
}

func TestDeferredHotplugRebuildBetweenSnapshotAndReconcile(t *testing.T) {
	x, e, stale := calibratedAutomaticEngine(t)
	disconnectFixtureWAN(x)
	current, err := x.adapter.Discover(context.Background())
	if err != nil || current.Generation == stale.Generation || current.Channels[0].Online || current.Channels[0].Device != "" || !NativeBaseline(x.save, current) || NativeBaseline(x.save, stale) {
		t.Fatal("fixture does not represent a WAN disconnect with a native stock rebuild", current, err)
	}
	stock := x.save
	before := e.state
	var deferred *DeferredApplyError
	if err = e.Reconcile(context.Background(), stale, false); !errors.As(err, &deferred) {
		t.Fatal("ordinary WAN disconnect was treated as a foreign conflict", err)
	}
	if x.cfg.Values["main"].Text("mode") != "automatic" || e.state.ApplyBlocked != "" || x.runner.count("uci") != 0 || x.runner.count("iptables-restore") != 0 || x.save != stock || e.state.LastApply != before.LastApply || !reflect.DeepEqual(e.state.Weights, before.Weights) {
		t.Fatal("stale hotplug reconciliation paused, wrote rules, or changed applied state", e.state)
	}
	if _, err = os.Stat(filepath.Join(x.dir, "lease.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("hotplug deferral armed a lease", err)
	}
	if err = e.Tick(context.Background()); err != nil {
		t.Fatal(err)
	}
	if e.state.ApplyBlocked != "" || e.state.LastError != "" || x.cfg.Values["main"].Text("mode") != "automatic" || !reflect.DeepEqual(e.state.Weights, []int{0, 1000}) || mutatingRestoreCount(x) != 1 || x.runner.count("mwan3") != 0 {
		t.Fatal("later stable automatic tick did not reconcile disconnected WAN without probing", e.state)
	}
}

func TestDeferredLeafReturnsToStockOrOwnedBeforeLockedInspection(t *testing.T) {
	for _, owned := range []bool{false, true} {
		t.Run(map[bool]string{false: "stock", true: "owned"}[owned], func(t *testing.T) {
			x, e, s := calibratedAutomaticEngine(t)
			if owned {
				if err := e.Reconcile(context.Background(), s, false); err != nil {
					t.Fatal(err)
				}
			}
			legitimate := x.save
			leaseBefore, leaseErr := os.ReadFile(filepath.Join(x.dir, "lease.json"))
			if owned && leaseErr != nil {
				t.Fatal(leaseErr)
			}
			x.save = strings.Replace(legitimate, "--set-xmark", "--set-mark", 1)
			base := x.runner.fn
			reads := 0
			x.runner.fn = func(args []string, input string) (string, error) {
				out, err := base(args, input)
				if args[0] == "iptables-save" {
					reads++
					if reads == 1 {
						x.save = legitimate
					}
				}
				return out, err
			}
			before := mutatingRestoreCount(x)
			var deferred *DeferredApplyError
			if err := e.Reconcile(context.Background(), s, false); !errors.As(err, &deferred) || e.state.ApplyBlocked != "" || x.runner.count("uci") != 0 || mutatingRestoreCount(x) != before {
				t.Fatal("already recovered leaf was incorrectly paused", err, e.state)
			}
			leaseAfter, _ := os.ReadFile(filepath.Join(x.dir, "lease.json"))
			if string(leaseBefore) != string(leaseAfter) {
				t.Fatal("deferral changed existing lease")
			}
			x.runner.fn = base
			if err := e.Tick(context.Background()); err != nil || e.state.ApplyDeferred != "" || e.state.LastError != "" || e.state.ApplyBlocked != "" || x.runner.count("mwan3") != 0 {
				t.Fatal("later stable tick did not reconcile legitimate leaf", err, e.state)
			}
		})
	}
}

func requireStockLockHeld(t *testing.T, path string) {
	t.Helper()
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
	if err == nil {
		_ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN)
		t.Fatal("potential conflict was inspected or paused without the stock lock")
	}
	if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
		t.Fatal(err)
	}
}

func TestForeignLeafRevalidationAndPauseHoldStockLock(t *testing.T) {
	x, e, s := calibratedAutomaticEngine(t)
	x.save = strings.Replace(x.save, "--set-xmark", "--set-mark", 1)
	base := x.runner.fn
	lockedReads, lockedPauses := 0, 0
	x.runner.fn = func(args []string, input string) (string, error) {
		if args[0] == "ubus" && args[2] == "uci" {
			requireStockLockHeld(t, x.adapter.LockPath)
			lockedReads++
		}
		if args[0] == "uci" {
			requireStockLockHeld(t, x.adapter.LockPath)
			lockedPauses++
		}
		return base(args, input)
	}
	var deferred *DeferredApplyError
	err := e.Reconcile(context.Background(), s, false)
	if err == nil || errors.As(err, &deferred) || e.state.ApplyBlocked == "" || x.cfg.Values["main"].Text("mode") != "observe" || lockedReads != 2 || lockedPauses != 2 || mutatingRestoreCount(x) != 0 {
		t.Fatal("unchanged-generation foreign leaf was not hard blocked under lock", err, e.state)
	}
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	unlock, err := fileLock(ctx, x.adapter.LockPath)
	if err != nil {
		t.Fatal("revalidation leaked the shared lock", err)
	}
	unlock()
}

func TestPotentialConflictInspectionFailuresRemainBlocked(t *testing.T) {
	for _, failure := range []string{"discovery", "incompatible", "rules-unreadable"} {
		t.Run(failure, func(t *testing.T) {
			x, e, s := calibratedAutomaticEngine(t)
			x.save = strings.Replace(x.save, "--set-xmark", "--set-mark", 1)
			base := x.runner.fn
			reads := 0
			x.runner.fn = func(args []string, input string) (string, error) {
				if failure == "discovery" && args[0] == "ubus" && args[2] == "uci" {
					return "", errors.New("discovery unavailable")
				}
				if failure == "incompatible" && args[0] == "iptables" {
					return "iptables v1.8.10 (nf_tables)", nil
				}
				if args[0] == "iptables-save" {
					reads++
					if failure == "rules-unreadable" && reads > 1 {
						return "", errors.New("iptables inspection unavailable")
					}
				}
				return base(args, input)
			}
			var deferred *DeferredApplyError
			err := e.Reconcile(context.Background(), s, false)
			if err == nil || errors.As(err, &deferred) || e.state.ApplyBlocked == "" || e.state.ApplyDeferred != "" || x.runner.count("uci") != 0 || x.runner.count("iptables-restore") != 0 {
				t.Fatal("failed current inspection assumed ownership or allowed apply", err, e.state)
			}
			if failure == "incompatible" && !strings.Contains(err.Error(), "apply requires iptables legacy") {
				t.Fatal("incompatibility diagnostic obscured", err)
			}
		})
	}
}

func TestHotplugGenerationChangeCannotClaimForeignLeaf(t *testing.T) {
	x, e, stale := calibratedAutomaticEngine(t)
	disconnectFixtureWAN(x)
	x.save = strings.Replace(x.save, "--set-xmark", "--set-mark", 1)
	var deferred *DeferredApplyError
	if err := e.Reconcile(context.Background(), stale, false); !errors.As(err, &deferred) || e.state.ApplyBlocked != "" || x.runner.count("uci") != 0 {
		t.Fatal("changed generation was not deferred without pausing", err, e.state)
	}
	if err := e.Tick(context.Background()); err == nil || errors.As(err, &deferred) || e.state.ApplyBlocked == "" || x.cfg.Values["main"].Text("mode") != "observe" || mutatingRestoreCount(x) != 0 {
		t.Fatal("fresh stable generation claimed a genuinely foreign leaf", err, e.state)
	}
}

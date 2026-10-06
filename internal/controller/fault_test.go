package controller

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestScaledCalibrationWindow(t *testing.T) {
	s := Snapshot{Config: DefaultConfig()}
	s.Channels = make([]Channel, 3)
	for i := range s.Channels {
		s.Channels[i].Online = true
	}
	if CalibrationWindow(s) != 10*time.Minute {
		t.Fatal(CalibrationWindow(s))
	}
	s.Channels = make([]Channel, 60)
	for i := range s.Channels {
		s.Channels[i].Online = true
	}
	if CalibrationWindow(s) != 34*time.Minute {
		t.Fatal(CalibrationWindow(s))
	}
}
func TestProbabilityQuantizationAndCounterDrift(t *testing.T) {
	a := []string{`-A p -m statistic --mode random --probability 0.60000000000 -j RETURN`}
	b := []string{`-A p -m statistic --mode random --probability 0.60000000009 -j RETURN`}
	if !equivalentRules(a, b) {
		t.Fatal("32-bit probability quantization not tolerated")
	}
	if otherRules("*mangle\n:PREROUTING ACCEPT [1:2]\nCOMMIT\n", "p") != otherRules("*mangle\n:PREROUTING ACCEPT [123:456]\nCOMMIT\n", "p") {
		t.Fatal("traffic counters mistaken for unrelated rule change")
	}
}
func TestProbeStaleGenerationAndReservationWriteFailure(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		out, err := base(a, in)
		if a[0] == "mwan3" {
			x.raw = "topology changed during probe"
		}
		return out, err
	}
	b := Budgets{filepath.Join(x.dir, "budgets")}
	if _, err = x.adapter.Probe(ctx, s, s.Channels[0], b, x.now); err == nil || !strings.Contains(err.Error(), "generation changed") {
		t.Fatal(err)
	}
	if r, _ := b.Read("a"); r.Used != s.Config.Bytes {
		t.Fatal("discarded probe lost reservation", r)
	}
	x.runner.fn = base
	s, err = x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := x.runner.count("mwan3")
	bad := Budgets{filepath.Join(x.dir, "watchdog.ready", "budgets")}
	x.ready(t)
	if _, err = x.adapter.Probe(ctx, s, s.Channels[0], bad, x.now); err == nil {
		t.Fatal("unpersisted reservation accepted")
	}
	if x.runner.count("mwan3") != before {
		t.Fatal("network started before durable reservation")
	}
}
func TestUnsupportedLegacyAndFailedTransaction(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.ready(t)
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] == "iptables" {
			return "iptables v1.8.10 (nf_tables)", nil
		}
		return base(a, in)
	}
	s, err := x.adapter.Discover(ctx)
	if err != nil || s.Compatible {
		t.Fatal(s, err)
	}
	if err = x.adapter.Apply(ctx, s, []int{500, 500}); err == nil || x.runner.count("iptables-restore") != 0 {
		t.Fatal(err)
	}
	x.runner.fn = base
	s, err = x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := x.save
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] == "iptables-restore" && a[1] == "--test" {
			return "", errors.New("invalid transaction")
		}
		return base(a, in)
	}
	if err = x.adapter.Apply(ctx, s, []int{500, 500}); err == nil || x.save != before {
		t.Fatal("test failure changed leaf", err)
	}
}
func TestVerificationFailureInvokesIndependentRestore(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.ready(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		out, err := base(a, in)
		if a[0] == "iptables-restore" && a[1] == "--noflush" {
			x.save = strings.Replace(x.save, "-A mwan3_policy_other -j RETURN", "-A mwan3_policy_other -j DROP", 1)
		}
		return out, err
	}
	if err = x.adapter.Apply(ctx, s, []int{500, 500}); err == nil {
		t.Fatal("verification mismatch accepted")
	}
	if x.runner.count("/usr/libexec/mwan3-autobalancer/restore") != 1 {
		t.Fatal("independent restore not invoked")
	}
	lockCtx, cancel := context.WithTimeout(ctx, time.Second)
	defer cancel()
	unlock, err := fileLock(lockCtx, x.adapter.LockPath)
	if err != nil {
		t.Fatal("stock lock leaked", err)
	}
	unlock()
}

func TestProbeSourceMismatchAndDurationLimit(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := Budgets{filepath.Join(x.dir, "budgets")}
	original := x.curl
	x.curl = strings.Replace(original, `"local_ip":"192.0.2.1"`, `"local_ip":"192.0.2.5"`, 1)
	if _, err = x.adapter.Probe(ctx, s, s.Channels[0], b, x.now); err == nil || !strings.Contains(err.Error(), "source or destination changed") {
		t.Fatal(err)
	}
	x.curl = strings.Replace(original, `"time_total":3`, `"time_total":15`, 1)
	x.curlErr = &CommandError{Command: "mwan3", Code: 28, Cause: errors.New("max-time reached")}
	if speed, err := x.adapter.Probe(ctx, s, s.Channels[0], b, x.now); err != nil || speed <= 0 {
		t.Fatal(speed, err)
	}
	x.curl = strings.Replace(x.curl, `"size_download":1048576`, `"size_download":0`, 1)
	if _, err = x.adapter.Probe(ctx, s, s.Channels[0], b, x.now); err == nil {
		t.Fatal("timeout without body accepted")
	}
}

func TestLeasePolicyCannotBeOrphaned(t *testing.T) {
	x := makeFixture(t)
	x.ready(t)
	if err := x.adapter.Recovery.Arm("balanced"); err != nil {
		t.Fatal(err)
	}
	if err := x.adapter.Recovery.Arm("another"); err == nil {
		t.Fatal("previous policy lease orphaned")
	}
}

func TestExplicitRollbackPausesOwnModeBeforeRestore(t *testing.T) {
	x := makeFixture(t)
	x.cfg.Values["main"]["mode"] = "automatic"
	e := makeEngine(t, x)
	base := x.runner.fn
	paused := false
	committed := false
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] == "uci" {
			if a[1] == "set" {
				if a[2] != "mwan3_autobalancer.main.mode=observe" {
					t.Fatal(a)
				}
				paused = true
				x.cfg.Values["main"]["mode"] = "observe"
			}
			if a[1] == "commit" {
				if a[2] != "mwan3_autobalancer" {
					t.Fatal(a)
				}
				committed = true
			}
			return "", nil
		}
		if a[0] == "/usr/libexec/mwan3-autobalancer/restore" {
			if !paused || !committed || len(a) != 2 {
				t.Fatal("restore preceded persistent pause", a)
			}
		}
		return base(a, in)
	}
	r, err := e.Rollback(context.Background())
	if err != nil || r.Mode != "observe" || !committed {
		t.Fatal(r, err)
	}
	if x.runner.count("iptables-restore") != 0 {
		t.Fatal("Go implemented rollback instead of stock helper")
	}
}

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestForeignLeafIsPausedNeverReplaced(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.ready(t)
	x.cfg.Values["main"]["mode"] = "automatic"
	e := makeEngine(t, x)
	s, err := e.refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if !NativeBaseline(x.save, s) {
		t.Fatal("native fixture rejected")
	}
	x.save = strings.Replace(x.save, "--set-xmark 0x300/0x3f00", "--set-xmark 0x999/0x3f00", 1)
	before := x.save
	err = e.Reconcile(ctx, s, false)
	if err == nil || x.save != before || x.runner.count("iptables-restore") != 0 || x.cfg.Values["main"].Text("mode") != "observe" {
		t.Fatal(err, x.save)
	}
	if e.state.ApplyBlocked == "" {
		t.Fatal("conflict not locally blocked")
	}
}
func TestForeignLeafRecheckedUnderLock(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.ready(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := x.runner.fn
	reads := 0
	x.runner.fn = func(a []string, in string) (string, error) {
		out, err := base(a, in)
		if a[0] == "iptables-save" {
			reads++
			if reads == 1 {
				x.save = strings.Replace(x.save, "--set-xmark 0x300/0x3f00", "--set-xmark 0x400/0x3f00", 1)
			}
		}
		return out, err
	}
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err == nil || x.runner.count("iptables-restore") != 0 {
		t.Fatal("under-lock conflict replaced", err)
	}
	if x.cfg.Values["main"].Text("mode") != "observe" {
		t.Fatal("mode not paused")
	}
}
func TestNativeGrammarRejectsSmugglingAndForeignRules(t *testing.T) {
	x := makeFixture(t)
	s, err := x.adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	for _, rule := range []string{`-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m comment --comment "b 1 2 -j MARK --set-xmark 0x300/0x3f00"`, `-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m statistic --mode random --probability 0.5 -m comment --comment "b 1 2" -j DROP`, `-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m statistic --mode random --probability 0.5 -m comment --comment "b 1 2" -s 192.0.2.0/24 -j MARK --set-xmark 0x300/0x3f00`} {
		if _, err := parseLeafRule(rule, "mwan3_policy_balanced", s.Mask); err == nil {
			t.Fatal("foreign grammar accepted", rule)
		}
	}
	wrong := strings.Replace(x.save, "0.50000000000", "0.501", 1)
	if NativeBaseline(wrong, s) {
		t.Fatal("foreign probability accepted")
	}
	legit := []string{`-A p -m mark --mark 0x0/0x3f00 -m comment --comment "a 1 1" -j MARK --set-xmark 0x100/0x3f00`}
	smuggled := []string{`-A p -m mark --mark 0x0/0x3f00 -m comment --comment "a 1 1 -j MARK --set-xmark 0x100/0x3f00"`}
	if equivalentRules(legit, smuggled) {
		t.Fatal("verification ignored quoted comment boundaries")
	}
}
func TestStockThreeDecimalRoundingAndOfflineOutput(t *testing.T) {
	s := Snapshot{Config: DefaultConfig(), Mask: 0x3f00, Shift: 8, LastResort: "unreachable", Channels: []Channel{{Interface: "a", ID: 1, Metric: 1, BaselineWeight: 1000, Online: true, Enabled: true}, {Interface: "b", ID: 2, Metric: 1, BaselineWeight: 1000, Online: true, Enabled: true}, {Interface: "c", ID: 3, Metric: 1, BaselineWeight: 1000, Online: true, Enabled: true}}}
	leaf := ":mwan3_policy_balanced - [0:0]\n"
	for i := 2; i >= 0; i-- {
		c := s.Channels[i]
		probability := ""
		if i > 0 {
			p := float64(1000/(i+1)) / 1000
			probability = fmt.Sprintf(" -m statistic --mode random --probability %.11f", p+1e-10)
		}
		leaf += fmt.Sprintf("-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00%s -m comment --comment \"%s 1000 %d\" -j MARK --set-xmark 0x%x/0x3f00\n", probability, c.Interface, 1000*(i+1), c.ID<<8)
	}
	if !NativeBaseline(leaf, s) {
		t.Fatal(leaf)
	}
	for i := range s.Channels {
		s.Channels[i].Online = false
		s.Channels[i].Device = "eth" + strconv.Itoa(i)
	}
	leaf = ":mwan3_policy_balanced - [0:0]\n" + `-A mwan3_policy_balanced -o eth0 -m mark --mark 0x0/0x3f00 -m comment --comment "out a eth0" -j MARK --set-xmark 0x3f00/0x3f00` + "\n" + `-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -j MARK --set-xmark 0x3e00/0x3f00` + "\n"
	if !NativeBaseline(leaf, s) {
		t.Fatal("native offline output rejected")
	}
	if NativeBaseline(strings.Replace(leaf, "-o eth0", "-o eth9", 1), s) {
		t.Fatal("foreign output device accepted")
	}
}
func TestPostCommitFailuresActuallyRestore(t *testing.T) {
	for _, kind := range []string{"command-error-after-commit", "inspection-error", "restore-fails"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			x := makeFixture(t)
			x.ready(t)
			s, err := x.adapter.Discover(ctx)
			if err != nil {
				t.Fatal(err)
			}
			base := x.runner.fn
			committed := false
			x.runner.fn = func(a []string, in string) (string, error) {
				if a[0] == "iptables-save" && committed {
					return "", errors.New("inspection failed")
				}
				if a[0] == "/usr/libexec/mwan3-autobalancer/restore" {
					if kind == "restore-fails" {
						return "", errors.New("stock helper unavailable")
					}
					committed = false
				}
				out, err := base(a, in)
				if a[0] == "iptables-restore" && a[1] == "--noflush" {
					committed = true
					if kind == "command-error-after-commit" {
						return "", errors.New("exit status lost after commit")
					}
				}
				return out, err
			}
			if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err == nil {
				t.Fatal("unverified apply accepted")
			}
			if x.runner.count("/usr/libexec/mwan3-autobalancer/restore") != 1 || x.cfg.Values["main"].Text("mode") != "observe" {
				t.Fatal("no active independent restore", err)
			}
			if kind == "restore-fails" {
				var lease Lease
				if err = ReadJSON(filepath.Join(x.dir, "lease.json"), &lease); err != nil || !lease.RestoreRequested || lease.PID != x.adapter.Recovery.PID || lease.Session != x.adapter.Recovery.Session {
					t.Fatal(lease, err)
				}
				if err = x.adapter.Recovery.Heartbeat(); err == nil {
					t.Fatal("fresh heartbeat masked recovery request")
				}
			} else if x.save != fixtureStockLeaf() {
				t.Fatal("leaf not actually restored")
			}
		})
	}
}
func TestLargerProbeRetryTemplateAndQuota(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.cfg.Values["main"]["probe_url"] = "https://192.0.2.99/object?bytes={bytes}"
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	base := x.runner.fn
	payloads := []int64{}
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] != "mwan3" {
			return base(a, in)
		}
		limit := int64(0)
		for i := range a {
			if a[i] == "--max-filesize" {
				limit, _ = strconv.ParseInt(a[i+1], 10, 64)
			}
		}
		payloads = append(payloads, limit)
		if !strings.HasSuffix(a[len(a)-1], "bytes="+strconv.FormatInt(limit, 10)) {
			t.Fatal("URL bytes did not track payload", a)
		}
		duration := float64(limit) / 37500000
		data, _ := json.Marshal(CurlStats{Code: 206, Bytes: float64(limit), Start: 1, Total: 1 + duration, LocalIP: "192.0.2.1", RemoteIP: "192.0.2.99"})
		return string(data), nil
	}
	b := Budgets{filepath.Join(x.dir, "budgets")}
	speed, err := x.adapter.Probe(ctx, s, s.Channels[0], b, x.now)
	if err != nil || speed < 299 || speed > 301 || len(payloads) != 2 || payloads[1] <= payloads[0] || payloads[1] > s.Config.MaxProbeBytes {
		t.Fatal(speed, payloads, err)
	}
	r, _ := b.Read("a")
	if r.Used != payloads[0]+payloads[1] {
		t.Fatal("larger retry not reserved", r)
	}
	x.cfg.Values["main"]["daily_budget_bytes"] = "33554432"
	s, err = x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	payloads = nil
	if _, err = x.adapter.Probe(ctx, s, s.Channels[1], b, x.now); err == nil || len(payloads) != 1 {
		t.Fatal("retry ignored quota", err, payloads)
	}
}
func TestEpochOverrideIsolationAndScheduleStatus(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	e := makeEngine(t, x)
	if err := e.Cycle(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	x.now = x.now.Add(time.Minute)
	if err := e.Cycle(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	before := e.state.Samples["b"]
	x.cfg.Values["a_override"] = Section{".type": "wan", "interface": "a", "probe_bytes": "16777216", "interval_seconds": "43200", "daily_budget_bytes": "536870912"}
	r, err := e.Status(ctx)
	if err != nil || e.state.Samples["b"] != before || e.state.Samples["a"].Count != 0 {
		t.Fatal("unrelated WAN sample invalidated", r, err)
	}
	if r.Phase != "calibrating" || r.NextProbeAt == nil || !r.NextProbeAt.After(x.now) || r.Channels[1].Phase != "maintenance" {
		t.Fatal(r)
	}
	x.cfg.Values["main"]["schedule_mode"] = "on-change"
	r, err = e.Status(ctx)
	if err != nil || r.NextProbeAt == nil || r.Channels[0].NextProbeAt == nil || !r.Channels[0].NextProbeAt.After(x.now) || r.Channels[1].NextProbeAt != nil || r.Channels[1].Phase != "holding" {
		t.Fatal(r, err)
	}
	if err = e.Cycle(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	x.now = x.now.Add(time.Minute)
	if err = e.Cycle(ctx, true, false); err != nil {
		t.Fatal(err)
	}
	r, err = e.Status(ctx)
	if err != nil || r.NextProbeAt != nil || r.Channels[0].NextProbeAt != nil || r.Channels[1].NextProbeAt != nil {
		t.Fatal("finished on-change calibration still schedules probes", r, err)
	}
	read := x.adapter.ReadFile
	x.adapter.ReadFile = func(path string) ([]byte, error) {
		if strings.HasSuffix(path, "iface_state/a") {
			return []byte("offline"), nil
		}
		return read(path)
	}
	r, err = e.Status(ctx)
	if err != nil || r.NextProbeAt != nil {
		t.Fatal("offline on-change channel schedules a probe", r, err)
	}
	x.adapter.ReadFile = read
	r, err = e.Status(ctx)
	if err != nil || r.NextProbeAt == nil || r.Channels[0].NextProbeAt == nil || !r.NextProbeAt.After(x.now) || r.Channels[1].NextProbeAt != nil {
		t.Fatal("reconnect calibration has no next probe", r, err)
	}
}
func TestScheduleKnobsAndLockedModePause(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	m := x.cfg.Values["main"]
	m["calibration_interval_seconds"] = "240"
	m["calibration_window_seconds"] = "1200"
	m["settle_seconds"] = "30"
	s, err := x.adapter.Discover(ctx)
	if err != nil || s.Config.CalibrationInterval != 4*time.Minute || s.Config.Settle != 30*time.Second || CalibrationWindow(s) != 20*time.Minute {
		t.Fatal(s.Config, err)
	}
	e := makeEngine(t, x)
	_, err = e.refresh(ctx)
	if err != nil || e.state.Schedule["a"].SettleUntil != x.now.Add(30*time.Second) {
		t.Fatal(e.state.Schedule, err)
	}
	m["mode"] = "automatic"
	s, err = x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	x.ready(t)
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		out, err := base(a, in)
		if a[0] == "iptables-restore" && a[1] == "--test" {
			m["mode"] = "observe"
		}
		return out, err
	}
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, false); err == nil || x.runner.count("iptables-restore") != 1 || x.save != fixtureStockLeaf() {
		t.Fatal("pause during apply validation lost", err)
	}
	m["calibration_interval_seconds"] = "0"
	if _, err = ParseConfig(x.cfg); err == nil {
		t.Fatal("invalid calibration knob accepted")
	}
}

func TestPauseDuringProbeDoesNotApplyCapturedAutomaticMode(t *testing.T) {
	x := makeFixture(t)
	x.cfg.Values["main"]["mode"] = "automatic"
	x.ready(t)
	e := makeEngine(t, x)
	ctx := context.Background()
	_, _ = e.refresh(ctx)
	x.now = x.now.Add(time.Minute)
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		out, err := base(a, in)
		if a[0] == "mwan3" {
			x.cfg.Values["main"]["mode"] = "observe"
		}
		return out, err
	}
	if err := e.Cycle(ctx, false, true); err == nil {
		t.Fatal("pause not observed")
	}
	if x.runner.count("iptables-restore") != 0 || x.save != fixtureStockLeaf() {
		t.Fatal("captured automatic configuration still applied")
	}
}
func TestExplicitRollbackClearsLatchOnlyAfterVerifiedStock(t *testing.T) {
	for _, success := range []bool{false, true} {
		t.Run(strconv.FormatBool(success), func(t *testing.T) {
			x := makeFixture(t)
			x.ready(t)
			e := makeEngine(t, x)
			e.state.ApplyBlocked = "old conflict"
			e.state.LastError = "old conflict"
			e.Adapter.Recovery.HeartbeatStopped.Store(true)
			base := x.runner.fn
			if !success {
				x.runner.fn = func(a []string, in string) (string, error) {
					if a[0] == "/usr/libexec/mwan3-autobalancer/restore" {
						return "", errors.New("restore failed")
					}
					return base(a, in)
				}
			}
			r, err := e.Rollback(context.Background())
			if success {
				if err != nil || e.state.ApplyBlocked != "" || e.Adapter.Recovery.HeartbeatStopped.Load() || r.Mode != "observe" {
					t.Fatal(r, err, e.state.ApplyBlocked)
				}
			} else if err == nil || e.state.ApplyBlocked == "" {
				t.Fatal("failed restoration cleared latch", r, err)
			}
		})
	}
}

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type fakeRunner struct {
	mu    sync.Mutex
	calls [][]string
	fn    func([]string, string) (string, error)
}

func (f *fakeRunner) Run(_ context.Context, a []string, in string) (string, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.calls = append(f.calls, append([]string(nil), a...))
	return f.fn(a, in)
}
func (f *fakeRunner) count(command string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.calls {
		if c[0] == command {
			n++
		}
	}
	return n
}

type fixture struct {
	adapter *Adapter
	runner  *fakeRunner
	uci     UCI
	cfg     UCI
	raw     string
	save    string
	curlErr error
	curl    string
	now     time.Time
	up      float64
	dir     string
}

func makeFixture(t *testing.T) *fixture {
	t.Helper()
	x := &fixture{dir: t.TempDir(), now: time.Unix(10000, 0), up: 100, raw: "synthetic mwan3 configuration"}
	x.cfg = UCI{Values: map[string]Section{"main": {".type": "main", "enabled": "1", "mode": "observe", "policy": "balanced", "probe_url": "https://192.0.2.99/object"}}}
	x.uci = UCI{Values: map[string]Section{"globals": {".type": "globals", "mmx_mask": "0x3f00"}, "balanced": {".type": "policy", "use_member": []any{"a_member", "b_member"}, "last_resort": "default"}, "wan": {".type": "interface", ".index": float64(1), "enabled": "0"}, "a": {".type": "interface", ".index": float64(2), "enabled": "1", "family": "ipv4"}, "b": {".type": "interface", ".index": float64(48), "enabled": "1", "family": "ipv4"}, "a_member": {".type": "member", "interface": "a", "metric": "1", "weight": "1"}, "b_member": {".type": "member", "interface": "b", "metric": "1", "weight": "1"}}}
	x.save = fixtureStockLeaf()
	x.curl = `mwan3 diagnostic line` + "\n" + `{"http_code":206,"size_download":1048576,"time_total":3,"time_starttransfer":1,"local_ip":"192.0.2.1","remote_ip":"192.0.2.99"}`
	x.runner = &fakeRunner{}
	x.runner.fn = func(a []string, in string) (string, error) {
		switch a[0] {
		case "ubus":
			if a[2] == "uci" {
				if strings.Contains(a[4], "mwan3_autobalancer") {
					b, _ := json.Marshal(x.cfg)
					return string(b), nil
				}
				b, _ := json.Marshal(x.uci)
				return string(b), nil
			}
			return `{"up":true,"l3_device":"eth2","ipv4-address":[{"address":"192.0.2.1"}]}`, nil
		case "opkg":
			return "mwan3 - 2.11.16-r5", nil
		case "iptables":
			return "iptables v1.8.10 (legacy)", nil
		case "iptables-save":
			return x.save, nil
		case "iptables-restore":
			if len(a) > 1 && a[1] == "--test" {
				return "", nil
			}
			lines := strings.Split(x.save, "\n")
			out := []string{}
			for _, line := range lines {
				if strings.HasPrefix(line, "-A mwan3_policy_balanced ") {
					continue
				}
				if line == "COMMIT" {
					for _, l := range strings.Split(in, "\n") {
						if strings.HasPrefix(l, "-A ") {
							out = append(out, l)
						}
					}
				}
				out = append(out, line)
			}
			x.save = strings.Join(out, "\n")
			return "", nil
		case "uci":
			if a[1] == "set" && a[2] == "mwan3_autobalancer.main.mode=observe" {
				x.cfg.Values["main"]["mode"] = "observe"
			}
			return "", nil
		case "curl":
			return "curl 8.19.0 (aarch64-openwrt-linux)", nil
		case "ip":
			return "192.0.2.99 from 192.0.2.1 dev eth2", nil
		case "mwan3":
			return x.curl, x.curlErr
		case "/usr/libexec/mwan3-autobalancer/restore":
			x.save = fixtureStockLeaf()
			_ = os.Remove(filepath.Join(x.dir, "lease.json"))
			return "", nil
		}
		return "", fmt.Errorf("unexpected command %v", a)
	}
	recovery := Recovery{Dir: x.dir, HeartbeatStopped: new(atomic.Bool), Now: func() (float64, error) { return x.up, nil }, ReadProc: func(int) ([]byte, error) {
		return []byte("/bin/sh\x00/usr/libexec/mwan3-autobalancer/watchdog\x00"), nil
	}, PID: 1234, Session: strings.Repeat("a", 32)}
	x.adapter = &Adapter{Runner: x.runner, LockPath: filepath.Join(x.dir, "mwan3.lock"), Recovery: recovery, ReadFile: func(p string) ([]byte, error) {
		switch p {
		case "/etc/config/mwan3":
			return []byte(x.raw), nil
		case "/etc/config/mwan3_autobalancer":
			b, _ := json.Marshal(x.cfg)
			return b, nil
		}
		if strings.Contains(p, "iface_state") {
			return []byte("online"), nil
		}
		return nil, os.ErrNotExist
	}}
	return x
}
func (x *fixture) ready(t *testing.T) {
	t.Helper()
	if err := AtomicJSON(filepath.Join(x.dir, "watchdog.ready"), WatchdogReady{987, 100}); err != nil {
		t.Fatal(err)
	}
}
func TestDiscoveryOrderingAndOverrides(t *testing.T) {
	x := makeFixture(t)
	s, err := x.adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if s.Channels[0].ID != 2 || s.Channels[1].ID != 3 {
		t.Fatal(s.Channels)
	}
	x.uci.Values["modem"] = Section{".type": "interface", ".index": float64(3), "enabled": "0"}
	s, err = x.adapter.Discover(context.Background())
	if err != nil || s.Channels[1].ID != 4 {
		t.Fatal(s, err)
	}
	x.cfg.Values["override"] = Section{".type": "wan", "interface": "missing"}
	if _, err = x.adapter.Discover(context.Background()); err == nil {
		t.Fatal("unknown override accepted")
	}
	delete(x.cfg.Values, "override")
	x.uci.Values["b_member"]["interface"] = "a"
	if _, err = x.adapter.Discover(context.Background()); err == nil {
		t.Fatal("duplicate WAN accepted")
	}
}
func TestApplyGatesAndLeafTransaction(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	before := x.save
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err == nil {
		t.Fatal("absent watchdog accepted")
	}
	if x.save != before || x.runner.count("iptables-restore") != 0 {
		t.Fatal("changed chain without watchdog")
	}
	x.ready(t)
	x.raw = "changed topology"
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err == nil {
		t.Fatal("stale generation accepted")
	}
	if x.save != before {
		t.Fatal("stale chain changed")
	}
	s, err = x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(x.save, `"a 600 1000"`) || !strings.Contains(x.save, `"b 400 400"`) || !strings.Contains(x.save, "-A mwan3_policy_other -j RETURN") {
		t.Fatal(x.save)
	}
	var lease Lease
	if err = ReadJSON(filepath.Join(x.dir, "lease.json"), &lease); err != nil || lease.PID != 1234 || lease.HeartbeatFile != "heartbeat.1234.json" || lease.Session == "" {
		t.Fatal(lease, err)
	}
	x.up = 116
	if err = x.adapter.Recovery.Ready(); err == nil {
		t.Fatal("stale watchdog accepted")
	}
	s.Compatible = false
	s.CompatibilityError = "unsupported"
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err == nil {
		t.Fatal("unsupported accepted")
	}
}
func TestFallbackMarks(t *testing.T) {
	s := Snapshot{Config: DefaultConfig(), Channels: []Channel{{}}, Mask: 0x3f00, Shift: 8}
	for _, v := range []struct{ name, mark string }{{"default", "0x3f00"}, {"blackhole", "0x3d00"}, {"unreachable", "0x3e00"}} {
		s.LastResort = v.name
		rules, _, err := BuildRules(s, []int{0})
		if err != nil || !strings.Contains(rules, "--set-xmark "+v.mark+"/0x3f00") || strings.Count(rules, "COMMIT") != 1 || strings.Contains(rules, "-F\n") {
			t.Fatal(rules, err)
		}
	}
}
func TestPersistentBudgetAndFailedProbe(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := Budgets{filepath.Join(x.dir, "persistent", "budgets")}
	x.curlErr = errors.New("curl failure")
	if _, err = x.adapter.Probe(ctx, s, s.Channels[0], b, x.now); err == nil {
		t.Fatal("failed curl accepted")
	}
	r, err := b.Read("a")
	if err != nil || r.Used != 33554432 {
		t.Fatal(r, err)
	}
	if _, err = b.Reserve(ctx, "a", x.now.Add(-48*time.Hour), 1, r.Used); err == nil {
		t.Fatal("backwards clock reset quota")
	}
	b2 := Budgets{b.Path}
	if r2, _ := b2.Read("a"); r2 != r {
		t.Fatal("quota lost")
	}
	if _, err = b.Reserve(ctx, "a", x.now.Add(24*time.Hour), 7, 10); err != nil {
		t.Fatal(err)
	}
	x.curlErr = nil
	speed, err := x.adapter.Probe(ctx, s, s.Channels[1], b, x.now)
	if err != nil || speed <= 0 {
		t.Fatal(speed, err)
	}
	x.curl = strings.Replace(x.curl, `"time_total":3`, `"time_total":1.2`, 1)
	if _, err = x.adapter.Probe(ctx, s, s.Channels[1], b, x.now); err == nil || !strings.Contains(err.Error(), "insufficient confidence") {
		t.Fatal(err)
	}
}
func TestBudgetConcurrentReservations(t *testing.T) {
	b := Budgets{filepath.Join(t.TempDir(), "budgets")}
	var wg sync.WaitGroup
	var successes int
	var mu sync.Mutex
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Reserve(context.Background(), "wan", time.Unix(1000, 0), 10, 100)
			if err == nil {
				mu.Lock()
				successes++
				mu.Unlock()
			}
		}()
	}
	wg.Wait()
	r, err := b.Read("wan")
	if err != nil || r.Used != 100 || successes != 10 {
		t.Fatal(r, successes, err)
	}
}
func TestWatchdogPIDAndOwner(t *testing.T) {
	x := makeFixture(t)
	x.ready(t)
	x.adapter.Recovery.ReadProc = func(int) ([]byte, error) { return []byte("/usr/libexec/mwan3-autobalancer/watchdog-imposter\x00"), nil }
	if err := x.adapter.Recovery.Ready(); err == nil {
		t.Fatal("imposter accepted")
	}
	if err := x.adapter.Recovery.Heartbeat(); err != nil {
		t.Fatal(err)
	}
	var hb Heartbeat
	if err := ReadJSON(filepath.Join(x.dir, "heartbeat.1234.json"), &hb); err != nil || hb.PID != 1234 || hb.Session != strings.Repeat("a", 32) {
		t.Fatal(hb, err)
	}
}
func TestConfigValidationAndCurl(t *testing.T) {
	x := makeFixture(t)
	c, err := ParseConfig(x.cfg)
	if err != nil || c.Interval != 6*time.Hour || c.ScheduleMode != "hybrid" {
		t.Fatal(c, err)
	}
	for _, v := range []string{"file:///tmp/x", "https://user:pass@example.org/x", "https://example.org/x#fragment"} {
		if ValidateURL(v) == nil {
			t.Fatal(v)
		}
	}
	if !StreamingCapableCurl("curl 8.19.0 x") || StreamingCapableCurl("curl 7.88.1 x") || StreamingCapableCurl("curl 8.3.9 x") {
		t.Fatal("version gate")
	}
	x.cfg.Values["main"]["mode"] = "apply"
	if _, err := ParseConfig(x.cfg); err == nil {
		t.Fatal("mode silently enabled")
	}
}

func fixtureStockLeaf() string {
	return "*mangle\n:mwan3_policy_balanced - [0:0]\n:mwan3_policy_other - [0:0]\n-A mwan3_policy_other -j RETURN\n" + `-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m statistic --mode random --probability 0.50000000000 -m comment --comment "b 1 2" -j MARK --set-xmark 0x300/0x3f00` + "\n" + `-A mwan3_policy_balanced -m mark --mark 0x0/0x3f00 -m comment --comment "a 1 1" -j MARK --set-xmark 0x200/0x3f00` + "\nCOMMIT\n"
}

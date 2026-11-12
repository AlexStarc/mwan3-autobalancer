package controller

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func tickAt(t *testing.T, e *Engine, x *fixture, at time.Time) {
	t.Helper()
	x.now = at
	_ = e.Tick(context.Background())
}
func nextFor(t *testing.T, e *Engine, name string) time.Time {
	t.Helper()
	for _, c := range e.Report().Channels {
		if c.Interface == name && c.NextProbeAt != nil {
			return *c.NextProbeAt
		}
	}
	t.Fatalf("no next probe for %s: %+v", name, e.Report())
	return time.Time{}
}
func TestAutomaticSettleAndReconnectThrottle(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	// Existing short configuration must still respect the fixed guards.
	x.cfg.Values["main"]["settle_seconds"] = "60"
	x.cfg.Values["main"]["calibration_interval_seconds"] = "120"
	x.cfg.Values["main"]["calibration_window_seconds"] = "600"
	tickAt(t, e, x, start)
	if got := nextFor(t, e, "a"); !got.Equal(start.Add(5 * time.Minute)) {
		t.Fatal("settle deadline", got)
	}
	tickAt(t, e, x, start.Add(time.Minute))
	if x.runner.count("mwan3") != 0 {
		t.Fatal("probe before five stable minutes")
	}
	tickAt(t, e, x, start.Add(5*time.Minute))
	if x.runner.count("mwan3") != 2 {
		t.Fatal("missing initial samples")
	}
	// Epoch changes must erase samples but preserve the per-interface attempt floor.
	x.uci.Values["a"]["enabled"] = "0"
	tickAt(t, e, x, start.Add(6*time.Minute))
	if e.Report().Channels[0].Online || e.state.Samples["a"].Count != 0 {
		t.Fatal("offline not reflected immediately")
	}
	x.uci.Values["a"]["enabled"] = "1"
	tickAt(t, e, x, start.Add(7*time.Minute))
	if got := nextFor(t, e, "a"); !got.Equal(start.Add(20 * time.Minute)) {
		t.Fatal("reconnect lost throttle", got)
	}
	tickAt(t, e, x, start.Add(12*time.Minute))
	if x.runner.count("mwan3") != 2 {
		t.Fatal("reconnect triggered early download")
	}
	// Another flap resets continuous-online settling, without delaying b.
	x.uci.Values["a"]["enabled"] = "0"
	tickAt(t, e, x, start.Add(17*time.Minute))
	x.uci.Values["a"]["enabled"] = "1"
	tickAt(t, e, x, start.Add(18*time.Minute))
	tickAt(t, e, x, start.Add(20*time.Minute))
	if e.state.Samples["a"].Count != 0 || e.state.Samples["b"].Count != 2 {
		t.Fatal("channel schedules not independent", e.state.Samples)
	}
	tickAt(t, e, x, start.Add(23*time.Minute))
	if e.state.Samples["a"].Count != 1 {
		t.Fatal("stable channel never retried")
	}
}
func TestAutomaticFailureBackoffAndSuccessReset(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	x.cfg.Values["main"]["interval_seconds"] = "14400"
	x.curlErr = errors.New("transfer failed")
	tickAt(t, e, x, start)
	at := start.Add(5 * time.Minute)
	for i, delay := range []time.Duration{15 * time.Minute, 30 * time.Minute, 60 * time.Minute, 60 * time.Minute} {
		tickAt(t, e, x, at)
		if e.state.Schedule["a"].Attempts != i+1 {
			t.Fatal("attempt count", e.state.Schedule)
		}
		if got := nextFor(t, e, "a"); !got.Equal(at.Add(delay)) {
			t.Fatal("backoff", i, got, at.Add(delay))
		}
		tickAt(t, e, x, at.Add(delay-time.Second))
		if e.state.Schedule["a"].Attempts != i+1 {
			t.Fatal("early retry")
		}
		at = at.Add(delay)
	}
	x.curlErr = nil
	tickAt(t, e, x, at)
	// A reconnect starts new calibration, retaining the successful attempt floor.
	x.uci.Values["a"]["enabled"] = "0"
	tickAt(t, e, x, at.Add(time.Minute))
	x.uci.Values["a"]["enabled"] = "1"
	tickAt(t, e, x, at.Add(2*time.Minute))
	x.curlErr = errors.New("new failure")
	at = at.Add(15 * time.Minute)
	tickAt(t, e, x, at)
	if got := nextFor(t, e, "a"); !got.Equal(at.Add(15 * time.Minute)) {
		t.Fatal("success did not reset failure streak", got)
	}
}
func TestMaintenanceFailureUsesBackoff(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	x.cfg.Values["main"]["interval_seconds"] = "14400"
	tickAt(t, e, x, start)
	tickAt(t, e, x, start.Add(5*time.Minute))
	tickAt(t, e, x, start.Add(20*time.Minute))
	at := start.Add(20*time.Minute + 4*time.Hour)
	if got := nextFor(t, e, "a"); !got.Equal(at) {
		t.Fatal("ordinary interval changed", got)
	}
	x.curlErr = errors.New("maintenance failed")
	tickAt(t, e, x, at)
	if got := nextFor(t, e, "a"); !got.Equal(at.Add(15 * time.Minute)) {
		t.Fatal("failed maintenance should retry", got)
	}
	x.curlErr = nil
	tickAt(t, e, x, at.Add(15*time.Minute))
	if got := nextFor(t, e, "a"); !got.Equal(at.Add(15*time.Minute + 4*time.Hour)) {
		t.Fatal("successful maintenance interval", got)
	}
}
func TestRestartKeepsThrottleAndRearmsSettle(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	tickAt(t, e, x, start)
	tickAt(t, e, x, start.Add(5*time.Minute))
	x.now = start.Add(6 * time.Minute)
	e = makeEngine(t, x)
	tickAt(t, e, x, x.now)
	if got := nextFor(t, e, "a"); !got.Equal(start.Add(20 * time.Minute)) {
		t.Fatal("restart lost floor", got)
	}
	tickAt(t, e, x, start.Add(20*time.Minute))
	if x.runner.count("mwan3") != 4 {
		t.Fatal("retry missing")
	}
	// Even a completed schedule with an immediate due date cannot assume continuity.
	x.now = start.Add(7 * time.Hour)
	e = makeEngine(t, x)
	tickAt(t, e, x, x.now)
	if x.runner.count("mwan3") != 4 || !nextFor(t, e, "a").Equal(x.now.Add(5*time.Minute)) {
		t.Fatal("restart skipped fresh settling")
	}
}
func TestManualBypassesGuardsAndPersistsBeforeTransfer(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	base := x.runner.fn
	x.runner.fn = func(args []string, in string) (string, error) {
		if args[0] == "mwan3" {
			// Emulate a crash immediately after payload starts by inspecting on-disk state.
			var disk struct {
				Throttle map[string]struct {
					RetryAfter time.Time `json:"retry_after"`
				} `json:"probe_throttle"`
			}
			if err := ReadJSON(e.StatePath, &disk); err != nil {
				t.Fatal("attempt not persisted before network", err)
			}
			if got := disk.Throttle[args[2]].RetryAfter; !got.Equal(start.Add(15 * time.Minute)) {
				t.Fatal("attempt not persisted before network", got)
			}
		}
		return base(args, in)
	}
	if err := e.Cycle(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	x.runner.fn = base
	if x.runner.count("mwan3") != 2 {
		t.Fatal("manual guard bypass missing")
	}
	tickAt(t, e, x, start.Add(5*time.Minute))
	if x.runner.count("mwan3") != 2 {
		t.Fatal("automatic ignored manual measurement floor")
	}
}
func TestDiscoveryAndQuotaDoNotConsumeActualAttempts(t *testing.T) {
	x, calls, body := yandexFixture(t)
	*body = `{}`
	e := makeEngine(t, x)
	start := x.now
	tickAt(t, e, x, start)
	tickAt(t, e, x, start.Add(5*time.Minute))
	if *calls != 2 || e.state.Schedule["a"].Attempts != 0 {
		t.Fatal("discovery counted as measurement", *calls, e.state.Schedule)
	}
	if !nextFor(t, e, "a").Equal(start.Add(15 * time.Minute)) {
		t.Fatal("initial discovery failure hides cooldown deadline")
	}
	*body = yandexMetadata
	tickAt(t, e, x, start.Add(15*time.Minute))
	if e.state.Schedule["a"].Attempts != 1 {
		t.Fatal("discovery failure imposed measurement backoff", e.state.Schedule)
	}
	// Exhausting quota without a payload must not count another actual attempt.
	r, _ := e.Budgets.Read("a")
	_, _ = e.Budgets.Reserve(context.Background(), "a", x.now, 268435456-r.Used, 268435456)
	tickAt(t, e, x, start.Add(30*time.Minute))
	if e.state.Schedule["a"].Attempts != 1 {
		t.Fatal("quota refusal consumed measurement attempt")
	}
	// Scheduler state survives independently of the discovery cache.
	if e.StatePath != filepath.Join(x.dir, "state.json") || !strings.Contains(e.Report().Channels[0].ProbeError, "budget") {
		t.Fatal(e.Report())
	}
}

func TestRemovedChannelStartsFreshCalibration(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	tickAt(t, e, x, start)
	tickAt(t, e, x, start.Add(5*time.Minute))
	x.uci.Values["balanced"]["use_member"] = []any{"b_member"}
	tickAt(t, e, x, start.Add(6*time.Minute))
	x.uci.Values["balanced"]["use_member"] = []any{"a_member", "b_member"}
	tickAt(t, e, x, start.Add(7*time.Minute))
	if e.state.Samples["a"].Count != 0 {
		t.Fatal("sample survived removal and reappearance")
	}
	if !nextFor(t, e, "a").Equal(start.Add(20 * time.Minute)) {
		t.Fatal("removal lost throttle")
	}
}
func TestDiscoveryDeferralLeavesTimeForRemainingMeasurements(t *testing.T) {
	x, _, body := yandexFixture(t)
	*body = `{}`
	e := makeEngine(t, x)
	start := x.now
	tickAt(t, e, x, start)
	// Metadata is unavailable for two hours; payload attempts remain at zero.
	for minute := 5; minute < 125; minute += 2 {
		tickAt(t, e, x, start.Add(time.Duration(minute)*time.Minute))
	}
	*body = yandexMetadata
	tickAt(t, e, x, nextFor(t, e, "a"))
	if e.state.Schedule["a"].Attempts != 1 {
		t.Fatal(e.state.Schedule)
	}
	x.curlErr = errors.New("payload failure")
	for attempt := 2; attempt <= 5; attempt++ {
		tickAt(t, e, x, nextFor(t, e, "a"))
		if e.state.Schedule["a"].Attempts != attempt {
			t.Fatal("deferral prematurely exhausted calibration", e.state.Schedule)
		}
	}
}
func TestInFlightReportIncludesReservedThrottle(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	base := x.runner.fn
	x.runner.fn = func(args []string, in string) (string, error) {
		if args[0] == "mwan3" && nextFor(t, e, args[2]).Before(x.now.Add(15*time.Minute)) {
			t.Fatal("in-flight status hides guard")
		}
		return base(args, in)
	}
	if err := e.Cycle(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
}

func TestLegacyStateMigrationPreservesSamplesAndQuotas(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	if err := e.Cycle(context.Background(), true, false); err != nil {
		t.Fatal(err)
	}
	// A pre-0.1.6 file contains the old schedule/sample maps but no throttle map.
	e.state.ProbeThrottle = nil
	if err := e.save(); err != nil {
		t.Fatal(err)
	}
	before, _ := e.Budgets.Read("a")
	x.now = start.Add(time.Minute)
	e = makeEngine(t, x)
	tickAt(t, e, x, x.now)
	if x.runner.count("mwan3") != 2 || e.state.Samples["a"].Count != 1 {
		t.Fatal("legacy sample lost or immediate probe")
	}
	if got := nextFor(t, e, "a"); !got.Equal(x.now.Add(15 * time.Minute)) {
		t.Fatal("legacy migration did not quarantine attempts", got)
	}
	after, _ := e.Budgets.Read("a")
	if before != after {
		t.Fatal("migration modified quota", before, after)
	}
	tickAt(t, e, x, x.now.Add(15*time.Minute))
	if e.state.Samples["a"].Count != 2 {
		t.Fatal("migration blocked later calibration")
	}
}
func TestStateSaveFailurePreventsPayload(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	x.ready(t)
	e.StatePath = filepath.Join(x.dir, "watchdog.ready", "state.json")
	err := e.Cycle(context.Background(), true, false)
	if err == nil || x.runner.count("mwan3") != 0 {
		t.Fatal("download started without durable throttle", err)
	}
	throttle := e.state.ProbeThrottle["a"]
	if e.state.Schedule["a"].Attempts != 0 || throttle.Failures != 0 || !throttle.LastAttempt.IsZero() || !throttle.RetryAfter.IsZero() {
		t.Fatalf("persistence refusal consumed an actual attempt: schedule=%+v throttle=%+v", e.state.Schedule["a"], throttle)
	}
	if !nextFor(t, e, "a").Equal(x.now.Add(5 * time.Minute)) {
		t.Fatal("persistence refusal moved original automatic deadline")
	}
	e.StatePath = filepath.Join(x.dir, "state.json")
	tickAt(t, e, x, x.now.Add(5*time.Minute))
	if x.runner.count("mwan3") != 2 || e.state.Schedule["a"].Attempts != 1 || e.state.Samples["a"].Count != 1 {
		t.Fatal("repaired persistence did not allow first real measurement", e.state.Schedule)
	}
	used, err := e.Budgets.Read("a")
	if err != nil || used.Used != 2*33554432 {
		t.Fatal("conservative quota reservation was refunded", used, err)
	}
}
func TestFailureStreakSurvivesRestartAndEpochChange(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	x.curlErr = errors.New("network failed")
	tickAt(t, e, x, start)
	tickAt(t, e, x, start.Add(5*time.Minute))
	tickAt(t, e, x, start.Add(20*time.Minute))
	x.now = start.Add(21 * time.Minute)
	e = makeEngine(t, x)
	tickAt(t, e, x, x.now)
	x.uci.Values["a"]["enabled"] = "0"
	tickAt(t, e, x, start.Add(22*time.Minute))
	x.uci.Values["a"]["enabled"] = "1"
	tickAt(t, e, x, start.Add(23*time.Minute))
	if !nextFor(t, e, "a").Equal(start.Add(50 * time.Minute)) {
		t.Fatal("restart or reconnect lost failure backoff")
	}
	tickAt(t, e, x, start.Add(50*time.Minute))
	if !nextFor(t, e, "a").Equal(start.Add(110 * time.Minute)) {
		t.Fatal("failure streak reset across restart or epoch")
	}
	tickAt(t, e, x, start.Add(110*time.Minute))
	if !nextFor(t, e, "a").Equal(start.Add(170 * time.Minute)) {
		t.Fatal("failure backoff cap")
	}
}
func TestSourceEpochInvalidatesSamplesButKeepsThrottle(t *testing.T) {
	x := makeFixture(t)
	e := makeEngine(t, x)
	start := x.now
	tickAt(t, e, x, start)
	tickAt(t, e, x, start.Add(5*time.Minute))
	base := x.runner.fn
	x.runner.fn = func(args []string, in string) (string, error) {
		out, err := base(args, in)
		if len(args) > 2 && args[0] == "ubus" && args[2] == "network.interface.a" {
			out = strings.ReplaceAll(out, "192.0.2.1", "192.0.2.2")
		}
		return out, err
	}
	tickAt(t, e, x, start.Add(6*time.Minute))
	if e.state.Samples["a"].Count != 0 || e.state.Samples["b"].Count != 1 || !nextFor(t, e, "a").Equal(start.Add(20*time.Minute)) {
		t.Fatal("source change reused samples or reset throttle", e.state.Schedule, e.state.Samples)
	}
}

func TestOfflineRoutingReconcilesDuringProbeSettling(t *testing.T) {
	x := makeFixture(t)
	x.ready(t)
	x.cfg.Values["main"]["mode"] = "automatic"
	e := makeEngine(t, x)
	start := x.now
	tickAt(t, e, x, start)
	x.uci.Values["a"]["enabled"] = "0"
	tickAt(t, e, x, start.Add(time.Minute))
	if x.runner.count("mwan3") != 0 || len(e.state.Weights) != 2 || e.state.Weights[0] != 0 || e.state.Weights[1] != 1000 {
		t.Fatal("settling delayed offline exclusion", e.state.Weights)
	}
}

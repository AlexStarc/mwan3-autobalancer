package controller

import (
	"context"
	"errors"
	"path/filepath"
	"testing"
	"time"
)

func makeEngine(t *testing.T, x *fixture) *Engine {
	e := NewEngine(x.adapter, Budgets{filepath.Join(x.dir, "budgets")}, filepath.Join(x.dir, "state.json"))
	e.Now = func() time.Time { return x.now }
	return e
}
func TestCalibrationAndMaintenance(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	e := makeEngine(t, x)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("mwan3") != 0 {
		t.Fatal("no settle")
	}
	x.now = x.now.Add(time.Minute)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("mwan3") != 2 {
		t.Fatal("initial sample missing")
	}
	x.now = x.now.Add(2 * time.Minute)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if e.state.Samples["a"].Count != 2 || x.runner.count("mwan3") != 4 {
		t.Fatal("calibration incomplete")
	}
	x.now = x.now.Add(time.Hour)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("mwan3") != 4 {
		t.Fatal("frequent probes")
	}
	x.now = x.now.Add(5 * time.Hour)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("mwan3") != 6 {
		t.Fatal("maintenance missing")
	}
	x.cfg.Values["main"]["schedule_mode"] = "on-change"
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	x.now = x.now.Add(time.Minute)
	_ = e.Tick(ctx)
	x.now = x.now.Add(2 * time.Minute)
	_ = e.Tick(ctx)
	count := x.runner.count("mwan3")
	x.now = x.now.Add(48 * time.Hour)
	_ = e.Tick(ctx)
	if x.runner.count("mwan3") != count {
		t.Fatal("on-change periodic probe")
	}
	r := e.Report()
	if r.Channels[0].SpeedMbps != nil || r.Channels[0].ProbeState != "historical" {
		t.Fatal(r.Channels[0])
	}
}
func TestCalibrationFiniteFailures(t *testing.T) {
	x := makeFixture(t)
	x.curlErr = errors.New("slow transfer")
	e := makeEngine(t, x)
	ctx := context.Background()
	_ = e.Tick(ctx)
	for i := 0; i < 10; i++ {
		x.now = x.now.Add(2 * time.Minute)
		_ = e.Tick(ctx)
	}
	if n := x.runner.count("mwan3"); n != 10 {
		t.Fatal("calibration was not bounded to five attempts per channel", n)
	}
	if !e.state.Schedule["a"].Complete {
		t.Fatal("missing exhausted epoch")
	}
}
func TestTTLRecomputeAndOnChangeHold(t *testing.T) {
	ctx := context.Background()
	for _, mode := range []string{"hybrid", "on-change"} {
		t.Run(mode, func(t *testing.T) {
			x := makeFixture(t)
			x.ready(t)
			x.cfg.Values["main"]["mode"] = "automatic"
			x.cfg.Values["main"]["schedule_mode"] = mode
			e := makeEngine(t, x)
			s, err := e.refresh(ctx)
			if err != nil {
				t.Fatal(err)
			}
			e.state.Samples["a"] = Sample{Speed: 80, Count: 2, At: x.now}
			e.state.Samples["b"] = Sample{Speed: 40, Count: 2, At: x.now}
			if err = e.Reconcile(ctx, s, false); err != nil {
				t.Fatal(err)
			}
			if e.state.Weights[0] != 667 {
				t.Fatal(e.state.Weights)
			}
			count := x.runner.count("iptables-restore")
			x.now = x.now.Add(48 * time.Hour)
			if err = e.Reconcile(ctx, s, false); err != nil {
				t.Fatal(err)
			}
			if mode == "hybrid" {
				if e.state.Weights[0] != 500 || x.runner.count("iptables-restore") != count+2 {
					t.Fatal("TTL did not recompute")
				}
			} else {
				if e.state.Weights[0] != 667 || x.runner.count("iptables-restore") != count {
					t.Fatal("on-change did not hold")
				}
			}
		})
	}
}
func TestEngineHysteresisAndHotplug(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.ready(t)
	x.cfg.Values["main"]["mode"] = "automatic"
	e := makeEngine(t, x)
	s, err := e.refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = e.Reconcile(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	count := x.runner.count("iptables-restore")
	x.now = x.now.Add(time.Minute)
	e.state.Samples["a"] = Sample{Speed: 54, Count: 2, At: x.now}
	e.state.Samples["b"] = Sample{Speed: 46, Count: 2, At: x.now}
	if err = e.Reconcile(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("iptables-restore") != count {
		t.Fatal("hysteresis bypass")
	}
	x.save = fixtureStockLeaf()
	if err = e.Reconcile(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("iptables-restore") != count+2 {
		t.Fatal("stock hotplug rebuild not reconciled")
	}
	count = x.runner.count("iptables-restore")
	if err = e.Reconcile(ctx, s, false); err != nil {
		t.Fatal(err)
	}
	if x.runner.count("iptables-restore") != count {
		t.Fatal("unchanged rule rewrite")
	}
}
func TestNoURLAndProbeFailureNotOffline(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.cfg.Values["main"]["probe_url"] = ""
	e := makeEngine(t, x)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if err := e.Cycle(ctx, true, false); err == nil {
		t.Fatal("empty URL accepted")
	}
	if x.runner.count("mwan3") != 0 {
		t.Fatal("network with empty URL")
	}
	x.cfg.Values["main"]["probe_url"] = "https://192.0.2.99/object"
	x.curlErr = errors.New("curl failure")
	if err := e.Cycle(ctx, true, false); err == nil {
		t.Fatal("missing error")
	}
	r := e.Report()
	if !r.Channels[0].Online || r.Channels[0].ProbeState != "failed" || r.Channels[0].ProbeError == "" {
		t.Fatal(r.Channels[0])
	}
	if x.runner.count("iptables-restore") != 0 {
		t.Fatal("dry run changed chain")
	}
}
func TestRestoredState(t *testing.T) {
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
	restored := makeEngine(t, x)
	r, err := restored.Status(ctx)
	if err != nil || r.Channels[0].ValidSamples != 2 || r.Channels[0].SpeedMbps == nil {
		t.Fatal(r, err)
	}
}

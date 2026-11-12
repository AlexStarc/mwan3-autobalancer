package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"sync/atomic"
	"time"
)

type Schedule struct {
	Epoch            string    `json:"epoch"`
	SettleUntil      time.Time `json:"settle_until"`
	CalibrationUntil time.Time `json:"calibration_until"`
	Next             time.Time `json:"next"`
	Attempts         int       `json:"attempts"`
	Complete         bool      `json:"complete"`
	WindowSeconds    int64     `json:"window_seconds"`
}
type State struct {
	ProbeThrottle     map[string]ProbeThrottle `json:"probe_throttle"`
	Samples           map[string]Sample        `json:"samples"`
	Schedule          map[string]Schedule      `json:"schedule"`
	ProbeErrors       map[string]string        `json:"probe_errors"`
	ProbeStates       map[string]string        `json:"probe_states"`
	Weights           []int                    `json:"weights"`
	LastApply         time.Time                `json:"last_apply"`
	AppliedGeneration string                   `json:"applied_generation"`
	Generation        string                   `json:"generation"`
	LastError         string                   `json:"last_error"`
	ApplyBlocked      string                   `json:"apply_blocked,omitempty"`
	ApplyDeferred     string                   `json:"apply_deferred,omitempty"`
}
type Report struct {
	Policy                 string     `json:"policy"`
	Mode                   string     `json:"mode"`
	ScheduleMode           string     `json:"schedule_mode"`
	Compatible             bool       `json:"compatible"`
	CompatibilityError     string     `json:"compatibility_error"`
	Busy                   bool       `json:"busy"`
	LastError              string     `json:"last_error"`
	LeaseActive            bool       `json:"lease_active"`
	ApplyReady             bool       `json:"apply_ready"`
	ApplyUnavailableReason string     `json:"apply_unavailable_reason"`
	Channels               []Channel  `json:"channels"`
	Generation             string     `json:"generation"`
	Phase                  string     `json:"phase"`
	NextProbeAt            *time.Time `json:"next_probe_at"`
}
type Engine struct {
	Adapter   *Adapter
	Budgets   Budgets
	StatePath string
	Now       func() time.Time
	mu        sync.Mutex
	state     State
	busy      bool
	started   bool
	report    Report
}

func NewEngine(a *Adapter, b Budgets, statePath string) *Engine {
	e := &Engine{Adapter: a, Budgets: b, StatePath: statePath, Now: time.Now}
	if a.Recovery.HeartbeatStopped == nil {
		a.Recovery.HeartbeatStopped = new(atomic.Bool)
	}
	_ = ReadJSON(statePath, &e.state)
	if legacyDeferredLatch(e.state.ApplyBlocked) {
		e.state.ApplyDeferred = e.state.ApplyBlocked
		e.state.ApplyBlocked = ""
	}
	e.init()
	return e
}
func (e *Engine) init() {
	if e.state.ProbeThrottle == nil {
		e.state.ProbeThrottle = map[string]ProbeThrottle{}
	}
	if e.state.Samples == nil {
		e.state.Samples = map[string]Sample{}
	}
	if e.state.Schedule == nil {
		e.state.Schedule = map[string]Schedule{}
	}
	if e.state.ProbeErrors == nil {
		e.state.ProbeErrors = map[string]string{}
	}
	if e.state.ProbeStates == nil {
		e.state.ProbeStates = map[string]string{}
	}
}
func channelEpoch(s Snapshot, c Channel) string {
	target := s.Config.URL
	provider := ""
	if s.Config.ProbeProvider == "yandex" {
		target = ""
		provider = "yandex"
	}
	b, _ := json.Marshal(struct {
		Provider                                string `json:",omitempty"`
		Policy, URL                             string
		ProbeBytes, MaxProbeBytes, MinimumBytes int64
		Timeout                                 time.Duration
		MinimumSeconds, Alpha                   float64
		Member, Interface, Device, Source       string
		Metric, Weight                          int
		Online                                  bool
		Mask                                    uint32
		ID                                      int
	}{provider, s.Config.Policy, target, s.Config.For(c.Interface).Bytes, s.Config.MaxProbeBytes, s.Config.MinimumBytes, s.Config.Timeout, s.Config.MinimumSeconds, s.Config.Alpha, c.Member, c.Interface, c.Device, c.SourceIP, c.Metric, c.BaselineWeight, c.Online, s.Mask, c.ID})
	return string(b)
}
func (e *Engine) refresh(ctx context.Context) (Snapshot, error) {
	s, err := e.Adapter.Discover(ctx)
	if err != nil {
		e.mu.Lock()
		e.state.LastError = err.Error()
		e.report.LastError = err.Error()
		e.report.Phase = "error"
		e.report.ApplyReady = false
		e.report.ApplyUnavailableReason = "fresh discovery failed"
		e.mu.Unlock()
		return s, err
	}
	now := e.Now()
	e.mu.Lock()
	defer e.mu.Unlock()
	seen := make(map[string]bool, len(s.Channels))
	for _, c := range s.Channels {
		seen[c.Interface] = true
		epoch := channelEpoch(s, c)
		sc := e.state.Schedule[c.Interface]
		if !e.started {
			// Old state has no independent throttle. Its last failed attempt
			// cannot be reconstructed reliably, so migrate conservatively.
			if _, ok := e.state.ProbeThrottle[c.Interface]; !ok && sc.Epoch != "" {
				e.state.ProbeThrottle[c.Interface] = ProbeThrottle{RetryAfter: now.Add(automaticAttemptInterval)}
			}
		}
		if sc.Epoch != epoch {
			delete(e.state.Samples, c.Interface)
			window := CalibrationWindow(s)
			sc = Schedule{Epoch: epoch, SettleUntil: now.Add(settleDuration(s.Config)), CalibrationUntil: now.Add(window), Next: now.Add(settleDuration(s.Config)), WindowSeconds: int64(window / time.Second)}
			e.state.ProbeStates[c.Interface] = "settling"
			e.state.ProbeErrors[c.Interface] = ""
		}
		if !e.started && c.Online {
			sc.SettleUntil = now.Add(settleDuration(s.Config))
			extendCalibration(&sc, now.Add(CalibrationWindow(s)), s.Config)
		}
		sc.Next = nextAutomatic(sc, e.state.ProbeThrottle[c.Interface])
		if sc.Epoch != e.state.Schedule[c.Interface].Epoch || !e.started {
			extendCalibration(&sc, sc.Next.Add(CalibrationWindow(s)-settleDuration(s.Config)), s.Config)
		}
		e.state.Schedule[c.Interface] = sc
	}
	for iface := range e.state.Schedule {
		if !seen[iface] {
			delete(e.state.Schedule, iface)
			delete(e.state.Samples, iface)
			delete(e.state.ProbeStates, iface)
			delete(e.state.ProbeErrors, iface)
		}
	}
	e.started = true
	e.state.Generation = s.Generation
	e.makeReport(s, now)
	return s, nil
}
func (e *Engine) makeReport(s Snapshot, now time.Time) {
	w, err := Weights(s.Channels, e.state.Samples, now, s.Config.MaxAge)
	if err != nil {
		e.state.LastError = err.Error()
	}
	r := Report{Policy: s.Config.Policy, Mode: s.Config.Mode, ScheduleMode: s.Config.ScheduleMode, Compatible: s.Compatible, CompatibilityError: s.CompatibilityError, Busy: e.busy, LastError: e.state.LastError, Generation: s.Generation, Channels: append([]Channel(nil), s.Channels...)}
	readyErr := e.Adapter.Recovery.Ready()
	r.ApplyReady = readyErr == nil && s.Compatible
	if !s.Compatible {
		r.ApplyUnavailableReason = s.CompatibilityError
	} else if readyErr != nil {
		r.ApplyUnavailableReason = readyErr.Error()
	}
	if e.state.ApplyBlocked != "" {
		r.ApplyReady = false
		r.ApplyUnavailableReason = e.state.ApplyBlocked
	}
	var lease Lease
	r.LeaseActive = ReadJSON(filepath.Join(e.Adapter.Recovery.Dir, "lease.json"), &lease) == nil
	if r.LeaseActive && lease.RestoreRequested {
		r.ApplyReady = false
		r.ApplyUnavailableReason = "independent recovery is requested"
	}
	if r.LeaseActive && lease.Policy != s.Config.Policy {
		r.ApplyReady = false
		r.ApplyUnavailableReason = "restore the previously leased policy before switching"
	}
	if e.Adapter.Recovery.HeartbeatStopped.Load() {
		r.ApplyReady = false
		r.ApplyUnavailableReason = "restart controller after independent recovery failure"
	}
	for i := range r.Channels {
		c := &r.Channels[i]
		if len(w) > i {
			c.ProposedWeight = w[i]
		}
		if len(s.Applied) == len(s.Channels) {
			c.AppliedWeight = s.Applied[i]
		}
		sample := e.state.Samples[c.Interface]
		c.ValidSamples = sample.Count
		c.ProbeState = e.state.ProbeStates[c.Interface]
		c.ProbeError = e.state.ProbeErrors[c.Interface]
		schedule := e.state.Schedule[c.Interface]
		deadline := schedule.CalibrationUntil
		c.CalibrationDeadline = &deadline
		c.CalibrationAttempts = schedule.Attempts
		c.EffectiveCalibrationSeconds = schedule.WindowSeconds
		if !c.Online {
			c.ProbeState = "offline"
		}
		if sample.Count > 0 {
			age := now.Sub(sample.At).Seconds()
			c.AgeSeconds = &age
			if valid(sample, now, s.Config.MaxAge) {
				speed := sample.Speed
				c.SpeedMbps = &speed
			} else if sample.Count >= 2 && c.ProbeState != "measuring" {
				c.ProbeState = "historical"
			}
		}
		record, err := e.Budgets.Read(c.Interface)
		if err != nil {
			c.ProbeError = err.Error()
		}
		c.BudgetUsedBytes = record.Used
		if now.UTC().Format("2006-01-02") > record.Day {
			c.BudgetUsedBytes = 0
		}
		c.BudgetLimitBytes = s.Config.For(c.Interface).DailyBudget
		c.Phase = "holding"
		if !s.Config.Enabled || !s.Config.HasProbeTarget() {
			c.Phase = "disabled"
		} else if !c.Online {
			c.Phase = "holding"
		} else if c.ProbeError != "" {
			c.Phase = "error"
		} else if pendingCalibration(schedule, now) {
			c.Phase = "calibrating"
		} else if s.Config.ScheduleMode == "hybrid" {
			c.Phase = "maintenance"
		}
		pending := pendingCalibration(schedule, now)
		if s.Config.Enabled && s.Config.HasProbeTarget() && c.Online && (s.Config.ScheduleMode == "hybrid" || pending) {
			next := nextAutomatic(schedule, e.state.ProbeThrottle[c.Interface])
			if !schedule.Complete && (schedule.Attempts >= calibrationAttempts || now.After(schedule.CalibrationUntil)) {
				next = nextAutomatic(Schedule{Next: now.Add(s.Config.For(c.Interface).Interval), SettleUntil: schedule.SettleUntil}, e.state.ProbeThrottle[c.Interface])
			}
			if !next.After(now) {
				next = now.Add(20 * time.Second)
			}
			next = next.UTC()
			c.NextProbeAt = &next
			if r.NextProbeAt == nil || next.Before(*r.NextProbeAt) {
				rootNext := next
				r.NextProbeAt = &rootNext
			}
		}
	}
	r.Phase = "holding"
	if !s.Config.Enabled || !s.Config.HasProbeTarget() {
		r.Phase = "disabled"
	} else {
		channelError := false
		for _, c := range r.Channels {
			if c.Phase == "error" {
				channelError = true
			}
			if c.Phase == "maintenance" && r.Phase == "holding" {
				r.Phase = "maintenance"
			}
			if c.Phase == "calibrating" {
				r.Phase = "calibrating"
			}
		}
		if e.state.LastError != "" || e.state.ApplyBlocked != "" || channelError {
			r.Phase = "error"
		}
	}
	e.report = r
}
func (e *Engine) Status(ctx context.Context) (Report, error) {
	_, err := e.refresh(ctx)
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.report
	r.Busy = e.busy
	return r, err
}
func (e *Engine) Report() Report {
	e.mu.Lock()
	defer e.mu.Unlock()
	r := e.report
	r.Busy = e.busy
	return r
}
func (e *Engine) save() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	return AtomicJSON(e.StatePath, e.state)
}
func (e *Engine) Cycle(ctx context.Context, manual, apply bool) error {
	e.mu.Lock()
	if e.busy {
		e.mu.Unlock()
		return errors.New("probe cycle already active")
	}
	e.busy = true
	e.mu.Unlock()
	defer func() { e.mu.Lock(); e.busy = false; e.mu.Unlock(); _ = e.save() }()
	s, err := e.refresh(ctx)
	if err != nil {
		return err
	}
	now := e.Now()
	if !s.Config.HasProbeTarget() {
		e.mu.Lock()
		e.state.LastError = "set probe_url to enable active measurement"
		e.makeReport(s, now)
		e.mu.Unlock()
		return errors.New("set probe_url to enable active measurement")
	}
	var cycleErr error
	for _, c := range s.Channels {
		if !c.Online {
			continue
		}
		e.mu.Lock()
		sc := e.state.Schedule[c.Interface]
		now = e.Now()
		sample := e.state.Samples[c.Interface]
		if !manual && !sc.Complete && !pendingCalibration(sc, now) {
			sc.Complete = true
			sc.Next = now.Add(s.Config.For(c.Interface).Interval)
			e.state.Schedule[c.Interface] = sc
			e.state.ProbeStates[c.Interface] = "calibration-exhausted"
		}
		due := manual || (!now.Before(nextAutomatic(sc, e.state.ProbeThrottle[c.Interface])) && (pendingCalibration(sc, now) || (s.Config.ScheduleMode == "hybrid" && sc.Complete)))
		if due {
			e.state.ProbeStates[c.Interface] = "measuring"
		}
		e.makeReport(s, now)
		e.mu.Unlock()
		if !due {
			continue
		}
		started := false
		// Persist the attempt before payload network I/O. Discovery, preflight
		// and quota refusals never call this hook or consume actual attempts.
		begin := func() error {
			if started {
				return nil
			}
			e.mu.Lock()
			defer e.mu.Unlock()
			// Publish the reservation only after persistence succeeds. Keep the
			// candidate maps private so refusal consumes no attempt or streak.
			candidate := e.state
			candidate.ProbeThrottle = maps.Clone(e.state.ProbeThrottle)
			candidate.Schedule = maps.Clone(e.state.Schedule)
			throttle := candidate.ProbeThrottle[c.Interface]
			throttle.LastAttempt = e.Now()
			throttle.Failures = min(throttle.Failures+1, 3)
			throttle.RetryAfter = e.Now().Add(failureBackoff(throttle.Failures))
			candidate.ProbeThrottle[c.Interface] = throttle
			current := candidate.Schedule[c.Interface]
			current.Attempts++
			current.Next = throttle.RetryAfter
			candidate.Schedule[c.Interface] = current
			if err := AtomicJSON(e.StatePath, candidate); err != nil {
				return err
			}
			e.state = candidate
			e.makeReport(s, e.Now())
			started = true
			return nil
		}
		speed, probeErr := e.Adapter.probe(ctx, s, c, e.Budgets, e.Now(), begin)
		e.mu.Lock()
		sc = e.state.Schedule[c.Interface]
		var deferred *ProbeDeferredError
		if !started {
			sc.Next = e.Now().Add(s.Config.CalibrationInterval)
			if errors.As(probeErr, &deferred) {
				sc.Next = deferred.Until
			}
			// Discovery waiting is scheduling rather than measurement failure.
			if deferred != nil {
				extendCalibration(&sc, sc.Next.Add(CalibrationWindow(s)-settleDuration(s.Config)), s.Config)
			}
		} else {
			throttle := e.state.ProbeThrottle[c.Interface]
			if probeErr == nil {
				throttle.Failures = 0
				throttle.RetryAfter = e.Now().Add(automaticAttemptInterval)
			} else {
				throttle.RetryAfter = e.Now().Add(failureBackoff(throttle.Failures))
			}
			e.state.ProbeThrottle[c.Interface] = throttle
			sc.Next = nextAutomatic(Schedule{Next: e.Now().Add(calibrationInterval(s.Config))}, throttle)
		}
		if probeErr != nil {
			e.state.ProbeErrors[c.Interface] = probeErr.Error()
			e.state.ProbeStates[c.Interface] = "failed"
			cycleErr = probeErr
		} else {
			sample = Observe(sample, speed, e.Now(), s.Config.Alpha)
			sample.Generation = sc.Epoch
			e.state.Samples[c.Interface] = sample
			e.state.ProbeErrors[c.Interface] = ""
			e.state.ProbeStates[c.Interface] = "measured"
			if sample.Count >= 2 {
				sc.Complete = true
				sc.Next = e.Now().Add(s.Config.For(c.Interface).Interval)
			}
		}
		if sc.Complete && started && probeErr == nil {
			sc.Next = e.Now().Add(s.Config.For(c.Interface).Interval)
		}
		sc.Next = nextAutomatic(sc, e.state.ProbeThrottle[c.Interface])
		e.state.Schedule[c.Interface] = sc
		e.makeReport(s, e.Now())
		e.mu.Unlock()
		if err = e.save(); err != nil {
			return err
		}
		if ctx.Err() != nil {
			return ctx.Err()
		}
	}
	fresh, err := e.refresh(ctx)
	if err != nil {
		return err
	}
	if fresh.Generation != s.Generation {
		return errors.New("cycle topology changed; samples invalidated")
	}
	if cycleErr != nil {
		e.mu.Lock()
		e.state.LastError = cycleErr.Error()
		e.mu.Unlock()
	} else {
		e.mu.Lock()
		if e.state.ApplyDeferred == "" {
			e.state.LastError = ""
		}
		e.mu.Unlock()
	}
	if apply {
		if err = e.Reconcile(ctx, fresh, manual); err != nil {
			return err
		}
		fresh, err = e.refresh(ctx)
		if err != nil {
			return err
		}
	}
	e.mu.Lock()
	e.makeReport(fresh, e.Now())
	e.mu.Unlock()
	return cycleErr
}
func (e *Engine) Reconcile(ctx context.Context, s Snapshot, explicit bool) error {
	if !explicit && (!s.Config.Enabled || s.Config.Mode != "automatic") {
		return nil
	}
	e.mu.Lock()
	if !explicit && e.state.ApplyBlocked != "" {
		e.mu.Unlock()
		return nil
	}
	now := e.Now()
	weights, err := Weights(s.Channels, e.state.Samples, now, s.Config.MaxAge)
	old := append([]int(nil), e.state.Weights...)
	last := e.state.LastApply
	changed := e.state.AppliedGeneration != s.Generation
	freshSample := false
	for _, sample := range e.state.Samples {
		if valid(sample, now, s.Config.MaxAge) && sample.At.After(last) {
			freshSample = true
		}
	}
	hold := s.Config.ScheduleMode == "on-change" && !changed && !freshSample && !explicit
	if hold && len(old) == len(weights) {
		weights = old
	}
	e.mu.Unlock()
	if err != nil {
		return err
	}
	// Detect a stock hotplug rebuild even when logical topology is unchanged.
	actual, readErr := e.Adapter.Runner.Run(ctx, []string{"iptables-save", "-t", "mangle"}, "")
	if readErr != nil {
		return readErr
	}
	// Compare with the LAST applied weights, so a small proposal does not masquerade as hotplug drift.
	expectedWeights := old
	if len(expectedWeights) != len(s.Channels) {
		expectedWeights = weights
	}
	_, expected, err := BuildRules(s, expectedWeights)
	if err != nil {
		return err
	}
	got, exists := chainLines(actual, "mwan3_policy_"+s.Config.Policy)
	if !exists || !e.Adapter.AllowedLeaf(actual, s) {
		fresh, conflictErr := e.Adapter.revalidateLeafConflict(ctx, s)
		err := e.recordApplyError(fresh, now, conflictErr)
		_ = e.save()
		return err
	}
	leafChanged := !exists || !equivalentRules(got, expected)
	_, proposedRules, buildErr := BuildRules(s, weights)
	if buildErr != nil {
		return buildErr
	}
	if equivalentRules(got, proposedRules) {
		if explicit {
			if err := e.Adapter.Recovery.Ready(); err != nil {
				return err
			}
		}
		e.mu.Lock()
		e.state.Weights = weights
		e.state.AppliedGeneration = s.Generation
		e.clearDeferredDiagnostic()
		if explicit {
			if e.state.LastError == e.state.ApplyBlocked {
				e.state.LastError = ""
			}
			e.state.ApplyBlocked = ""
		}
		e.mu.Unlock()
		return e.save()
	}
	if reflect.DeepEqual(old, weights) && !changed && !leafChanged {
		return nil
	}
	if !leafChanged && !ShouldApply(old, weights, now, last, changed, s.Config.Hysteresis, s.Config.MinimumApply) {
		return nil
	}
	if !last.IsZero() && now.Sub(last) < s.Config.MinimumApply {
		return nil
	}
	if err = e.Adapter.Apply(ctx, s, weights, explicit); err != nil {
		return e.recordApplyError(s, now, err)
	}
	e.mu.Lock()
	e.state.Weights = weights
	e.clearDeferredDiagnostic()
	if e.state.LastError == e.state.ApplyBlocked {
		e.state.LastError = ""
	}
	e.state.ApplyBlocked = ""
	e.state.LastApply = now
	e.state.AppliedGeneration = s.Generation
	s.Applied = append([]int(nil), weights...)
	e.makeReport(s, now)
	e.mu.Unlock()
	if err = e.save(); err != nil {
		return e.Adapter.RecoverFailedApply(s.Config.Policy, fmt.Errorf("post-apply state persistence failed: %w", err))
	}
	return nil
}
func (e *Engine) Tick(ctx context.Context) error {
	s, err := e.refresh(ctx)
	if err != nil {
		return err
	}
	if s.Config.Enabled && s.Config.HasProbeTarget() {
		if err = e.Cycle(ctx, false, s.Config.Mode == "automatic"); err != nil {
			return err
		}
	}
	return e.Reconcile(ctx, s, false)
}
func (e *Engine) Heartbeat(ctx context.Context) {
	ticker := time.NewTicker(5 * time.Second)
	defer ticker.Stop()
	_ = e.Adapter.Recovery.Heartbeat()
	defer os.Remove(filepath.Join(e.Adapter.Recovery.Dir, e.Adapter.Recovery.HeartbeatName()))
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if err := e.Adapter.Recovery.Heartbeat(); err != nil {
				e.mu.Lock()
				e.state.LastError = fmt.Sprintf("owner heartbeat failed: %v", err)
				e.mu.Unlock()
			}
		}
	}
}

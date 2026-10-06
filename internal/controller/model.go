package controller

import (
	"errors"
	"math"
	"sort"
	"time"
)

type Sample struct {
	Speed      float64   `json:"speed"`
	Count      int       `json:"count"`
	At         time.Time `json:"at"`
	Generation string    `json:"generation"`
}
type Channel struct {
	Member                      string     `json:"member"`
	Interface                   string     `json:"interface"`
	Device                      string     `json:"device"`
	Online                      bool       `json:"online"`
	BaselineWeight              int        `json:"baseline_weight"`
	ProposedWeight              int        `json:"proposed_weight"`
	AppliedWeight               int        `json:"applied_weight"`
	SpeedMbps                   *float64   `json:"speed_mbps"`
	AgeSeconds                  *float64   `json:"age_seconds"`
	ValidSamples                int        `json:"valid_samples"`
	ProbeState                  string     `json:"probe_state"`
	ProbeError                  string     `json:"probe_error"`
	BudgetUsedBytes             int64      `json:"budget_used_bytes"`
	BudgetLimitBytes            int64      `json:"budget_limit_bytes"`
	CalibrationDeadline         *time.Time `json:"calibration_deadline,omitempty"`
	CalibrationAttempts         int        `json:"calibration_attempts"`
	EffectiveCalibrationSeconds int64      `json:"effective_calibration_seconds"`
	Metric                      int        `json:"metric"`
	ID                          int        `json:"id"`
	SourceIP                    string     `json:"source_ip"`
	Enabled                     bool       `json:"enabled"`
	Family                      string     `json:"family"`
}

func valid(s Sample, now time.Time, ttl time.Duration) bool {
	return s.Count >= 2 && s.Speed > 0 && !math.IsInf(s.Speed, 0) && !math.IsNaN(s.Speed) && !s.At.After(now) && now.Sub(s.At) <= ttl
}
func Observe(s Sample, speed float64, now time.Time, alpha float64) Sample {
	if speed <= 0 || math.IsNaN(speed) || math.IsInf(speed, 0) {
		return s
	}
	if s.Count == 0 || s.Speed <= 0 {
		s.Speed = speed
	} else {
		s.Speed = alpha*speed + (1-alpha)*s.Speed
	}
	s.Count++
	s.At = now
	return s
}

// Unknown channels reserve their baseline share; known channels divide the remainder by speed.
func Weights(cs []Channel, samples map[string]Sample, now time.Time, ttl time.Duration) ([]int, error) {
	if len(cs) == 0 || len(cs) > 1000 {
		return nil, errors.New("policy must contain 1..1000 unique interfaces")
	}
	seen := map[string]bool{}
	metric := int(^uint(0) >> 1)
	for _, c := range cs {
		if seen[c.Interface] || c.BaselineWeight <= 0 {
			return nil, errors.New("duplicate interface or invalid baseline weight")
		}
		seen[c.Interface] = true
		if c.Online && c.EnabledOrDefault() && c.Metric < metric {
			metric = c.Metric
		}
	}
	active := []int{}
	baseline := 0.
	knownSpeed := 0.
	unknownShare := 0.
	for i, c := range cs {
		if c.Online && c.EnabledOrDefault() && c.Metric == metric {
			active = append(active, i)
			baseline += float64(c.BaselineWeight)
		}
	}
	out := make([]int, len(cs))
	if len(active) == 0 {
		return out, nil
	}
	for _, i := range active {
		if valid(samples[cs[i].Interface], now, ttl) {
			knownSpeed += samples[cs[i].Interface].Speed
		} else {
			unknownShare += float64(cs[i].BaselineWeight) / baseline
		}
	}
	shares := make([]float64, len(cs))
	for _, i := range active {
		if knownSpeed == 0 || !valid(samples[cs[i].Interface], now, ttl) {
			shares[i] = float64(cs[i].BaselineWeight) / baseline
		} else {
			shares[i] = (1 - unknownShare) * samples[cs[i].Interface].Speed / knownSpeed
		}
	}
	// Assign one unit to each active participant, then use Hamilton's method for the remaining units.
	// The positive lower bound only changes results whose unconstrained weight is below one.
	left := 1000
	locked := map[int]bool{}
	for {
		sum := 0.
		for _, i := range active {
			if !locked[i] {
				sum += shares[i]
			}
		}
		changed := false
		for _, i := range active {
			if !locked[i] && shares[i]/sum*float64(left) < 1 {
				out[i] = 1
				left--
				locked[i] = true
				changed = true
			}
		}
		if !changed {
			break
		}
	}
	sum := 0.
	for _, i := range active {
		if !locked[i] {
			sum += shares[i]
		}
	}
	type rem struct {
		i int
		r float64
	}
	rs := []rem{}
	used := 0
	for _, i := range active {
		if locked[i] {
			continue
		}
		x := shares[i] / sum * float64(left)
		out[i] = int(math.Floor(x))
		used += out[i]
		rs = append(rs, rem{i, x - float64(out[i])})
	}
	sort.SliceStable(rs, func(i, j int) bool { return rs[i].r > rs[j].r })
	for i := 0; i < left-used; i++ {
		out[rs[i].i]++
	}
	return out, nil
}

// A zero-value Channel is useful for model callers; discovery always supplies Enabled and Family.
func (c Channel) EnabledOrDefault() bool { return c.Enabled || c.Family == "" }
func ShouldApply(old, next []int, now, last time.Time, topologyChanged bool, hysteresis float64, min time.Duration) bool {
	if !last.IsZero() && now.Sub(last) < min {
		return false
	}
	if len(old) != len(next) {
		return true
	}
	if topologyChanged {
		return true
	}
	for i := range old {
		if math.Abs(float64(old[i]-next[i])) >= hysteresis*10 {
			return true
		}
	}
	return false
}

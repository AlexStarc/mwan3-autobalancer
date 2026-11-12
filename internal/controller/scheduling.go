package controller

import "time"

const (
	automaticSettle          = 5 * time.Minute
	automaticAttemptInterval = 15 * time.Minute
	maximumFailureBackoff    = time.Hour
	calibrationAttempts      = 5
)

// ProbeThrottle belongs to the logical interface, independently of sample epochs.
// An in-flight attempt is conservatively treated as failed until success is saved.
type ProbeThrottle struct {
	LastAttempt time.Time `json:"last_attempt"`
	RetryAfter  time.Time `json:"retry_after"`
	Failures    int       `json:"failures"`
}

func settleDuration(c Config) time.Duration { return max(c.Settle, automaticSettle) }
func calibrationInterval(c Config) time.Duration {
	return max(c.CalibrationInterval, automaticAttemptInterval)
}
func failureBackoff(failures int) time.Duration {
	switch {
	case failures <= 1:
		return automaticAttemptInterval
	case failures == 2:
		return 2 * automaticAttemptInterval
	default:
		return maximumFailureBackoff
	}
}
func nextAutomatic(sc Schedule, throttle ProbeThrottle) time.Time {
	next := sc.Next
	for _, guard := range []time.Time{sc.SettleUntil, throttle.RetryAfter} {
		if next.Before(guard) {
			next = guard
		}
	}
	return next
}
func pendingCalibration(sc Schedule, now time.Time) bool {
	return !sc.Complete && sc.Attempts < calibrationAttempts && !now.After(sc.CalibrationUntil)
}
func CalibrationWindow(s Snapshot) time.Duration {
	online := 0
	for _, c := range s.Channels {
		if c.Online {
			online++
		}
	}
	// Reconnects retain the failure streak: all four gaps may already be at
	// the cap. The initial wait for the retained throttle is added by refresh.
	window := settleDuration(s.Config) + time.Duration(2*calibrationAttempts*online)*s.Config.Timeout + time.Minute
	window += time.Duration(calibrationAttempts-1) * max(calibrationInterval(s.Config), maximumFailureBackoff)
	return max(window, s.Config.CalibrationMinimumWindow)
}
func extendCalibration(sc *Schedule, until time.Time, c Config) {
	if sc.Complete || !sc.CalibrationUntil.Before(until) {
		return
	}
	sc.CalibrationUntil = until
	sc.WindowSeconds = int64(until.Sub(sc.SettleUntil.Add(-settleDuration(c))) / time.Second)
}

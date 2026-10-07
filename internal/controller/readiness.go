package controller

import "errors"

// WatchdogUnavailableError is a read-only refusal: missing readiness, an expired
// record with verified process identity, or a recorded process that has vanished.
type WatchdogUnavailableError struct {
	Cause error
}

func (e *WatchdogUnavailableError) Error() string { return e.Cause.Error() }
func (e *WatchdogUnavailableError) Unwrap() error { return e.Cause }

// Identify Arm's readiness check separately from its lease and heartbeat writes.
type armReadinessError struct {
	Cause error
}

func (e *armReadinessError) Error() string { return e.Cause.Error() }
func (e *armReadinessError) Unwrap() error { return e.Cause }

func deferUnavailableWatchdog(err error) error {
	var unavailable *WatchdogUnavailableError
	if errors.As(err, &unavailable) {
		return &DeferredApplyError{Reason: err.Error(), Cause: err}
	}
	return err
}

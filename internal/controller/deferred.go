package controller

import (
	"errors"
	"time"
)

// DeferredApplyError is a verified refusal before arming the recovery lease or
// writing rules. It may be retried only by a later cycle with a fresh snapshot.
type DeferredApplyError struct {
	Reason string
}

func (e *DeferredApplyError) Error() string { return e.Reason }

func legacyDeferredLatch(reason string) bool {
	switch reason {
	case "generation changed during transaction validation", "stale generation or incompatible current configuration":
		// These exact 0.1.0-r2 errors were emitted exclusively before lease arm.
		return true
	default:
		return false
	}
}

// Called with e.mu held. Other errors may have replaced the transient diagnostic.
func (e *Engine) clearDeferredDiagnostic() {
	if e.state.ApplyDeferred != "" && e.state.LastError == e.state.ApplyDeferred {
		e.state.LastError = ""
	}
	e.state.ApplyDeferred = ""
}

func (e *Engine) recordApplyError(s Snapshot, now time.Time, err error) error {
	e.mu.Lock()
	defer e.mu.Unlock()
	var deferred *DeferredApplyError
	if errors.As(err, &deferred) {
		e.state.ApplyDeferred = err.Error()
	} else {
		e.state.ApplyDeferred = ""
		e.state.ApplyBlocked = err.Error()
	}
	e.state.LastError = err.Error()
	e.makeReport(s, now)
	return err
}

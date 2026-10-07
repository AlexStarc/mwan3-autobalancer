package controller

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

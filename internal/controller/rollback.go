package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"
)

func (a *Adapter) Pause(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	if _, err := a.Runner.Run(ctx, []string{"uci", "set", "mwan3_autobalancer.main.mode=observe"}, ""); err != nil {
		return err
	}
	_, err := a.Runner.Run(ctx, []string{"uci", "commit", "mwan3_autobalancer"}, "")
	return err
}
func (a *Adapter) RecoverFailedApply(policy string, cause error) error {
	// Recovery must outlive cancellation of the apply operation; each command remains bounded.
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	requestErr := a.Recovery.RequestRestore(cause.Error())
	pauseErr := a.Pause(ctx)
	_, restoreErr := a.Runner.Run(ctx, []string{"/usr/libexec/mwan3-autobalancer/restore", policy}, "")
	if restoreErr != nil {
		a.Recovery.StopHeartbeat()
	}
	return fmt.Errorf("%w; automatic pause: %v; independent restore: %v; watchdog restore request: %v", cause, pauseErr, restoreErr, requestErr)
}

// Rollback pauses only our own automatic mode before calling the independent stock restore helper.
func (e *Engine) Rollback(ctx context.Context) (Report, error) {
	u, err := e.Adapter.uci(ctx, "mwan3_autobalancer")
	if err != nil {
		return e.Report(), err
	}
	policy := u.Values["main"].Text("policy")
	if policy == "" {
		policy = "balanced"
	}
	if !identifier(policy) || len(policy) > 15 {
		return e.Report(), errors.New("invalid selected policy")
	}
	selectedPolicy := policy
	var lease Lease
	if leaseErr := ReadJSON(filepath.Join(e.Adapter.Recovery.Dir, "lease.json"), &lease); leaseErr == nil {
		if !identifier(lease.Policy) || len(lease.Policy) > 15 {
			return e.Report(), errors.New("invalid leased policy")
		}
		policy = lease.Policy
	} else if !errors.Is(leaseErr, os.ErrNotExist) {
		return e.Report(), leaseErr
	}
	if err = e.Adapter.Pause(ctx); err != nil {
		return e.Report(), err
	}
	if policy != selectedPolicy {
		cause := fmt.Errorf("leased policy %s differs from selected policy %s; select the leased policy before explicit restore verification", policy, selectedPolicy)
		e.mu.Lock()
		e.state.ApplyBlocked = cause.Error()
		e.state.LastError = cause.Error()
		e.mu.Unlock()
		_ = e.save()
		_ = e.Adapter.RequestLeasedRestore(context.WithoutCancel(ctx), policy, cause.Error())
		report, _ := e.Status(ctx)
		report.Mode = "observe"
		report.LastError = cause.Error()
		return report, cause
	}
	_, restoreErr := e.Adapter.Runner.Run(ctx, []string{"/usr/libexec/mwan3-autobalancer/restore", policy}, "")
	if restoreErr == nil {
		restoreErr = e.verifyExplicitRestore(ctx, policy)
	}
	if restoreErr != nil {
		e.mu.Lock()
		e.state.ApplyBlocked = restoreErr.Error()
		e.state.LastError = restoreErr.Error()
		e.mu.Unlock()
		_ = e.save()
		_ = e.Adapter.RequestLeasedRestore(context.WithoutCancel(ctx), policy, restoreErr.Error())
	}
	report, statusErr := e.Status(ctx)
	report.Mode = "observe"
	if restoreErr != nil {
		report.LastError = restoreErr.Error()
		return report, restoreErr
	}
	return report, statusErr
}
func (e *Engine) verifyExplicitRestore(ctx context.Context, policy string) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	ctx, unlock, err := commandFileLock(ctx, e.Adapter.LockPath)
	if err != nil {
		return err
	}
	defer unlock()
	s, err := e.Adapter.Discover(ctx)
	if err != nil {
		return err
	}
	if s.Config.Policy != policy {
		return errors.New("restored leased policy differs from selected policy; restoration cannot be verified and latch remains blocked")
	}
	if s.Config.Mode != "observe" {
		return errors.New("explicit restore requires current mode observe")
	}
	save, err := e.Adapter.Runner.Run(ctx, []string{"iptables-save", "-t", "mangle"}, "")
	if err != nil {
		return err
	}
	if !NativeBaseline(save, s) {
		return errors.New("restored selected policy is not the current validated stock baseline")
	}
	path := filepath.Join(e.Adapter.Recovery.Dir, "lease.json")
	var lease Lease
	if err = ReadJSON(path, &lease); err == nil {
		if lease.Policy != policy || policy != s.Config.Policy {
			return errors.New("remaining lease belongs to an unverified policy")
		}
		if err = os.Remove(path); err != nil {
			return err
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	e.mu.Lock()
	e.state.ApplyBlocked = ""
	e.state.ApplyDeferred = ""
	e.state.LastError = ""
	e.state.Weights = nil
	e.state.AppliedGeneration = ""
	e.state.LastApply = time.Time{}
	e.mu.Unlock()
	if err = e.save(); err != nil {
		return err
	}
	e.Adapter.Recovery.HeartbeatStopped.Store(false)
	return nil
}

func (a *Adapter) RequestLeasedRestore(ctx context.Context, policy, reason string) error {
	ctx, cancel := context.WithTimeout(ctx, 10*time.Second)
	defer cancel()
	unlock, err := fileLock(ctx, a.LockPath)
	if err != nil {
		return err
	}
	defer unlock()
	var lease Lease
	path := filepath.Join(a.Recovery.Dir, "lease.json")
	if err = ReadJSON(path, &lease); err != nil {
		return err
	}
	if lease.Policy != policy || lease.PID <= 1 || len(lease.Session) != 32 {
		return errors.New("cannot request restoration for an invalid/different lease")
	}
	lease.RestoreRequested = true
	lease.RestoreReason = reason
	return AtomicJSON(path, lease)
}

package controller

import (
	"context"
	"errors"
	"os"
	"path/filepath"
)

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
	var lease Lease
	if leaseErr := ReadJSON(filepath.Join(e.Adapter.Recovery.Dir, "lease.json"), &lease); leaseErr == nil {
		if !identifier(lease.Policy) || len(lease.Policy) > 15 {
			return e.Report(), errors.New("invalid leased policy")
		}
		policy = lease.Policy
	} else if !errors.Is(leaseErr, os.ErrNotExist) {
		return e.Report(), leaseErr
	}
	if _, err = e.Adapter.Runner.Run(ctx, []string{"uci", "set", "mwan3_autobalancer.main.mode=observe"}, ""); err != nil {
		return e.Report(), err
	}
	if _, err = e.Adapter.Runner.Run(ctx, []string{"uci", "commit", "mwan3_autobalancer"}, ""); err != nil {
		return e.Report(), err
	}
	_, restoreErr := e.Adapter.Runner.Run(ctx, []string{"/usr/libexec/mwan3-autobalancer/restore", policy}, "")
	report, statusErr := e.Status(ctx)
	report.Mode = "observe"
	if restoreErr != nil {
		report.LastError = restoreErr.Error()
		return report, restoreErr
	}
	return report, statusErr
}

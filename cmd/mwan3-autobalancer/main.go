package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"github.com/AlexStarc/mwan3-autobalancer/internal/controller"
)

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if err := run(ctx, os.Args[1:]); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
func run(ctx context.Context, args []string) error {
	if len(args) == 0 {
		return fmt.Errorf("usage: mwan3-autobalancer status|probe|once --dry-run|once --apply|daemon|rollback")
	}
	dir := "/var/run/mwan3-autobalancer"
	runner := controller.ExecRunner{}
	recovery := controller.NewRecovery(dir)
	adapter := &controller.Adapter{Runner: runner, ReadFile: os.ReadFile, LockPath: "/var/lock/procd_mwan3.lock", Recovery: recovery}
	engine := controller.NewEngine(adapter, controller.Budgets{Path: "/etc/mwan3-autobalancer/budgets"}, filepath.Join(dir, "state.json"))
	socket := filepath.Join(dir, "control.sock")
	emit := func(v any) error { return json.NewEncoder(os.Stdout).Encode(v) }
	switch args[0] {
	case "status":
		if len(args) != 1 {
			return fmt.Errorf("status takes no arguments")
		}
		var report controller.Report
		if err := controller.Control(ctx, socket, "status", &report); err == nil {
			return emit(report)
		}
		report, err := engine.Status(ctx)
		if err != nil {
			report.LastError = err.Error()
		}
		if outErr := emit(report); outErr != nil {
			return outErr
		}
		return err
	case "probe":
		if len(args) != 1 {
			return fmt.Errorf("probe takes no arguments")
		}
		var reply controller.ControlReply
		if err := controller.Control(ctx, socket, "probe", &reply); err != nil {
			return fmt.Errorf("start daemon to queue an observation probe: %w", err)
		}
		if err := emit(reply); err != nil {
			return err
		}
		if reply.Error != "" {
			return fmt.Errorf("%s", reply.Error)
		}
		return nil
	case "once":
		if len(args) != 2 || (args[1] != "--dry-run" && args[1] != "--apply") {
			return fmt.Errorf("once requires exactly --dry-run or --apply")
		}
		err := engine.Once(ctx, args[1] == "--apply")
		if outErr := emit(engine.Report()); outErr != nil {
			return outErr
		}
		return err
	case "daemon":
		if len(args) != 1 {
			return fmt.Errorf("daemon takes no arguments")
		}
		return engine.Daemon(ctx)
	case "rollback":
		if len(args) != 1 {
			return fmt.Errorf("rollback takes no arguments")
		}
		var activeReport controller.Report
		if err := controller.Control(ctx, socket, "rollback", &activeReport); err == nil && activeReport.Policy != "" && activeReport.Mode == "observe" {
			if outErr := emit(activeReport); outErr != nil {
				return outErr
			}
			if activeReport.LastError != "" {
				return fmt.Errorf("%s", activeReport.LastError)
			}
			return nil
		}
		report, err := engine.Rollback(ctx)
		if outErr := emit(report); outErr != nil {
			return outErr
		}
		return err
	default:
		return fmt.Errorf("unknown command %q", args[0])
	}
}

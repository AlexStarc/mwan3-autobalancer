package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// Run the actual runner in a separate Go owner so SIGKILL also closes its pipes and lock.
func TestRunnerOwnerDeathHelper(t *testing.T) {
	dir := os.Getenv("AUTOBALANCER_OWNER_DEATH_FIXTURE")
	if dir == "" {
		return
	}
	ctx := context.Background()
	if os.Getenv("AUTOBALANCER_OWNER_DEATH_LOCK") == "1" {
		lockedCtx, unlock, err := commandFileLock(ctx, filepath.Join(dir, "stock.lock"))
		if err != nil {
			t.Fatal(err)
		}
		defer unlock()
		ctx = lockedCtx
	}
	shell := os.Getenv("AUTOBALANCER_OWNER_DEATH_SHELL")
	if shell == "" {
		shell = "/bin/sh"
	}
	_, err := runCommand(ctx, shell, []string{os.Args[0], "-test.run=^TestRunnerActiveCommandHelper$"}, "")
	if err != nil {
		t.Fatal(err)
	}
}

func TestRunnerActiveCommandHelper(t *testing.T) {
	dir := os.Getenv("AUTOBALANCER_OWNER_DEATH_FIXTURE")
	if dir == "" {
		return
	}
	child := exec.Command("sleep", "30")
	if err := child.Start(); err != nil {
		t.Fatal(err)
	}
	defer child.Process.Kill()
	group, err := syscall.Getpgid(0)
	if err != nil {
		t.Fatal(err)
	}
	if os.Getenv("AUTOBALANCER_OWNER_DEATH_LOCK") == "1" {
		var stat syscall.Stat_t
		if err := syscall.Fstat(5, &stat); err != nil {
			t.Fatal("stock lock descriptor was not inherited", err)
		}
		var expected syscall.Stat_t
		if err := syscall.Stat(filepath.Join(dir, "stock.lock"), &expected); err != nil || stat.Ino != expected.Ino || stat.Dev != expected.Dev {
			t.Fatal("inherited descriptor does not refer to the stock lock", stat.Ino, expected.Ino, err)
		}
	}
	for name, pid := range map[string]int{"command.pid": os.Getpid(), "child.pid": child.Process.Pid, "group.pid": group} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(strconv.Itoa(pid)), 0600); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "ready"), nil, 0600); err != nil {
		t.Fatal(err)
	}
	time.Sleep(time.Second)
	if err := os.WriteFile(filepath.Join(dir, "policy"), []byte("late-apply"), 0600); err != nil {
		t.Fatal(err)
	}
	_ = child.Wait()
}

func TestRunnerOwnerDeathStopsActiveCommandsBeforeRecovery(t *testing.T) {
	for _, shell := range runnerTestShells() {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			for _, locked := range []bool{false, true} {
				t.Run(strconv.FormatBool(locked), func(t *testing.T) {
					dir := t.TempDir()
					owner := exec.Command(os.Args[0], "-test.run=^TestRunnerOwnerDeathHelper$")
					owner.Env = append(os.Environ(), "AUTOBALANCER_OWNER_DEATH_FIXTURE="+dir, "AUTOBALANCER_OWNER_DEATH_SHELL="+shell)
					if locked {
						owner.Env = append(owner.Env, "AUTOBALANCER_OWNER_DEATH_LOCK=1")
					}
					if err := owner.Start(); err != nil {
						t.Fatal(err)
					}
					defer owner.Process.Kill()
					deadline := time.Now().Add(3 * time.Second)
					for {
						if _, err := os.Stat(filepath.Join(dir, "ready")); err == nil {
							break
						}
						if time.Now().After(deadline) {
							t.Fatal("owner command never started")
						}
						time.Sleep(10 * time.Millisecond)
					}
					groupData, err := os.ReadFile(filepath.Join(dir, "group.pid"))
					if err != nil {
						t.Fatal(err)
					}
					group, err := strconv.Atoi(strings.TrimSpace(string(groupData)))
					if err != nil || group <= 1 {
						t.Fatal(string(groupData), err)
					}
					defer syscall.Kill(-group, syscall.SIGKILL)
					if locked {
						// Keep a member parented by this test outside the group. Otherwise POSIX
						// orphan-group SIGHUP/SIGCONT would resume the stopped monitor on owner death.
						guard := exec.Command("sleep", "30")
						guard.SysProcAttr = &syscall.SysProcAttr{Setpgid: true, Pgid: group}
						if err := guard.Start(); err != nil {
							t.Fatal(err)
						}
						defer func() { _ = guard.Process.Kill(); _ = guard.Wait() }()
						probeCtx, stop := context.WithTimeout(context.Background(), 100*time.Millisecond)
						prematureUnlock, probeErr := fileLock(probeCtx, filepath.Join(dir, "stock.lock"))
						stop()
						if probeErr == nil {
							prematureUnlock()
							t.Fatal("stock lock not held even before owner death")
						}
						// Delay the EOF monitor deliberately: recovery must remain locked out even
						// when owner death releases Go's descriptor before group cleanup executes.
						if err = syscall.Kill(-group, syscall.SIGSTOP); err != nil {
							t.Fatal(err)
						}
						time.Sleep(50 * time.Millisecond)
					}
					if err := owner.Process.Kill(); err != nil {
						t.Fatal(err)
					}
					_ = owner.Wait()
					if locked {
						blockedCtx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
						unlock, err := fileLock(blockedCtx, filepath.Join(dir, "stock.lock"))
						cancel()
						if err == nil {
							unlock()
							t.Fatal("recovery acquired stock lock before old command group exited", "owner", owner.Process.Pid, "group", group, "group exists", syscall.Kill(-group, 0))
						}
						if !errors.Is(err, context.DeadlineExceeded) {
							t.Fatal(err)
						}
						if err = syscall.Kill(-group, syscall.SIGCONT); err != nil {
							t.Fatal(err)
						}
					}
					// Simulate the independent stock helper taking the same flock and restoring.
					ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
					defer cancel()
					unlock, err := fileLock(ctx, filepath.Join(dir, "stock.lock"))
					if err != nil {
						t.Fatal("recovery could not acquire released stock lock", err)
					}
					defer unlock()
					// Restore immediately: polling must never erase a surviving command's late write.
					if err := os.WriteFile(filepath.Join(dir, "policy"), []byte("stock-restored"), 0600); err != nil {
						t.Fatal(err)
					}
					restoredAt := time.Now()
					cleanupDeadline := restoredAt.Add(500 * time.Millisecond)
					for _, name := range []string{"command.pid", "child.pid"} {
						data, err := os.ReadFile(filepath.Join(dir, name))
						if err != nil {
							t.Fatal(err)
						}
						pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
						if err != nil || pid <= 1 {
							t.Fatal(string(data), err)
						}
						defer syscall.Kill(pid, syscall.SIGKILL)
						stat := runnerProcessState(t, pid)
						// An unlocked probe has no shared stock lock: its EOF monitor needs CPU
						// after owner death. Applying commands must already be dead when recovery
						// acquires their inherited lock, so that variant keeps the immediate check.
						if !locked {
							for stat != "" && !strings.HasPrefix(stat, "Z") && time.Now().Before(cleanupDeadline) {
								time.Sleep(10 * time.Millisecond)
								stat = runnerProcessState(t, pid)
							}
							t.Logf("unlocked %s cleanup observed after %s", name, time.Since(restoredAt))
						}
						if stat != "" && !strings.HasPrefix(stat, "Z") {
							t.Errorf("active command survived owner death/recovery lock: %s %d %s", name, pid, stat)
						}
					}
					time.Sleep(time.Until(restoredAt.Add(1200 * time.Millisecond)))
					data, err := os.ReadFile(filepath.Join(dir, "policy"))
					if err != nil || string(data) != "stock-restored" {
						t.Fatal("late apply overwrote independent restoration", string(data), err)
					}
				})
			}
		})
	}
}

func runnerTestShells() []string {
	shells := []string{"/bin/sh"}
	if _, err := os.Stat("/bin/dash"); err == nil {
		shells = append(shells, "/bin/dash")
	}
	return shells
}

func runnerProcessState(t *testing.T, pid int) string {
	t.Helper()
	if runtime.GOOS == "linux" {
		// BusyBox ps lacks -p and the stat= format. /proc also distinguishes dead
		// zombies from running descendants without depending on a ps implementation.
		data, err := os.ReadFile(fmt.Sprintf("/proc/%d/stat", pid))
		if runnerProcProcessGone(err) {
			return ""
		}
		if err != nil {
			t.Fatal(err)
		}
		end := strings.LastIndex(string(data), ") ")
		if end < 0 || end+2 >= len(data) {
			t.Fatal("invalid process stat", string(data))
		}
		return string(data[end+2])
	}
	state, err := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
	if err != nil {
		if syscall.Kill(pid, 0) == syscall.ESRCH {
			return ""
		}
		if errors.Is(err, os.ErrPermission) {
			// Restricted macOS environments can deny ps. Conservatively count an
			// existing PID as alive rather than claiming cleanup without evidence.
			return "unknown-alive"
		}
		t.Fatal("could not inspect owned process", pid, err)
	}
	return strings.TrimSpace(string(state))
}

func runnerProcProcessGone(err error) bool {
	// procfs can open stat successfully and then return ESRCH if the task exits
	// before read. Both errors prove disappearance; permission/I/O errors do not.
	return errors.Is(err, os.ErrNotExist) || errors.Is(err, syscall.ESRCH)
}

func TestRunnerProcProcessGone(t *testing.T) {
	for _, tc := range []struct {
		name string
		err  error
		gone bool
	}{
		{"missing-before-open", &os.PathError{Op: "open", Path: "/proc/123/stat", Err: syscall.ENOENT}, true},
		{"exited-during-read", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.ESRCH}, true},
		{"permission-denied", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.EACCES}, false},
		{"io-error", &os.PathError{Op: "read", Path: "/proc/123/stat", Err: syscall.EIO}, false},
		{"successful-read", nil, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if gone := runnerProcProcessGone(tc.err); gone != tc.gone {
				t.Fatalf("process gone = %v, want %v for %v", gone, tc.gone, tc.err)
			}
		})
	}
}

func stockOfflineFixture(s Snapshot) string {
	chain := "mwan3_policy_" + s.Config.Policy
	result := "*mangle\n:" + chain + " - [0:0]\n"
	// Stock inserts each offline out-device exception at the head in declared member order.
	for i := len(s.Channels) - 1; i >= 0; i-- {
		c := s.Channels[i]
		if c.Device != "" {
			result += fmt.Sprintf("-A %s -o %s -m mark --mark 0x0/0x%x -m comment --comment \"out %s %s\" -j MARK --set-xmark 0x%x/0x%x\n", chain, c.Device, s.Mask, c.Interface, c.Device, s.Mask, s.Mask)
		}
	}
	id := s.Mask >> s.Shift
	switch s.LastResort {
	case "blackhole":
		id -= 2
	case "unreachable":
		id--
	}
	result += fmt.Sprintf("-A %s -m mark --mark 0x0/0x%x -m comment --comment \"%s\" -j MARK --set-xmark 0x%x/0x%x\nCOMMIT\n", chain, s.Mask, s.LastResort, id<<s.Shift, s.Mask)
	return result
}
func TestAllOfflineNativeFallbackCommentsAndExceptions(t *testing.T) {
	for _, last := range []string{"default", "unreachable", "blackhole"} {
		t.Run(last, func(t *testing.T) {
			ctx := context.Background()
			x := makeFixture(t)
			x.uci.Values["balanced"]["last_resort"] = last
			read := x.adapter.ReadFile
			x.adapter.ReadFile = func(p string) ([]byte, error) {
				if strings.Contains(p, "iface_state/") {
					return []byte("offline"), nil
				}
				return read(p)
			}
			s, err := x.adapter.Discover(ctx)
			if err != nil {
				t.Fatal(err)
			}
			native := stockOfflineFixture(s)
			if !NativeBaseline(native, s) {
				t.Fatal("stock all-offline fallback rejected", native)
			}
			generated, rules, err := BuildRules(s, []int{0, 0})
			if err != nil || !NativeBaseline(generated, s) { // Restore input has no chain declaration; inspect rules with the existing declaration.
				inspection := fmt.Sprintf(":mwan3_policy_%s - [0:0]\n%s\n", s.Config.Policy, strings.Join(rules, "\n"))
				if err != nil || !NativeBaseline(inspection, s) {
					t.Fatal(generated, err)
				}
			}
			nativeRules, _ := chainLines(native, "mwan3_policy_balanced")
			if !equivalentRules(nativeRules, rules) {
				t.Fatal("all-offline controller rules dropped/changed stock exceptions", nativeRules, rules)
			}
			wrong := strings.Replace(native, `--comment "`+last+`"`, `--comment "foreign"`, 1)
			if NativeBaseline(wrong, s) {
				t.Fatal("foreign terminal comment accepted")
			}
			wrong = strings.Replace(native, "--set-xmark 0x3f00/0x3f00", "--set-xmark 0x300/0x3f00", 1)
			if NativeBaseline(wrong, s) {
				t.Fatal("wrong native out-device mark accepted")
			}
			x.save = native
			e := makeEngine(t, x)
			e.state.ApplyBlocked = "previous error"
			e.Adapter.Recovery.HeartbeatStopped.Store(true)
			base := x.runner.fn
			x.runner.fn = func(a []string, in string) (string, error) {
				if a[0] == "/usr/libexec/mwan3-autobalancer/restore" {
					x.save = native
					_ = os.Remove(filepath.Join(x.dir, "lease.json"))
					return "", nil
				}
				return base(a, in)
			}
			report, err := e.Rollback(ctx)
			if err != nil || e.state.ApplyBlocked != "" || e.Adapter.Recovery.HeartbeatStopped.Load() || report.Mode != "observe" {
				t.Fatal("stock all-offline explicit restore did not verify", report, err)
			}
		})
	}
}
func TestRollbackPolicyMismatchKeepsLeasedPolicyProtected(t *testing.T) {
	x := makeFixture(t)
	x.ready(t)
	e := makeEngine(t, x)
	if err := x.adapter.Recovery.Arm("old"); err != nil {
		t.Fatal(err)
	}
	e.state.ApplyBlocked = "unrestored old policy"
	e.Adapter.Recovery.HeartbeatStopped.Store(true)
	x.save += "\n:mwan3_policy_old - [0:0]\n-A mwan3_policy_old -j DROP\n"
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] == "/usr/libexec/mwan3-autobalancer/restore" {
			_ = os.Remove(filepath.Join(x.dir, "lease.json"))
			return "", nil
		}
		return base(a, in)
	}
	report, err := e.Rollback(context.Background())
	if err == nil || e.state.ApplyBlocked == "" || !e.Adapter.Recovery.HeartbeatStopped.Load() || !strings.Contains(x.save, "-A mwan3_policy_old -j DROP") {
		t.Fatal("unverified leased policy unblocked", report, err)
	}
	var lease Lease
	if err = ReadJSON(filepath.Join(x.dir, "lease.json"), &lease); err != nil || lease.Policy != "old" || !lease.RestoreRequested {
		t.Fatal("old lease lost protection", lease, err)
	}
	if x.runner.count("/usr/libexec/mwan3-autobalancer/restore") != 0 {
		t.Fatal("mismatched policy passed to unverifiable helper")
	}
}
func TestRunnerKillsBackgroundChildAfterEarlyWrapperExit(t *testing.T) {
	for _, closedPipes := range []bool{false, true} {
		t.Run(strconv.FormatBool(closedPipes), func(t *testing.T) {
			pidFile := filepath.Join(t.TempDir(), "child.pid")
			script := `sleep 30 & echo $! > "$1"; exit 0`
			if closedPipes {
				script = `sleep 30 >/dev/null 2>&1 & echo $! > "$1"; exit 0`
			}
			start := time.Now()
			out, err := (ExecRunner{}).Run(context.Background(), []string{"sh", "-c", script, "fixture", pidFile}, "")
			if err != nil {
				t.Fatal(out, err)
			}
			if time.Since(start) > 2*time.Second {
				t.Fatal("cleanup depended on waiting for inherited pipes")
			}
			data, err := os.ReadFile(pidFile)
			if err != nil {
				t.Fatal(err)
			}
			pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
			if err != nil || pid <= 1 {
				t.Fatal(string(data), err)
			}
			deadline := time.Now().Add(time.Second)
			for {
				stat := runnerProcessState(t, pid)
				if stat == "" || strings.HasPrefix(stat, "Z") {
					break
				}
				if time.Now().After(deadline) {
					t.Fatal("owned process group descendant survived", pid, stat)
				}
				time.Sleep(10 * time.Millisecond)
			}
		})
	}
}
func TestRunnerPreservesExitStatusStdinAndLiteralArguments(t *testing.T) {
	for _, shell := range runnerTestShells() {
		t.Run(filepath.Base(shell), func(t *testing.T) {
			literal := "$(echo BAD); `echo BAD`"
			out, err := runCommand(context.Background(), shell, []string{"sh", "-c", `printf '%s' "$1"; cat; printf 'diagnostic' >&2; exit 28`, "fixture", literal}, "|stdin")
			var commandErr *CommandError
			if !errors.As(err, &commandErr) || commandErr.Code != 28 || commandErr.Stderr != "diagnostic" || out != literal+"|stdin" {
				t.Fatal(out, err)
			}
			for _, code := range []int{1, 127, 143, 255} {
				_, err = runCommand(context.Background(), shell, []string{"sh", "-c", `exit "$1"`, "fixture", strconv.Itoa(code)}, "")
				if !errors.As(err, &commandErr) || commandErr.Code != code {
					t.Fatal(code, err)
				}
			}
		})
	}
}

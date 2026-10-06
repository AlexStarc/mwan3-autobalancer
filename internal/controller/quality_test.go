package controller

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

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
				state, _ := exec.Command("ps", "-o", "stat=", "-p", strconv.Itoa(pid)).Output()
				stat := strings.TrimSpace(string(state))
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
	literal := "$(echo BAD); `echo BAD`"
	out, err := (ExecRunner{}).Run(context.Background(), []string{"sh", "-c", `printf '%s' "$1"; cat; printf 'diagnostic' >&2; exit 28`, "fixture", literal}, "|stdin")
	var commandErr *CommandError
	if !errors.As(err, &commandErr) || commandErr.Code != 28 || commandErr.Stderr != "diagnostic" || out != literal+"|stdin" {
		t.Fatal(out, err)
	}
	for _, code := range []int{1, 127, 143, 255} {
		_, err = (ExecRunner{}).Run(context.Background(), []string{"sh", "-c", `exit "$1"`, "fixture", strconv.Itoa(code)}, "")
		if !errors.As(err, &commandErr) || commandErr.Code != code {
			t.Fatal(code, err)
		}
	}
}

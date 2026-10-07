package controller

import (
	"bufio"
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"
	"time"
)

type Runner interface {
	Run(context.Context, []string, string) (string, error)
}
type ExecRunner struct{}
type CommandError struct {
	Command string
	Code    int
	Cause   error
	Stderr  string
}

func (e *CommandError) Error() string {
	return fmt.Sprintf("%s: exit %d: %v: %s", e.Command, e.Code, e.Cause, e.Stderr)
}
func (e *CommandError) Unwrap() error { return e.Cause }

type cappedBuffer struct {
	b        bytes.Buffer
	max      int
	mu       sync.Mutex
	overflow bool
	cancel   context.CancelFunc
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	n := len(p)
	remaining := b.max - b.b.Len()
	if len(p) > remaining {
		b.overflow = true
		if b.cancel != nil {
			b.cancel()
		}
		p = p[:remaining]
	}
	_, _ = b.b.Write(p)
	return n, nil
}

// The monitor observes owner-pipe EOF concurrently with the command, including Go SIGKILL.
// The supervisor keeps the group leader alive until Go receives the command status and kills
// the group before Wait reaps that leader; neither cleanup path can target a reused leader PID.
// Commands and descendants inherit the group; deliberately detached groups are outside this ownership.
const commandSupervisor = `set +m
exec 6<&0
leader=$$
(
  IFS= read -r release <&4
  kill -KILL "-$leader"
) </dev/null >/dev/null 2>&1 3>&- 6<&- &
monitor=$!
"$@" <&6 3>&- 4>&- 6<&- &
exec 6<&-
child=$!
wait "$child"
result=$?
printf '%s\n' "$result" >&3
wait "$monitor"
exit "$result"
`

func (ExecRunner) Run(ctx context.Context, args []string, input string) (string, error) {
	return runCommand(ctx, "/bin/sh", args, input)
}

// Shell selection is internal so tests can exercise dash as well as the host's /bin/sh.
// Production always uses /bin/sh; no configuration value selects or supplies shell text.
func runCommand(ctx context.Context, shell string, args []string, input string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	statusR, statusW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer statusR.Close()
	defer statusW.Close()
	holdR, holdW, err := os.Pipe()
	if err != nil {
		return "", err
	}
	defer holdR.Close()
	defer holdW.Close()
	launchArgs := append([]string{"-c", commandSupervisor, "mwan3-autobalancer-runner"}, args...)
	cmd := exec.CommandContext(ctx, shell, launchArgs...)
	cmd.ExtraFiles = []*os.File{statusW, holdR}
	if lock, _ := ctx.Value(commandLockKey{}).(*os.File); lock != nil {
		// Inherit the same flock open-file-description, not an independently opened file.
		// Owner death cannot release recovery's lock while an old command can still mutate.
		cmd.ExtraFiles = append(cmd.ExtraFiles, lock)
	}
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	cmd.Cancel = func() error {
		if cmd.Process != nil {
			return syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
		}
		return nil
	}
	cmd.WaitDelay = 2 * time.Second
	cmd.Stdin = strings.NewReader(input)
	out := &cappedBuffer{max: 256 * 1024, cancel: cancel}
	errout := &cappedBuffer{max: 16 * 1024, cancel: cancel}
	cmd.Stdout = out
	cmd.Stderr = errout
	if err = cmd.Start(); err != nil {
		return "", &CommandError{args[0], -1, err, ""}
	}
	_ = statusW.Close()
	_ = holdR.Close()
	status, statusErr := bufio.NewReader(io.LimitReader(statusR, 8)).ReadString('\n')
	// The supervisor is still waiting (or an unreaped zombie after cancellation) at this point.
	// Cleanup is unconditional and occurs before Wait, including early-exiting wrappers with open or closed pipes.
	killErr := syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	waitErr := cmd.Wait()
	if out.overflow || errout.overflow {
		return out.b.String(), errors.New("command output exceeded limit")
	}
	code := -1
	if statusErr == nil {
		parsed, parseErr := strconv.Atoi(strings.TrimSpace(status))
		if parseErr == nil && parsed >= 0 && parsed <= 255 {
			code = parsed
		} else {
			statusErr = errors.New("invalid command status")
		}
	}
	cause := statusErr
	if ctx.Err() != nil {
		cause = ctx.Err()
	} else if killErr != nil && killErr != syscall.ESRCH {
		cause = fmt.Errorf("owned process group cleanup: %w", killErr)
	} else if errors.Is(waitErr, exec.ErrWaitDelay) {
		cause = waitErr
	} else if code != 0 && cause == nil {
		cause = fmt.Errorf("command exited with status %d", code)
	}
	if cause != nil {
		return out.b.String(), &CommandError{args[0], code, cause, strings.TrimSpace(errout.b.String())}
	}
	return out.b.String(), nil
}
func AtomicJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".atomic-")
	if err != nil {
		return err
	}
	name := f.Name()
	defer os.Remove(name)
	if err = f.Chmod(0600); err == nil {
		_, err = f.Write(append(data, '\n'))
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err != nil {
		return err
	}
	if err = os.Rename(name, path); err != nil {
		return err
	}
	d, err := os.Open(filepath.Dir(path))
	if err != nil {
		return err
	}
	defer d.Close()
	return d.Sync()
}
func ReadJSON(path string, v any) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	d := json.NewDecoder(io.LimitReader(f, 1024*1024))
	if err = d.Decode(v); err != nil {
		return err
	}
	var extra any
	if err = d.Decode(&extra); err != io.EOF {
		return errors.New("extra JSON data")
	}
	return nil
}

type commandLockKey struct{}

// Only commands inside this lock scope inherit its descriptor. Independent restoration
// uses a fresh context after unlock, and must never inherit the old stock lock.
func commandFileLock(ctx context.Context, path string) (context.Context, func(), error) {
	f, unlock, err := acquireFileLock(ctx, path)
	if err != nil {
		return ctx, nil, err
	}
	return context.WithValue(ctx, commandLockKey{}, f), unlock, nil
}

func withoutCommandLock(ctx context.Context) context.Context {
	return context.WithValue(ctx, commandLockKey{}, (*os.File)(nil))
}

func fileLock(ctx context.Context, path string) (func(), error) {
	_, unlock, err := acquireFileLock(ctx, path)
	return unlock, err
}

func acquireFileLock(ctx context.Context, path string) (*os.File, func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			var once sync.Once
			return f, func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, nil, ctx.Err()
		case <-time.After(50 * time.Millisecond):
		}
	}
}

type BudgetRecord struct {
	Day  string `json:"day"`
	Used int64  `json:"used"`
}
type Budgets struct{ Path string }

func (b Budgets) Read(iface string) (BudgetRecord, error) {
	all := map[string]BudgetRecord{}
	err := ReadJSON(b.Path, &all)
	if errors.Is(err, os.ErrNotExist) {
		return BudgetRecord{}, nil
	}
	return all[iface], err
}
func (b Budgets) Reserve(ctx context.Context, iface string, now time.Time, amount, limit int64) (BudgetRecord, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	if !identifier(iface) || amount <= 0 || limit <= 0 {
		return BudgetRecord{}, errors.New("invalid budget request")
	}
	unlock, err := fileLock(ctx, b.Path+".lock")
	if err != nil {
		return BudgetRecord{}, err
	}
	defer unlock()
	all := map[string]BudgetRecord{}
	err = ReadJSON(b.Path, &all)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return BudgetRecord{}, err
	}
	r := all[iface]
	day := now.UTC().Format("2006-01-02")
	if day > r.Day {
		r = BudgetRecord{Day: day}
	}
	if r.Used < 0 || amount > limit-r.Used {
		return r, errors.New("daily probe budget exhausted")
	}
	r.Used += amount
	all[iface] = r
	if err = AtomicJSON(b.Path, all); err != nil {
		return BudgetRecord{}, err
	}
	return r, nil
}
func Uptime() (float64, error) {
	b, err := os.ReadFile("/proc/uptime")
	if err != nil {
		return 0, err
	}
	f := strings.Fields(string(b))
	if len(f) < 1 {
		return 0, errors.New("invalid uptime")
	}
	return strconv.ParseFloat(f[0], 64)
}

type WatchdogReady struct {
	PID    int     `json:"pid"`
	Uptime float64 `json:"uptime"`
}
type Recovery struct {
	Dir              string
	Now              func() (float64, error)
	ReadProc         func(int) ([]byte, error)
	PID              int
	Session          string
	HeartbeatStopped *atomic.Bool
}

func (r Recovery) Ready() error {
	var record struct {
		PID    *int     `json:"pid"`
		Uptime *float64 `json:"uptime"`
	}
	if err := ReadJSON(filepath.Join(r.Dir, "watchdog.ready"), &record); err != nil {
		cause := fmt.Errorf("watchdog readiness: %w", err)
		if errors.Is(err, os.ErrNotExist) {
			return &WatchdogUnavailableError{Cause: cause}
		}
		return cause
	}
	if record.PID == nil || record.Uptime == nil {
		return errors.New("watchdog readiness record is missing PID or uptime")
	}
	w := WatchdogReady{PID: *record.PID, Uptime: *record.Uptime}
	up, err := r.Now()
	if err != nil {
		return fmt.Errorf("watchdog uptime: %w", err)
	}
	if w.PID <= 1 {
		return errors.New("invalid watchdog PID")
	}
	if math.IsNaN(up) || math.IsInf(up, 0) || up < 0 || w.Uptime < 0 || up-w.Uptime < 0 {
		return errors.New("watchdog readiness has invalid or future uptime")
	}
	cmd, err := r.ReadProc(w.PID)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return &WatchdogUnavailableError{Cause: fmt.Errorf("watchdog process absent: %w", err)}
		}
		return fmt.Errorf("watchdog process inspection: %w", err)
	}
	found := false
	for _, arg := range strings.Split(string(cmd), "\x00") {
		if arg == "/usr/libexec/mwan3-autobalancer/watchdog" {
			found = true
		}
	}
	if !found {
		return errors.New("watchdog PID is not the independent watchdog")
	}
	if up-w.Uptime > 15 {
		return &WatchdogUnavailableError{Cause: errors.New("watchdog readiness expired")}
	}
	return nil
}

type Heartbeat struct {
	PID     int     `json:"pid"`
	Session string  `json:"session"`
	Uptime  float64 `json:"uptime"`
}
type Lease struct {
	Policy           string  `json:"policy"`
	Uptime           float64 `json:"uptime"`
	PID              int     `json:"pid"`
	Session          string  `json:"session"`
	HeartbeatFile    string  `json:"heartbeat_file"`
	ChainHash        string  `json:"chain_hash,omitempty"`
	RestoreRequested bool    `json:"restore_requested"`
	RestoreReason    string  `json:"restore_reason,omitempty"`
}

func NewRecovery(dir string) Recovery {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return Recovery{Dir: dir, PID: os.Getpid(), Session: hex.EncodeToString(nonce), HeartbeatStopped: new(atomic.Bool), Now: Uptime, ReadProc: func(pid int) ([]byte, error) { return os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)) }}
}
func (r Recovery) HeartbeatName() string { return fmt.Sprintf("heartbeat.%d.json", r.PID) }
func (r Recovery) Heartbeat() error {
	if r.HeartbeatStopped != nil && r.HeartbeatStopped.Load() {
		return errors.New("owner heartbeat stopped for independent recovery")
	}
	if r.PID < 2 || len(r.Session) != 32 {
		return errors.New("missing recovery owner identity")
	}
	up, err := r.Now()
	if err != nil {
		return err
	}
	return AtomicJSON(filepath.Join(r.Dir, r.HeartbeatName()), Heartbeat{r.PID, r.Session, up})
}
func (r Recovery) Arm(policy string, chainHash ...string) error {
	if r.HeartbeatStopped != nil && r.HeartbeatStopped.Load() {
		return errors.New("owner must restart after failed independent recovery")
	}
	if err := r.Ready(); err != nil {
		return &armReadinessError{Cause: err}
	}
	var previous Lease
	if err := ReadJSON(filepath.Join(r.Dir, "lease.json"), &previous); err == nil {
		if previous.RestoreRequested {
			return errors.New("independent restore requested; recovery must complete before applying")
		}
		if previous.Policy != policy {
			return errors.New("active lease belongs to another policy; restore the previous policy before switching")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("existing recovery lease is invalid: %w", err)
	}
	if err := r.Heartbeat(); err != nil {
		return err
	}
	up, err := r.Now()
	if err != nil {
		return err
	}
	lease := Lease{Policy: policy, Uptime: up, PID: r.PID, Session: r.Session, HeartbeatFile: r.HeartbeatName()}
	if len(chainHash) > 0 {
		lease.ChainHash = chainHash[0]
	}
	return AtomicJSON(filepath.Join(r.Dir, "lease.json"), lease)
}
func (r Recovery) RequestRestore(reason string) error {
	var lease Lease
	path := filepath.Join(r.Dir, "lease.json")
	if err := ReadJSON(path, &lease); err != nil {
		return err
	}
	if lease.PID != r.PID || lease.Session != r.Session {
		return errors.New("cannot request restoration for another lease owner")
	}
	lease.RestoreRequested = true
	lease.RestoreReason = reason
	return AtomicJSON(path, lease)
}
func (r Recovery) RecordChain(hash string) error {
	var lease Lease
	path := filepath.Join(r.Dir, "lease.json")
	if err := ReadJSON(path, &lease); err != nil {
		return err
	}
	if lease.PID != r.PID || lease.Session != r.Session {
		return errors.New("recovery lease owner changed")
	}
	lease.ChainHash = hash
	return AtomicJSON(path, lease)
}
func (r Recovery) StopHeartbeat() {
	if r.HeartbeatStopped != nil {
		r.HeartbeatStopped.Store(true)
	}
	_ = os.Remove(filepath.Join(r.Dir, r.HeartbeatName()))
}

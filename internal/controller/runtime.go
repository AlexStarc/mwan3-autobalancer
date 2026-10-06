package controller

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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
func (ExecRunner) Run(ctx context.Context, args []string, input string) (string, error) {
	if len(args) == 0 {
		return "", errors.New("empty command")
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
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
	err := cmd.Run()
	if out.overflow || errout.overflow {
		return out.b.String(), errors.New("command output exceeded limit")
	}
	if err != nil {
		code := -1
		if cmd.ProcessState != nil {
			code = cmd.ProcessState.ExitCode()
		}
		return out.b.String(), &CommandError{args[0], code, err, strings.TrimSpace(errout.b.String())}
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
func fileLock(ctx context.Context, path string) (func(), error) {
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return nil, err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	for {
		err = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB)
		if err == nil {
			var once sync.Once
			return func() { once.Do(func() { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }) }, nil
		}
		if err != syscall.EWOULDBLOCK && err != syscall.EAGAIN {
			f.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			f.Close()
			return nil, ctx.Err()
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
	Dir      string
	Now      func() (float64, error)
	ReadProc func(int) ([]byte, error)
	PID      int
	Session  string
}

func (r Recovery) Ready() error {
	var w WatchdogReady
	if err := ReadJSON(filepath.Join(r.Dir, "watchdog.ready"), &w); err != nil {
		return fmt.Errorf("watchdog readiness: %w", err)
	}
	up, err := r.Now()
	if err != nil {
		return err
	}
	if w.PID <= 1 || w.Uptime < 0 || up-w.Uptime < 0 || up-w.Uptime > 15 {
		return errors.New("watchdog readiness is stale")
	}
	cmd, err := r.ReadProc(w.PID)
	if err != nil {
		return errors.New("watchdog process absent")
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
	return nil
}

type Heartbeat struct {
	PID     int     `json:"pid"`
	Session string  `json:"session"`
	Uptime  float64 `json:"uptime"`
}
type Lease struct {
	Policy        string  `json:"policy"`
	Uptime        float64 `json:"uptime"`
	PID           int     `json:"pid"`
	Session       string  `json:"session"`
	HeartbeatFile string  `json:"heartbeat_file"`
}

func NewRecovery(dir string) Recovery {
	nonce := make([]byte, 16)
	if _, err := rand.Read(nonce); err != nil {
		panic(err)
	}
	return Recovery{Dir: dir, PID: os.Getpid(), Session: hex.EncodeToString(nonce), Now: Uptime, ReadProc: func(pid int) ([]byte, error) { return os.ReadFile(fmt.Sprintf("/proc/%d/cmdline", pid)) }}
}
func (r Recovery) HeartbeatName() string { return fmt.Sprintf("heartbeat.%d.json", r.PID) }
func (r Recovery) Heartbeat() error {
	if r.PID < 2 || len(r.Session) != 32 {
		return errors.New("missing recovery owner identity")
	}
	up, err := r.Now()
	if err != nil {
		return err
	}
	return AtomicJSON(filepath.Join(r.Dir, r.HeartbeatName()), Heartbeat{r.PID, r.Session, up})
}
func (r Recovery) Arm(policy string) error {
	if err := r.Ready(); err != nil {
		return err
	}
	var previous Lease
	if err := ReadJSON(filepath.Join(r.Dir, "lease.json"), &previous); err == nil {
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
	return AtomicJSON(filepath.Join(r.Dir, "lease.json"), Lease{policy, up, r.PID, r.Session, r.HeartbeatName()})
}

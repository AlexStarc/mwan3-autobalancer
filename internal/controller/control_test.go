package controller

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestDaemonSocketAndCleanup(t *testing.T) {
	x := makeFixture(t)
	short, err := os.MkdirTemp("/private/tmp", "ab-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	x.dir = short
	x.adapter.Recovery.Dir = short
	e := makeEngine(t, x)
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- e.Daemon(ctx) }()
	socket := filepath.Join(x.dir, "control.sock")
	deadline := time.Now().Add(2 * time.Second)
	for {
		var r Report
		if err := Control(context.Background(), socket, "status", &r); err == nil {
			if r.Policy != "balanced" {
				t.Fatal(r)
			}
			break
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("daemon socket unavailable")
		}
		time.Sleep(10 * time.Millisecond)
	}
	info, err := os.Stat(socket)
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	var reply ControlReply
	if err := Control(context.Background(), socket, "probe", &reply); err != nil || !reply.Queued {
		t.Fatal(reply, err)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("bounded daemon shutdown failed")
	}
	if _, err = os.Stat(socket); !os.IsNotExist(err) {
		t.Fatal("socket remains", err)
	}
}
func TestProcessGroupCancellationAndOutputBound(t *testing.T) {
	runner := ExecRunner{}
	ctx, cancel := context.WithTimeout(context.Background(), 100*time.Millisecond)
	defer cancel()
	start := time.Now()
	if _, err := runner.Run(ctx, []string{"sh", "-c", "sleep 30 & wait"}, ""); err == nil {
		t.Fatal("canceled process accepted")
	}
	if time.Since(start) > 2*time.Second {
		t.Fatal("process group not promptly reaped")
	}
	out, err := runner.Run(context.Background(), []string{"sh", "-c", "while :; do printf 'abcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyzabcdefghijklmnopqrstuvwxyz'; done"}, "")
	if err == nil || !strings.Contains(err.Error(), "output exceeded") || len(out) > 256*1024 {
		t.Fatal(len(out), err)
	}
}
func TestStatusReflectsCurrentChain(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	x.ready(t)
	e := makeEngine(t, x)
	s, err := e.refresh(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if err = x.adapter.Apply(ctx, s, []int{600, 400}, true); err != nil {
		t.Fatal(err)
	}
	r, err := e.Status(ctx)
	if err != nil || r.Channels[0].AppliedWeight != 600 || r.Channels[1].AppliedWeight != 400 {
		t.Fatal(r, err)
	}
	x.save = "*mangle\n:mwan3_policy_balanced - [0:0]\n-A mwan3_policy_balanced -j RETURN\nCOMMIT\n"
	r, err = e.Status(ctx)
	if err != nil || r.Channels[0].AppliedWeight != 0 {
		t.Fatal("reported saved weights after external rollback", r, err)
	}
}

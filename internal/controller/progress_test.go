package controller

import (
	"context"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func TestSocketStatusPublishesEachSerialProbeStartAndFinish(t *testing.T) {
	x := makeFixture(t)
	// Use a short Unix path: macOS's default temp path can exceed the socket limit.
	short, err := os.MkdirTemp("/tmp", "ab-progress-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(short)
	x.dir = short
	x.adapter.Recovery.Dir = short
	e := makeEngine(t, x)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if _, err = e.refresh(ctx); err != nil {
		t.Fatal(err)
	}
	// Existing stale measurements must not hide the active "measuring" state.
	for _, iface := range []string{"a", "b"} {
		e.state.Samples[iface] = Sample{Speed: 50, Count: 2, At: x.now.Add(-48 * time.Hour)}
		sc := e.state.Schedule[iface]
		sc.Complete = true
		sc.Next = x.now.Add(6 * time.Hour)
		e.state.Schedule[iface] = sc
	}
	started := make(chan string, 2)
	releaseA := make(chan struct{})
	releaseB := make(chan struct{})
	var closeA, closeB sync.Once
	defer closeA.Do(func() { close(releaseA) })
	defer closeB.Do(func() { close(releaseB) })
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] == "mwan3" {
			started <- a[2]
			if a[2] == "a" {
				<-releaseA
			} else {
				<-releaseB
			}
		}
		return base(a, in)
	}
	done := make(chan error, 1)
	go func() { done <- e.Daemon(ctx) }()
	socket := filepath.Join(short, "control.sock")
	readReport := func() Report {
		t.Helper()
		var report Report
		if err := Control(context.Background(), socket, "status", &report); err != nil {
			t.Fatal(err)
		}
		return report
	}
	deadline := time.Now().Add(2 * time.Second)
	for {
		var r Report
		if err = Control(context.Background(), socket, "status", &r); err == nil && r.Policy == "balanced" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("socket unavailable", err)
		}
		time.Sleep(10 * time.Millisecond)
	}
	var reply ControlReply
	if err = Control(context.Background(), socket, "probe", &reply); err != nil || !reply.Queued {
		t.Fatal(reply, err)
	}
	waitStart := func(expected string) {
		t.Helper()
		select {
		case iface := <-started:
			if iface != expected {
				t.Fatal(iface, expected)
			}
		case <-time.After(2 * time.Second):
			t.Fatal("probe did not start", expected)
		}
	}
	waitStart("a")
	r := readReport()
	if !r.Busy || r.Channels[0].ProbeState != "measuring" || r.Channels[0].ValidSamples != 2 {
		t.Fatal("cached report hid first active probe", r)
	}
	closeA.Do(func() { close(releaseA) })
	waitStart("b")
	r = readReport()
	if !r.Busy || r.Channels[0].ProbeState != "measured" || r.Channels[0].ValidSamples != 3 || r.Channels[0].SpeedMbps == nil || r.Channels[1].ProbeState != "measuring" {
		t.Fatal("cached report hid completed first/current second probe", r)
	}
	closeB.Do(func() { close(releaseB) })
	deadline = time.Now().Add(2 * time.Second)
	for {
		r = readReport()
		if !r.Busy {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("probe cycle did not finish")
		}
		time.Sleep(10 * time.Millisecond)
	}
	if r.Channels[1].ProbeState != "measured" || r.Channels[1].ValidSamples != 3 {
		t.Fatal("completed second probe not published", r)
	}
	cancel()
	select {
	case err = <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("daemon did not stop")
	}
}

package controller

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"path/filepath"
	"sync/atomic"
	"time"
)

type ControlRequest struct {
	Command string `json:"command"`
}
type ControlReply struct {
	Queued bool   `json:"queued,omitempty"`
	Error  string `json:"error,omitempty"`
}

// Control is root-only through filesystem ownership and mode; there is no HTTP listener.
func (e *Engine) Daemon(ctx context.Context) error {
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	unlock, err := fileLock(lockCtx, filepath.Join(e.Adapter.Recovery.Dir, "controller.lock"))
	if err != nil {
		return errors.New("another controller owns the runtime")
	}
	defer unlock()
	socket := filepath.Join(e.Adapter.Recovery.Dir, "control.sock")
	_ = os.Remove(socket)
	ln, err := net.Listen("unix", socket)
	if err != nil {
		return err
	}
	defer ln.Close()
	defer os.Remove(socket)
	if err = os.Chmod(socket, 0600); err != nil {
		return err
	}
	ownedCtx, stop := context.WithCancel(ctx)
	defer stop()
	go e.Heartbeat(ownedCtx)
	_, _ = e.Status(ctx)
	queue := make(chan struct{}, 1)
	var queued atomic.Bool
	connections := make(chan struct{}, 8)
	go func() { <-ownedCtx.Done(); _ = ln.Close() }()
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			select {
			case connections <- struct{}{}:
			default:
				conn.Close()
				continue
			}
			go func(c net.Conn) {
				defer func() { <-connections; c.Close() }()
				_ = c.SetDeadline(time.Now().Add(3 * time.Second))
				var req ControlRequest
				if err := json.NewDecoder(io.LimitReader(c, 4096)).Decode(&req); err != nil {
					_ = json.NewEncoder(c).Encode(ControlReply{Error: "invalid bounded request"})
					return
				}
				switch req.Command {
				case "status":
					_ = json.NewEncoder(c).Encode(e.Report())
				case "probe":
					if e.Report().Busy || !queued.CompareAndSwap(false, true) {
						_ = json.NewEncoder(c).Encode(ControlReply{Error: "probe cycle already queued or active"})
						return
					}
					queue <- struct{}{}
					_ = json.NewEncoder(c).Encode(ControlReply{Queued: true})
				default:
					_ = json.NewEncoder(c).Encode(ControlReply{Error: "unknown command"})
				}
			}(conn)
		}
	}()
	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-queue: // User queued probes are observation-only regardless of automatic configuration.
			_ = e.Cycle(ctx, true, false)
			queued.Store(false)
		case <-ticker.C:
			_ = e.Tick(ctx)
		}
	}
}
func Control(ctx context.Context, socket, command string, out any) error {
	d := net.Dialer{Timeout: time.Second}
	conn, err := d.DialContext(ctx, "unix", socket)
	if err != nil {
		return err
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(3 * time.Second))
	if err = json.NewEncoder(conn).Encode(ControlRequest{command}); err != nil {
		return err
	}
	return json.NewDecoder(io.LimitReader(conn, 1024*1024)).Decode(out)
}
func (e *Engine) Once(ctx context.Context, apply bool) error {
	lockCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	unlock, err := fileLock(lockCtx, filepath.Join(e.Adapter.Recovery.Dir, "controller.lock"))
	if err != nil {
		return errors.New("daemon owns runtime; use probe for an asynchronous dry run")
	}
	defer unlock()
	if apply {
		if err = e.Adapter.Recovery.Ready(); err != nil {
			return err
		}
		heartbeatCtx, stop := context.WithCancel(ctx)
		defer stop()
		go e.Heartbeat(heartbeatCtx)
	}
	return e.Cycle(ctx, true, apply)
}

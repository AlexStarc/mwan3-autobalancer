package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestProbeRealCurlWithoutHTTPResponse(t *testing.T) {
	curl, err := exec.LookPath("curl")
	if err != nil {
		t.Fatal("curl is required for the native statistics regression", err)
	}
	x := makeFixture(t)
	s, err := x.adapter.Discover(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	base := x.runner.fn
	var transferErr *CommandError
	var output string
	x.runner.fn = func(args []string, input string) (string, error) {
		if args[0] != "mwan3" {
			return base(args, input)
		}
		format := ""
		for i := 0; i+1 < len(args); i++ {
			if args[i] == "--write-out" {
				format = args[i+1]
				break
			}
		}
		if format == "" {
			t.Fatal("probe omitted curl statistics")
		}
		// Exercise the production write-out against a real curl failure without
		// contacting any network, touching routing, or downloading an object.
		missing := (&url.URL{Scheme: "file", Path: filepath.Join(t.TempDir(), "missing")}).String()
		cmd := exec.Command(curl, "--silent", "--show-error", "--noproxy", "*", "--output", "/dev/null", "--write-out", format, "--url", missing)
		var stderr strings.Builder
		cmd.Stderr = &stderr
		data, cause := cmd.Output()
		var exit *exec.ExitError
		if !errors.As(cause, &exit) || exit.ExitCode() != 37 {
			t.Fatalf("missing-file curl failed unexpectedly: %v %s", cause, stderr.String())
		}
		output = string(data)
		transferErr = &CommandError{Command: "mwan3", Code: exit.ExitCode(), Cause: cause, Stderr: strings.TrimSpace(stderr.String())}
		return output, transferErr
	}
	b := Budgets{Path: filepath.Join(x.dir, "budgets")}
	speed, probeErr := x.adapter.Probe(context.Background(), s, s.Channels[0], b, x.now)
	if speed != 0 || transferErr == nil || !errors.Is(probeErr, transferErr) {
		t.Errorf("real curl failure was masked: speed=%v err=%v", speed, probeErr)
	}
	stats, statsErr := ParseCurl(output)
	if statsErr != nil || stats.Code != 0 {
		t.Errorf("missing HTTP response must remain valid JSON: %+v %v; output=%s", stats, statsErr, output)
	}
	assertProbeBudget(t, x, s, b)
}

func TestProbeTransferDiagnosticsAndCappedSamples(t *testing.T) {
	for _, tc := range []struct {
		name     string
		code     int
		bytes    float64
		total    float64
		start    float64
		exit     int
		badStats string
		want     string
		accepted bool
	}{
		{name: "connect-timeout", exit: 28, total: 4, want: "probe transfer failed"},
		{name: "timeout-without-response-at-limit", exit: 28, total: 15, want: "probe transfer failed"},
		{name: "timeout-without-body-at-limit", code: 200, exit: 28, total: 15, start: 1, want: "probe transfer failed"},
		{name: "tls-failure", exit: 60, total: 1, want: "probe transfer failed"},
		{name: "size-limit", code: 206, bytes: 1048576, total: 3, start: 1, exit: 63, want: "probe transfer failed"},
		{name: "failed-malformed-statistics", exit: 7, badStats: "{broken}", want: "invalid final curl statistics"},
		{name: "failed-missing-statistics", exit: 127, badStats: "\n", want: "invalid final curl statistics"},
		{name: "successful-malformed-statistics", badStats: "{broken}", want: "invalid final curl statistics"},
		{name: "http-error", code: 403, bytes: 1048576, total: 3, start: 1, want: "probe HTTP status 403"},
		{name: "complete-success", code: 206, bytes: 1048576, total: 3, start: 1, accepted: true},
		{name: "duration-capped-200", code: 200, bytes: 1048576, total: 15, start: 1, exit: 28, accepted: true},
		{name: "duration-capped-206", code: 206, bytes: 1048576, total: 15, start: 1, exit: 28, accepted: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			x := makeFixture(t)
			s, err := x.adapter.Discover(context.Background())
			if err != nil {
				t.Fatal(err)
			}
			data, err := json.Marshal(CurlStats{Code: tc.code, Bytes: tc.bytes, Total: tc.total, Start: tc.start, LocalIP: s.Channels[0].SourceIP, RemoteIP: "192.0.2.99"})
			if err != nil {
				t.Fatal(err)
			}
			x.curl = "mwan3 diagnostic line\n" + string(data) + "\n"
			if tc.badStats != "" {
				x.curl = tc.badStats
			}
			if tc.exit != 0 {
				x.curlErr = &CommandError{Command: "mwan3", Code: tc.exit, Cause: fmt.Errorf("fixture exit %d", tc.exit), Stderr: fmt.Sprintf("curl: (%d) fixture transport error", tc.exit)}
			}
			b := Budgets{Path: filepath.Join(x.dir, "budgets")}
			speed, err := x.adapter.Probe(context.Background(), s, s.Channels[0], b, x.now)
			if tc.accepted {
				want := tc.bytes * 8 / (tc.total - tc.start) / 1000000
				if err != nil || speed != want {
					t.Fatalf("valid rate sample changed: %v %v, want %v", speed, err, want)
				}
			} else {
				if speed != 0 || err == nil || !strings.Contains(err.Error(), tc.want) {
					t.Fatalf("wrong diagnostic: speed=%v err=%v, want %s", speed, err, tc.want)
				}
				if x.curlErr != nil && (!errors.Is(err, x.curlErr) || !strings.Contains(err.Error(), "probe transfer failed")) {
					t.Fatalf("original transfer failure was masked: %v", err)
				}
			}
			assertProbeBudget(t, x, s, b)
		})
	}
}

func assertProbeBudget(t *testing.T, x *fixture, s Snapshot, b Budgets) {
	t.Helper()
	r, err := b.Read(s.Channels[0].Interface)
	if err != nil || r.Used != s.Config.For(s.Channels[0].Interface).Bytes || x.runner.count("mwan3") != 1 {
		t.Fatalf("probe quota or attempt count changed: %+v %v, attempts=%d", r, err, x.runner.count("mwan3"))
	}
	if x.runner.count("iptables-restore") != 0 {
		t.Fatal("probe changed routing")
	}
}

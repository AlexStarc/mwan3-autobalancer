package controller

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net"
	"net/url"
	"strconv"
	"strings"
	"time"
)

type CurlStats struct {
	Code     int     `json:"http_code"`
	Bytes    float64 `json:"size_download"`
	Total    float64 `json:"time_total"`
	Start    float64 `json:"time_starttransfer"`
	LocalIP  string  `json:"local_ip"`
	RemoteIP string  `json:"remote_ip"`
	Speed    float64 `json:"speed_download"`
}

func ParseCurl(out string) (CurlStats, error) {
	var s CurlStats
	lines := strings.Split(strings.TrimSpace(out), "\n")
	if len(lines) == 0 {
		return s, errors.New("missing curl statistics")
	}
	err := json.Unmarshal([]byte(lines[len(lines)-1]), &s)
	if err != nil {
		return s, fmt.Errorf("invalid final curl statistics: %w", err)
	}
	return s, nil
}

type ShortTransferError struct{ Bytes, Duration float64 }

func (e *ShortTransferError) Error() string {
	return "insufficient confidence: transfer too short; use a larger object or {bytes} URL template"
}
func ExpandProbeURL(raw string, payload int64) string {
	return strings.ReplaceAll(raw, "{bytes}", strconv.FormatInt(payload, 10))
}
func (a *Adapter) Probe(ctx context.Context, s Snapshot, c Channel, b Budgets, now time.Time) (float64, error) {
	p := s.Config.For(c.Interface)
	speed, err := a.probeAttempt(ctx, s, c, b, now, p.Bytes, ExpandProbeURL(s.Config.URL, p.Bytes))
	var short *ShortTransferError
	if !errors.As(err, &short) || ctx.Err() != nil || p.Bytes >= s.Config.MaxProbeBytes {
		return speed, err
	}
	retry := p.Bytes * 2
	if short.Duration > 0 {
		needed := int64(math.Ceil(short.Bytes * s.Config.MinimumSeconds / short.Duration * 1.1))
		if needed > retry {
			retry = needed
		}
	}
	if retry > s.Config.MaxProbeBytes {
		retry = s.Config.MaxProbeBytes
	}
	if retry <= p.Bytes {
		return speed, err
	}
	// Exactly one larger request; its full payload is durably reserved independently before network.
	return a.probeAttempt(ctx, s, c, b, now, retry, ExpandProbeURL(s.Config.URL, retry))
}
func (a *Adapter) probeAttempt(ctx context.Context, s Snapshot, c Channel, b Budgets, now time.Time, payload int64, probeURL string) (float64, error) {
	if !c.Online {
		return 0, errors.New("WAN is offline; no probe")
	}
	if c.Family != "ipv4" {
		return 0, errors.New("active probes require an IPv4 mwan3 interface")
	}
	if s.Config.URL == "" {
		return 0, errors.New("set probe_url to an HTTP(S) test object to enable measurement")
	}
	if err := ValidateURL(probeURL); err != nil {
		return 0, err
	}
	p := s.Config.For(c.Interface)
	p.Bytes = payload
	version, err := a.Runner.Run(ctx, []string{"curl", "--version"}, "")
	if err != nil || !StreamingCapableCurl(version) {
		return 0, errors.New("safe probing requires curl >= 8.4 with streaming max-filesize enforcement")
	}
	// Resolve explicitly for both stock routing preflight and curl, preventing DNS rebind between checks.
	u, _ := url.Parse(probeURL)
	host := u.Hostname()
	port := u.Port()
	if port == "" {
		port = "443"
		if u.Scheme == "http" {
			port = "80"
		}
	}
	portN, e := strconv.Atoi(port)
	if e != nil || portN < 1 || portN > 65535 {
		return 0, errors.New("invalid probe port")
	}
	remote := net.ParseIP(host)
	if remote == nil {
		ips, err := net.DefaultResolver.LookupIP(ctx, "ip4", host)
		if err != nil || len(ips) == 0 {
			return 0, errors.New("probe hostname has no IPv4 address")
		}
		remote = ips[0]
	}
	if remote.To4() == nil {
		return 0, errors.New("probe destination must have IPv4")
	}
	mark := fmt.Sprintf("0x%x", uint32(c.ID)<<s.Shift)
	route, err := a.Runner.Run(ctx, []string{"ip", "-4", "route", "get", remote.String(), "from", c.SourceIP, "mark", mark}, "")
	if err != nil {
		return 0, fmt.Errorf("route preflight: %w", err)
	}
	fields := strings.Fields(route)
	has := func(k, v string) bool {
		for i := 0; i+1 < len(fields); i++ {
			if fields[i] == k && fields[i+1] == v {
				return true
			}
		}
		return false
	}
	if !has("dev", c.Device) || (!has("src", c.SourceIP) && !has("from", c.SourceIP)) {
		return 0, errors.New("route preflight does not use selected source and device")
	}
	if _, err = b.Reserve(ctx, c.Interface, now, p.Bytes, p.DailyBudget); err != nil {
		return 0, err
	}
	probeCtx, cancel := context.WithTimeout(ctx, s.Config.Timeout+2*time.Second)
	defer cancel()
	// Native JSON encodes a missing HTTP response as 0, not the raw invalid
	// JSON number 000, and escapes all string fields. The curl >= 8.4 gate
	// already exceeds the version that introduced this write-out format.
	statsFormat := "%{json}\n"
	args := []string{"mwan3", "use", c.Interface, "curl", "-4", "--silent", "--show-error", "--noproxy", "*", "--interface", c.Device, "--connect-timeout", "4", "--max-time", strconv.FormatFloat(s.Config.Timeout.Seconds(), 'f', 0, 64), "--max-filesize", strconv.FormatInt(p.Bytes, 10), "--range", "0-" + strconv.FormatInt(p.Bytes-1, 10), "--proto", "=http,https", "--proto-redir", "=http,https", "--resolve", host + ":" + port + ":" + remote.String(), "--output", "/dev/null", "--write-out", statsFormat, "--url", probeURL}
	out, runErr := a.Runner.Run(probeCtx, args, "")
	stats, err := ParseCurl(out)
	if err != nil {
		if runErr != nil {
			return 0, errors.Join(fmt.Errorf("probe transfer failed: %w", runErr), err)
		}
		return 0, err
	}
	if runErr != nil {
		var commandErr *CommandError
		// A duration-capped HTTP transfer is a valid rate sample only after body data actually flowed.
		if !errors.As(runErr, &commandErr) || commandErr.Code != 28 || stats.Total < s.Config.Timeout.Seconds()-.5 || (stats.Code != 200 && stats.Code != 206) || stats.Bytes <= 0 {
			return 0, fmt.Errorf("probe transfer failed: %w", runErr)
		}
	}
	if stats.Code != 200 && stats.Code != 206 {
		return 0, fmt.Errorf("probe HTTP status %d", stats.Code)
	}
	duration := stats.Total - stats.Start
	if stats.Bytes > float64(p.Bytes) || stats.Bytes < float64(s.Config.MinimumBytes) || duration <= 0 {
		return 0, errors.New("insufficient confidence: payload outside limits or no transfer duration")
	}
	if stats.LocalIP != c.SourceIP || stats.RemoteIP != remote.String() {
		return 0, errors.New("probe source or destination changed")
	}
	fresh, err := a.Discover(ctx)
	if err != nil || fresh.Generation != s.Generation {
		return 0, errors.New("probe discarded: topology/source generation changed")
	}
	if duration < s.Config.MinimumSeconds {
		return 0, &ShortTransferError{stats.Bytes, duration}
	}
	speed := stats.Bytes * 8 / duration / 1000000
	if speed <= 0 {
		return 0, errors.New("invalid measured speed")
	}
	return speed, nil
}
func StreamingCapableCurl(v string) bool {
	f := strings.Fields(v)
	if len(f) < 2 || f[0] != "curl" {
		return false
	}
	parts := strings.Split(f[1], ".")
	if len(parts) < 2 {
		return false
	}
	major, e := strconv.Atoi(parts[0])
	if e != nil {
		return false
	}
	minor, e := strconv.Atoi(parts[1])
	return e == nil && (major > 8 || (major == 8 && minor >= 4))
}

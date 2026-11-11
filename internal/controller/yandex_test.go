package controller

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

const yandexTarget = "https://node.cdn.yandex.net/token/probes/50mb?mid=example"
const yandexMetadata = `{"download":{"probes":[{"url":"https://small.cdn.yandex.net/probes/100kb","timeout":100},{"url":"` + yandexTarget + `"}]}}`

func yandexFixture(t *testing.T) (*fixture, *int, *string) {
	t.Helper()
	x := makeFixture(t)
	x.cfg.Values["main"]["probe_provider"] = "yandex"
	x.cfg.Values["main"]["probe_url"] = ""
	x.adapter.LookupIPv4 = func(context.Context, string) ([]net.IP, error) { return []net.IP{net.ParseIP("192.0.2.99")}, nil }
	calls := new(int)
	body := new(string)
	*body = yandexMetadata
	base := x.runner.fn
	x.runner.fn = func(a []string, in string) (string, error) {
		if a[0] == "mwan3" && strings.Contains(strings.Join(a, " "), "/get-probes?") {
			*calls++
			stats, _ := json.Marshal(CurlStats{Code: 200, Bytes: float64(len(*body)), LocalIP: "192.0.2.1", RemoteIP: "192.0.2.99"})
			return *body + "\n" + string(stats) + "\n", nil
		}
		return base(a, in)
	}
	return x, calls, body
}
func TestProbeProviderConfig(t *testing.T) {
	x := makeFixture(t)
	c, err := ParseConfig(x.cfg)
	if err != nil || c.ProbeProvider != "url" {
		t.Fatal(c, err)
	}
	x.cfg.Values["main"]["probe_provider"] = "yandex"
	x.cfg.Values["main"]["probe_url"] = ""
	c, err = ParseConfig(x.cfg)
	if err != nil || !c.HasProbeTarget() {
		t.Fatal(c, err)
	}
	x.cfg.Values["main"]["probe_provider"] = "untrusted"
	if _, err = ParseConfig(x.cfg); err == nil {
		t.Fatal("unknown provider accepted")
	}
}
func TestYandexMetadataSelection(t *testing.T) {
	if u, err := parseYandexMetadata([]byte(yandexMetadata)); err != nil || u != yandexTarget {
		t.Fatal(u, err)
	}
	for _, target := range []string{
		"http://node.cdn.yandex.net/probes/50mb", "https://cdn.yandex.net.evil.test/probes/50mb", "https://evilcdn.yandex.net/probes/50mb", "https://cdn.yandex.net/probes/50mb", "https://user@node.cdn.yandex.net/probes/50mb", "https://node.cdn.yandex.net:444/probes/50mb", "https://node.cdn.yandex.net/probes/50mb#fragment", "https://node.cdn.yandex.net/probes/100kb", "https://node.cdn.yandex.net/probes/50mb?timeout=100", "https://node.cdn.yandex.net/probes/50mb?x=%0a",
	} {
		b, _ := json.Marshal(map[string]any{"download": map[string]any{"probes": []any{map[string]any{"url": target}}}})
		if _, err := parseYandexMetadata(b); err == nil {
			t.Fatalf("accepted %s", target)
		}
	}
	for _, body := range []string{`{}`, "mwan3 diagnostic line\n" + yandexMetadata, yandexMetadata + `{}`, strings.Repeat(" ", 65537), `{"download":{"probes":[{"url":"` + yandexTarget + `","timeout":100}]}}`} {
		if _, err := parseYandexMetadata([]byte(body)); err == nil {
			t.Fatal("accepted invalid metadata")
		}
	}
	fallback := `{"download":{"probes":[{"url":"https://bad.test/probes/50mb","fallbackUrl":"` + yandexTarget + `"}]}}`
	if u, err := parseYandexMetadata([]byte(fallback)); err != nil || u != yandexTarget {
		t.Fatal(u, err)
	}
}
func TestYandexDiscoveryCacheBudgetAndBindings(t *testing.T) {
	ctx := context.Background()
	x, calls, _ := yandexFixture(t)
	s, err := x.adapter.Discover(ctx)
	if err != nil {
		t.Fatal(err)
	}
	b := Budgets{filepath.Join(x.dir, "budgets")}
	c := s.Channels[0]
	if _, err = x.adapter.Probe(ctx, s, c, b, x.now); err != nil {
		t.Fatal(err)
	}
	if *calls != 1 {
		t.Fatal(*calls)
	}
	r, _ := b.Read(c.Interface)
	if r.Used != s.Config.Bytes+yandexMetadataLimit {
		t.Fatal(r)
	}
	for _, args := range x.runner.calls {
		if args[0] == "mwan3" && strings.Contains(strings.Join(args, " "), "/get-probes?") {
			line := strings.Join(args, " ")
			for _, want := range []string{"mwan3 use a curl -q -4", "--interface eth2", "--max-time 8", "--max-filesize 65536", "--proto =https", "--resolve yandex.ru:443:192.0.2.99"} {
				if !strings.Contains(line, want) {
					t.Fatal(line, want)
				}
			}
			for _, bad := range []string{"--location", "--insecure", "--range"} {
				if strings.Contains(line, bad) {
					t.Fatal(line)
				}
			}
		}
	}
	// A new process and generation retain the same source-bound cache.
	a := *x.adapter
	s.Generation = "changed"
	u, err := a.yandexURL(ctx, s, c, b, x.now.Add(time.Hour))
	if err != nil || u != yandexTarget || *calls != 1 {
		t.Fatal(u, err, *calls)
	}
	info, err := os.Stat(a.yandexCachePath(c))
	if err != nil || info.Mode().Perm() != 0600 {
		t.Fatal(info, err)
	}
	c.SourceIP = "192.0.2.2"
	if a.yandexCachePath(c) == a.yandexCachePath(s.Channels[0]) {
		t.Fatal("cache shared across source")
	}
	c = s.Channels[0]
	c.Device = "eth3"
	if a.yandexCachePath(c) == a.yandexCachePath(s.Channels[0]) {
		t.Fatal("cache shared across devices")
	}
	s, _ = a.Discover(ctx)
	_, err = a.yandexURL(ctx, s, s.Channels[0], b, x.now.Add(25*time.Hour))
	if err != nil || *calls != 2 {
		t.Fatal(err, *calls)
	}
}
func TestYandexCooldownAndFailedRefresh(t *testing.T) {
	ctx := context.Background()
	x, calls, body := yandexFixture(t)
	s, _ := x.adapter.Discover(ctx)
	c := s.Channels[0]
	b := Budgets{filepath.Join(x.dir, "budgets")}
	*body = `{}`
	if _, err := x.adapter.yandexURL(ctx, s, c, b, x.now); err == nil {
		t.Fatal("invalid metadata accepted")
	}
	_, err := x.adapter.yandexURL(ctx, s, c, b, x.now.Add(time.Minute))
	var deferred *ProbeDeferredError
	if !errors.As(err, &deferred) || *calls != 1 || !deferred.Until.Equal(x.now.Add(10*time.Minute)) {
		t.Fatal(err, *calls)
	}
	*body = yandexMetadata
	if _, err = x.adapter.yandexURL(ctx, s, c, b, x.now.Add(10*time.Minute)); err != nil {
		t.Fatal(err)
	}
	for i := 0; i < 3; i++ {
		if err = x.adapter.recordYandexResult(c, &TargetTransferError{errors.New("HTTP 500")}); err != nil {
			t.Fatal(err)
		}
	}
	*body = `{}`
	if u, err := x.adapter.yandexURL(ctx, s, c, b, x.now.Add(21*time.Minute)); err != nil || u != yandexTarget || *calls != 3 {
		t.Fatal(u, err, *calls)
	}
	var cache yandexCache
	_ = ReadJSON(x.adapter.yandexCachePath(c), &cache)
	before := cache.Failures
	for _, err := range []error{errors.New("daily probe budget exhausted"), &ShortTransferError{1, 1}, errors.New("topology changed")} {
		_ = x.adapter.recordYandexResult(c, err)
	}
	_ = ReadJSON(x.adapter.yandexCachePath(c), &cache)
	if cache.Failures != before {
		t.Fatal(cache)
	}
}
func TestYandexNoMetadataForStatusOfflineOrQuota(t *testing.T) {
	ctx := context.Background()
	x, calls, _ := yandexFixture(t)
	e := makeEngine(t, x)
	if _, err := e.Status(ctx); err != nil || *calls != 0 {
		t.Fatal(err, *calls)
	}
	s, _ := x.adapter.Discover(ctx)
	c := s.Channels[0]
	b := e.Budgets
	c.Online = false
	if _, err := x.adapter.Probe(ctx, s, c, b, x.now); err == nil || *calls != 0 {
		t.Fatal(err, *calls)
	}
	c = s.Channels[0]
	_, _ = b.Reserve(ctx, c.Interface, x.now, s.Config.DailyBudget, s.Config.DailyBudget)
	if _, err := x.adapter.Probe(ctx, s, c, b, x.now); err == nil || *calls != 0 {
		t.Fatal(err, *calls)
	}
}
func TestYandexCalibrationDeferralAndAttemptBound(t *testing.T) {
	ctx := context.Background()
	x, calls, body := yandexFixture(t)
	*body = `{}`
	x.cfg.Values["main"]["schedule_mode"] = "on-change"
	e := makeEngine(t, x)
	_ = e.Tick(ctx)
	x.now = x.now.Add(time.Minute)
	_ = e.Tick(ctx)
	if *calls != 2 || e.state.Schedule["a"].Attempts != 1 {
		t.Fatal(*calls, e.state.Schedule)
	}
	x.now = x.now.Add(2 * time.Minute)
	_ = e.Tick(ctx)
	sc := e.state.Schedule["a"]
	if sc.Attempts != 1 || !sc.Next.Equal(x.now.Add(8*time.Minute)) || sc.CalibrationUntil.Before(sc.Next) {
		t.Fatal(sc)
	}
	for i := 0; i < 30; i++ {
		x.now = x.now.Add(2 * time.Minute)
		_ = e.Tick(ctx)
	}
	if *calls != 10 || e.state.Schedule["a"].Attempts != 5 || !e.state.Schedule["a"].Complete {
		t.Fatal(*calls, e.state.Schedule)
	}
}

func TestYandexRejectsUnboundAndOversizeResponses(t *testing.T) {
	for _, kind := range []string{"route", "source", "destination", "generation", "oversize", "HTTP", "transport"} {
		t.Run(kind, func(t *testing.T) {
			ctx := context.Background()
			x, calls, body := yandexFixture(t)
			s, _ := x.adapter.Discover(ctx)
			b := Budgets{filepath.Join(x.dir, "budgets")}
			base := x.runner.fn
			if kind == "oversize" {
				*body = strings.Repeat(" ", 65537) + yandexMetadata
			}
			x.runner.fn = func(args []string, in string) (string, error) {
				if kind == "route" && args[0] == "ip" {
					return "192.0.2.99 from 192.0.2.1 dev eth9", nil
				}
				out, err := base(args, in)
				if args[0] == "mwan3" {
					switch kind {
					case "source":
						out = strings.ReplaceAll(out, "192.0.2.1", "192.0.2.2")
					case "destination":
						out = strings.ReplaceAll(out, "192.0.2.99", "192.0.2.98")
					case "generation":
						x.raw = "topology changed during discovery"
					case "HTTP":
						out = strings.ReplaceAll(out, `"http_code":200`, `"http_code":302`)
					case "transport":
						err = errors.New("TLS verification failed")
					}
				}
				return out, err
			}
			if _, err := x.adapter.Probe(ctx, s, s.Channels[0], b, x.now); err == nil {
				t.Fatal("unsafe discovery accepted")
			}
			var cache yandexCache
			_ = ReadJSON(x.adapter.yandexCachePath(s.Channels[0]), &cache)
			if cache.URL != "" {
				t.Fatal(cache)
			}
			if kind == "route" && *calls != 0 {
				t.Fatal("network before route validation")
			}
		})
	}
}
func TestYandexThresholdDefersWithoutDiscardingHistory(t *testing.T) {
	ctx := context.Background()
	x, calls, body := yandexFixture(t)
	x.cfg.Values["main"]["schedule_mode"] = "on-change"
	e := makeEngine(t, x)
	_ = e.Tick(ctx)
	x.now = x.now.Add(time.Minute)
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	// One retained valid sample; three genuine target failures trigger discovery.
	x.curl = strings.ReplaceAll(x.curl, `"http_code":206`, `"http_code":503`)
	for i := 0; i < 3; i++ {
		x.now = x.now.Add(2 * time.Minute)
		_ = e.Tick(ctx)
	}
	if *calls != 2 || e.state.Schedule["a"].Attempts != 4 || e.state.Samples["a"].Count != 1 {
		t.Fatal(*calls, e.state.Schedule, e.state.Samples)
	}
	x.now = x.now.Add(2 * time.Minute)
	_ = e.Tick(ctx)
	if e.state.Schedule["a"].Attempts != 4 || *calls != 2 {
		t.Fatal("cooldown consumed an attempt", e.state.Schedule, *calls)
	}
	next := e.state.Schedule["a"].Next
	if !next.Equal(x.now.Add(2*time.Minute)) || e.state.Schedule["a"].CalibrationUntil.Before(next) {
		t.Fatal(e.state.Schedule)
	}
	// Dynamic target replacement changes neither channel epoch nor sample history.
	*body = strings.ReplaceAll(yandexMetadata, "node.cdn.yandex.net", "new.cdn.yandex.net")
	x.curl = strings.ReplaceAll(x.curl, `"http_code":503`, `"http_code":206`)
	x.now = next
	if err := e.Tick(ctx); err != nil {
		t.Fatal(err)
	}
	if *calls != 4 || e.state.Schedule["a"].Attempts != 5 || !e.state.Schedule["a"].Complete || e.state.Samples["a"].Count != 2 {
		t.Fatal(*calls, e.state.Schedule, e.state.Samples)
	}
	n := x.runner.count("mwan3")
	x.now = x.now.Add(48 * time.Hour)
	_ = e.Tick(ctx)
	if x.runner.count("mwan3") != n {
		t.Fatal("on-change restarted after completed discovery")
	}
}

func TestYandexExpiredCacheSurvivesAPIOutageAndCorruptCacheIsIgnored(t *testing.T) {
	ctx := context.Background()
	x, calls, body := yandexFixture(t)
	s, _ := x.adapter.Discover(ctx)
	c := s.Channels[0]
	b := Budgets{filepath.Join(x.dir, "budgets")}
	if _, err := x.adapter.yandexURL(ctx, s, c, b, x.now); err != nil {
		t.Fatal(err)
	}
	*body = `{}`
	x.now = x.now.Add(25 * time.Hour)
	if u, err := x.adapter.yandexURL(ctx, s, c, b, x.now); err != nil || u != yandexTarget || *calls != 2 {
		t.Fatal(u, err, *calls)
	}
	if u, err := x.adapter.yandexURL(ctx, s, c, b, x.now.Add(time.Minute)); err != nil || u != yandexTarget || *calls != 2 {
		t.Fatal(u, err, *calls)
	}
	// ReadJSON reports trailing data after assigning the first object: none of that
	// partial record may be trusted after its error.
	file := x.adapter.yandexCachePath(c)
	cache, _ := json.Marshal(yandexCache{URL: yandexTarget, Fetched: x.now, Attempted: x.now})
	if err := os.WriteFile(file, append(cache, []byte(` {}`)...), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := x.adapter.yandexURL(ctx, s, c, b, x.now); err == nil || *calls != 3 {
		t.Fatal(err, *calls)
	}
	*body = yandexMetadata
	if err := AtomicJSON(file, yandexCache{URL: yandexTarget, Fetched: x.now.Add(time.Hour), Attempted: x.now.Add(time.Hour)}); err != nil {
		t.Fatal(err)
	}
	if _, err := x.adapter.yandexURL(ctx, s, c, b, x.now); err != nil || *calls != 4 {
		t.Fatal(err, *calls)
	}
}

func TestYandexShutdownDoesNotCountAsTargetFailure(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	x, _, _ := yandexFixture(t)
	s, _ := x.adapter.Discover(ctx)
	c := s.Channels[0]
	b := Budgets{filepath.Join(x.dir, "budgets")}
	if _, err := x.adapter.yandexURL(ctx, s, c, b, x.now); err != nil {
		t.Fatal(err)
	}
	base := x.runner.fn
	x.runner.fn = func(args []string, in string) (string, error) {
		if args[0] == "mwan3" {
			cancel()
			return x.curl, context.Canceled
		}
		return base(args, in)
	}
	if _, err := x.adapter.Probe(ctx, s, c, b, x.now); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	var cache yandexCache
	_ = ReadJSON(x.adapter.yandexCachePath(c), &cache)
	if cache.Failures != 0 {
		t.Fatal("shutdown counted as target failure", cache)
	}
}

func TestLegacyURLEpochRetainsPersistedSamples(t *testing.T) {
	ctx := context.Background()
	x := makeFixture(t)
	s, _ := x.adapter.Discover(ctx)
	c := s.Channels[0]
	legacy, _ := json.Marshal(struct {
		Policy, URL                             string
		ProbeBytes, MaxProbeBytes, MinimumBytes int64
		Timeout                                 time.Duration
		MinimumSeconds, Alpha                   float64
		Member, Interface, Device, Source       string
		Metric, Weight                          int
		Online                                  bool
		Mask                                    uint32
		ID                                      int
	}{s.Config.Policy, s.Config.URL, s.Config.For(c.Interface).Bytes, s.Config.MaxProbeBytes, s.Config.MinimumBytes, s.Config.Timeout, s.Config.MinimumSeconds, s.Config.Alpha, c.Member, c.Interface, c.Device, c.SourceIP, c.Metric, c.BaselineWeight, c.Online, s.Mask, c.ID})
	e := makeEngine(t, x)
	e.state.Schedule[c.Interface] = Schedule{Epoch: string(legacy), Complete: true}
	e.state.Samples[c.Interface] = Sample{Speed: 42, Count: 2, At: x.now}
	if _, err := e.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if e.state.Samples[c.Interface].Count != 2 || !e.state.Schedule[c.Interface].Complete {
		t.Fatal("URL-only upgrade discarded history")
	}
	x.cfg.Values["main"]["probe_provider"] = "yandex"
	if _, err := e.Status(ctx); err != nil {
		t.Fatal(err)
	}
	if e.state.Samples[c.Interface].Count != 0 {
		t.Fatal("provider switch retained incompatible history")
	}
}

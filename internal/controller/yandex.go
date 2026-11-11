package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math"
	"net/url"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

const yandexMetadataLimit int64 = 64 * 1024
const yandexCacheTTL = 24 * time.Hour
const yandexRefreshCooldown = 10 * time.Minute

type ProbeDeferredError struct{ Until time.Time }

func (e *ProbeDeferredError) Error() string {
	return "Yandex discovery cooldown until " + e.Until.UTC().Format(time.RFC3339)
}

type yandexCache struct {
	URL       string    `json:"url"`
	Fetched   time.Time `json:"fetched"`
	Attempted time.Time `json:"attempted"`
	Failures  int       `json:"failures"`
}

func (a *Adapter) yandexCachePath(c Channel) string {
	sum := sha256.Sum256([]byte(c.Interface + "\x00" + c.Device + "\x00" + c.SourceIP))
	return filepath.Join(a.Recovery.Dir, "yandex-"+hex.EncodeToString(sum[:])+".json")
}
func validYandexURL(raw string) bool {
	if strings.IndexFunc(raw, unicode.IsControl) >= 0 {
		return false
	}
	u, err := url.Parse(raw)
	if err != nil || u.Scheme != "https" || u.User != nil || u.Fragment != "" || u.Hostname() == "" || (u.Port() != "" && u.Port() != "443") {
		return false
	}
	host := strings.ToLower(u.Hostname())
	if !strings.HasSuffix(host, ".cdn.yandex.net") {
		return false
	}
	for _, label := range strings.Split(host, ".") {
		if label == "" || len(label) > 63 || label[0] == '-' || label[len(label)-1] == '-' {
			return false
		}
		for _, ch := range label {
			if !(ch >= 'a' && ch <= 'z' || ch >= '0' && ch <= '9' || ch == '-') {
				return false
			}
		}
	}
	decoded, err := url.QueryUnescape(raw)
	if err != nil || strings.IndexFunc(decoded, unicode.IsControl) >= 0 {
		return false
	}
	if path.Clean(u.Path) != u.Path || !strings.HasSuffix(u.Path, "/probes/50mb") {
		return false
	}
	for key := range u.Query() {
		if strings.EqualFold(key, "timeout") {
			return false
		}
	}
	return true
}
func parseYandexMetadata(body []byte) (string, error) {
	if len(body) > int(yandexMetadataLimit) {
		return "", errors.New("Yandex metadata exceeds limit")
	}
	var response struct {
		Download struct {
			Probes []struct {
				URL         string          `json:"url"`
				FallbackURL string          `json:"fallbackUrl"`
				Timeout     json.RawMessage `json:"timeout"`
			} `json:"probes"`
		} `json:"download"`
	}
	if err := json.Unmarshal(body, &response); err != nil {
		return "", fmt.Errorf("invalid Yandex metadata: %w", err)
	}
	for _, probe := range response.Download.Probes {
		if len(probe.Timeout) > 0 {
			continue
		}
		for _, raw := range []string{probe.URL, probe.FallbackURL} {
			if validYandexURL(raw) {
				return raw, nil
			}
		}
	}
	return "", errors.New("Yandex metadata contains no trusted large download probe")
}
func (a *Adapter) yandexURL(ctx context.Context, s Snapshot, c Channel, b Budgets, now time.Time) (string, error) {
	if !c.Online || c.Family != "ipv4" {
		return "", errors.New("Yandex discovery requires an online IPv4 WAN")
	}
	file := a.yandexCachePath(c)
	var cache yandexCache
	// A damaged private cache can safely be rediscovered; it never supplies an unchecked URL.
	if err := ReadJSON(file, &cache); err != nil || cache.Fetched.After(now) || cache.Attempted.After(now) || cache.Failures < 0 {
		cache = yandexCache{}
	}
	usable := validYandexURL(cache.URL) && !cache.Fetched.IsZero()
	fresh := usable && now.Sub(cache.Fetched) < yandexCacheTTL
	if fresh && cache.Failures < 3 {
		return cache.URL, nil
	}
	if !cache.Attempted.IsZero() && now.Before(cache.Attempted.Add(yandexRefreshCooldown)) {
		if usable && cache.Failures < 3 {
			return cache.URL, nil
		}
		return "", &ProbeDeferredError{cache.Attempted.Add(yandexRefreshCooldown)}
	}
	target, err := a.fetchYandexMetadata(ctx, s, c, b, now, func() error { cache.Attempted = now; return AtomicJSON(file, cache) })
	if err != nil {
		if usable {
			return cache.URL, nil
		}
		return "", err
	}
	cache.URL = target
	cache.Fetched = now
	cache.Failures = 0
	if err = AtomicJSON(file, cache); err != nil {
		return "", err
	}
	return target, nil
}
func (a *Adapter) recordYandexResult(c Channel, result error) error {
	var transfer *TargetTransferError
	if result != nil && !errors.As(result, &transfer) {
		return nil
	}
	var cache yandexCache
	file := a.yandexCachePath(c)
	if err := ReadJSON(file, &cache); err != nil {
		return err
	}
	if result == nil {
		cache.Failures = 0
	} else if cache.Failures < 3 {
		cache.Failures++
	}
	return AtomicJSON(file, cache)
}
func (a *Adapter) fetchYandexMetadata(ctx context.Context, s Snapshot, c Channel, b Budgets, now time.Time, started func() error) (string, error) {
	target := "https://yandex.ru/internet/api/v0/get-probes?nocache=" + strconv.FormatInt(now.UnixNano(), 10)
	metadataCtx, cancel := context.WithTimeout(ctx, 8*time.Second)
	defer cancel()
	remote, host, port, err := a.probeRoute(metadataCtx, s, c, target)
	if err != nil {
		return "", err
	}
	// mwan3 prints the wrapped command (including write-out newlines) to
	// stdout. Keep the response in a private file so diagnostics never become
	// part of the JSON document and do not require heuristic prefix stripping.
	if err = os.MkdirAll(a.Recovery.Dir, 0700); err != nil {
		return "", err
	}
	bodyFile, err := os.CreateTemp(a.Recovery.Dir, ".yandex-metadata-*")
	if err != nil {
		return "", err
	}
	bodyPath := bodyFile.Name()
	defer os.Remove(bodyPath)
	if err = bodyFile.Close(); err != nil {
		return "", err
	}
	if _, err = b.Reserve(metadataCtx, c.Interface, now, yandexMetadataLimit, s.Config.For(c.Interface).DailyBudget); err != nil {
		return "", err
	}
	// Persist cooldown before issuing the request, including failed network requests and restarts.
	if err = started(); err != nil {
		return "", err
	}
	args := []string{"mwan3", "use", c.Interface, "curl", "-q", "-4", "--silent", "--show-error", "--noproxy", "*", "--interface", c.Device, "--connect-timeout", "4", "--max-time", "8", "--max-filesize", strconv.FormatInt(yandexMetadataLimit, 10), "--proto", "=https", "--proto-redir", "=https", "--resolve", host + ":" + port + ":" + remote.String(), "--output", bodyPath, "--write-out", "\n%{json}\n", "--url", target}
	out, runErr := a.Runner.Run(metadataCtx, args, "")
	if runErr != nil {
		return "", fmt.Errorf("Yandex discovery transfer failed: %w", runErr)
	}
	stats, err := ParseCurl(out)
	if err != nil {
		return "", err
	}
	if stats.Bytes > float64(yandexMetadataLimit) || stats.Bytes < 0 || math.IsNaN(stats.Bytes) || math.IsInf(stats.Bytes, 0) {
		return "", errors.New("Yandex discovery exceeds response limit")
	}
	if stats.Code != 200 {
		return "", fmt.Errorf("Yandex discovery HTTP status %d", stats.Code)
	}
	if stats.LocalIP != c.SourceIP || stats.RemoteIP != remote.String() {
		return "", errors.New("Yandex discovery source or destination changed")
	}
	fresh, err := a.Discover(metadataCtx)
	if err != nil || fresh.Generation != s.Generation {
		return "", errors.New("Yandex discovery discarded: topology/source generation changed")
	}
	bodyFile, err = os.Open(bodyPath)
	if err != nil {
		return "", err
	}
	defer bodyFile.Close()
	body, err := io.ReadAll(io.LimitReader(bodyFile, yandexMetadataLimit+1))
	if err != nil {
		return "", err
	}
	return parseYandexMetadata(body)
}

package controller

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"time"
)

var idPattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_-]{0,31}$`)

func identifier(s string) bool { return idPattern.MatchString(s) }

var devicePattern = regexp.MustCompile(`^[A-Za-z0-9_][A-Za-z0-9_.:-]{0,14}$`)

func deviceName(s string) bool { return devicePattern.MatchString(s) }

type Section map[string]any
type UCI struct {
	Values map[string]Section `json:"values"`
}

func (s Section) Text(k string) string { v, _ := s[k].(string); return v }
func (s Section) List(k string) []string {
	v := s[k]
	if a, ok := v.([]any); ok {
		out := []string{}
		for _, x := range a {
			z, ok := x.(string)
			if !ok {
				return nil
			}
			out = append(out, z)
		}
		return out
	}
	if x, ok := v.(string); ok {
		return strings.Fields(x)
	}
	return nil
}
func (s Section) Index() int {
	if f, ok := s[".index"].(float64); ok {
		return int(f)
	}
	i, _ := strconv.Atoi(s.Text(".index"))
	return i
}

type ProbeConfig struct {
	Interval    time.Duration
	Bytes       int64
	DailyBudget int64
}
type Config struct {
	Enabled      bool
	Mode         string
	Policy       string
	URL          string
	ScheduleMode string
	ProbeConfig
	Timeout        time.Duration
	MinimumSeconds float64
	MinimumBytes   int64
	Alpha          float64
	MaxAge         time.Duration
	Hysteresis     float64
	MinimumApply   time.Duration
	WAN            map[string]ProbeConfig
}

func DefaultConfig() Config {
	return Config{Mode: "observe", Policy: "balanced", ScheduleMode: "hybrid", ProbeConfig: ProbeConfig{6 * time.Hour, 33554432, 268435456}, Timeout: 15 * time.Second, MinimumSeconds: 2, MinimumBytes: 262144, Alpha: .25, MaxAge: 24 * time.Hour, Hysteresis: 5, MinimumApply: time.Minute, WAN: map[string]ProbeConfig{}}
}
func ParseConfig(u UCI) (Config, error) {
	c := DefaultConfig()
	main, ok := u.Values["main"]
	if !ok {
		return c, errors.New("missing mwan3_autobalancer.main")
	}
	if v := main.Text("enabled"); v != "" && v != "0" && v != "1" {
		return c, errors.New("enabled must be 0 or 1")
	}
	c.Enabled = main.Text("enabled") == "1"
	if v := main.Text("mode"); v != "" {
		c.Mode = v
	}
	if c.Mode != "observe" && c.Mode != "automatic" {
		return c, errors.New("mode must be observe or automatic")
	}
	if v := main.Text("policy"); v != "" {
		c.Policy = v
	}
	if !identifier(c.Policy) || len(c.Policy) > 15 {
		return c, errors.New("invalid policy name")
	}
	c.URL = main.Text("probe_url")
	if err := ValidateURL(c.URL); err != nil {
		return c, err
	}
	if v := main.Text("schedule_mode"); v != "" {
		c.ScheduleMode = v
	}
	if c.ScheduleMode != "hybrid" && c.ScheduleMode != "on-change" {
		return c, errors.New("invalid schedule_mode")
	}
	parse := func(s Section, k string, def, min, max float64) (float64, error) {
		v := s.Text(k)
		if v == "" {
			return def, nil
		}
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f < min || f > max || strings.ContainsAny(v, "nNiI") {
			return 0, fmt.Errorf("invalid %s", k)
		}
		return f, nil
	}
	integer := func(s Section, k string, def, min, max int64) (int64, error) {
		v := s.Text(k)
		if v == "" {
			return def, nil
		}
		n, err := strconv.ParseInt(v, 10, 64)
		if err != nil || n < min || n > max {
			return 0, fmt.Errorf("invalid %s", k)
		}
		return n, nil
	}
	pc := func(s Section, p ProbeConfig) (ProbeConfig, error) {
		n, e := integer(s, "interval_seconds", int64(p.Interval/time.Second), 60, 604800)
		if e != nil {
			return p, e
		}
		p.Interval = time.Duration(n) * time.Second
		p.Bytes, e = integer(s, "probe_bytes", p.Bytes, 262144, 1073741824)
		if e != nil {
			return p, e
		}
		p.DailyBudget, e = integer(s, "daily_budget_bytes", p.DailyBudget, p.Bytes, 1099511627776)
		return p, e
	}
	var err error
	c.ProbeConfig, err = pc(main, c.ProbeConfig)
	if err != nil {
		return c, err
	}
	n, err := integer(main, "timeout_seconds", 15, 3, 25)
	if err != nil {
		return c, err
	}
	c.Timeout = time.Duration(n) * time.Second
	c.MinimumSeconds, err = parse(main, "minimum_seconds", 2, .1, float64(n))
	if err != nil {
		return c, err
	}
	c.MinimumBytes, err = integer(main, "minimum_bytes", 262144, 1, c.Bytes)
	if err != nil {
		return c, err
	}
	c.Alpha, err = parse(main, "alpha", .25, .01, 1)
	if err != nil {
		return c, err
	}
	n, err = integer(main, "max_age_seconds", 86400, 60, 2592000)
	if err != nil {
		return c, err
	}
	c.MaxAge = time.Duration(n) * time.Second
	c.Hysteresis, err = parse(main, "hysteresis_percent", 5, 0, 100)
	if err != nil {
		return c, err
	}
	n, err = integer(main, "minimum_apply_seconds", 60, 60, 86400)
	if err != nil {
		return c, err
	}
	c.MinimumApply = time.Duration(n) * time.Second
	for _, s := range u.Values {
		if s.Text(".type") != "wan" {
			continue
		}
		iface := s.Text("interface")
		if !identifier(iface) {
			return c, errors.New("invalid WAN interface override")
		}
		if _, ok := c.WAN[iface]; ok {
			return c, errors.New("duplicate WAN override")
		}
		p, err := pc(s, c.ProbeConfig)
		if err != nil {
			return c, err
		}
		if p.Bytes < c.MinimumBytes {
			return c, errors.New("WAN probe smaller than minimum_bytes")
		}
		c.WAN[iface] = p
	}
	return c, nil
}
func (c Config) For(iface string) ProbeConfig {
	if p, ok := c.WAN[iface]; ok {
		return p
	}
	return c.ProbeConfig
}
func ValidateURL(s string) error {
	if s == "" {
		return nil
	}
	u, err := url.Parse(s)
	if err != nil || u.Host == "" || u.Hostname() == "" || (u.Scheme != "https" && u.Scheme != "http") || u.User != nil || u.Fragment != "" || strings.ContainsAny(s, "\r\n\x00") {
		return errors.New("probe_url must be an HTTP(S) URL without credentials or fragment")
	}
	return nil
}
func DecodeUCI(s string) (UCI, error) {
	var u UCI
	err := json.Unmarshal([]byte(s), &u)
	if err == nil && u.Values == nil {
		err = errors.New("missing UCI values")
	}
	return u, err
}

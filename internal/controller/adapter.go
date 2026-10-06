package controller

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"math/bits"
	"net"
	"reflect"
	"sort"
	"strconv"
	"strings"
	"time"
)

type Snapshot struct {
	Config             Config
	Channels           []Channel
	Generation         string
	Mask               uint32
	Shift              int
	LastResort         string
	Compatible         bool
	CompatibilityError string
	Applied            []int
	LiveSave           string
}
type Adapter struct {
	Runner   Runner
	ReadFile func(string) ([]byte, error)
	LockPath string
	Recovery Recovery
}

func (a *Adapter) uci(ctx context.Context, name string) (UCI, error) {
	s, err := a.Runner.Run(ctx, []string{"ubus", "call", "uci", "get", `{"config":"` + name + `"}`}, "")
	if err != nil {
		return UCI{}, err
	}
	return DecodeUCI(s)
}
func (a *Adapter) Discover(ctx context.Context) (Snapshot, error) {
	s := Snapshot{Compatible: true}
	cu, err := a.uci(ctx, "mwan3_autobalancer")
	if err != nil {
		return s, err
	}
	s.Config, err = ParseConfig(cu)
	if err != nil {
		return s, err
	}
	u, err := a.uci(ctx, "mwan3")
	if err != nil {
		return s, err
	}
	policy, ok := u.Values[s.Config.Policy]
	if !ok || policy.Text(".type") != "policy" {
		return s, errors.New("selected policy is absent")
	}
	s.LastResort = policy.Text("last_resort")
	if s.LastResort == "" {
		s.LastResort = "unreachable"
	}
	if s.LastResort != "default" && s.LastResort != "unreachable" && s.LastResort != "blackhole" {
		return s, errors.New("unsupported last_resort")
	}
	maskText := "0x3f00"
	for _, v := range u.Values {
		if v.Text(".type") == "globals" && v.Text("mmx_mask") != "" {
			maskText = v.Text("mmx_mask")
		}
	}
	mask, err := strconv.ParseUint(maskText, 0, 32)
	if err != nil || mask == 0 {
		return s, errors.New("invalid MMX mask")
	}
	s.Mask = uint32(mask)
	s.Shift = bits.TrailingZeros32(s.Mask)
	n := s.Mask >> s.Shift
	if n&(n+1) != 0 || bits.OnesCount32(s.Mask) < 3 {
		return s, errors.New("unsupported non-contiguous MMX mask")
	}
	type ordered struct {
		name  string
		index int
	}
	order := []ordered{}
	for name, v := range u.Values {
		if v.Text(".type") == "interface" {
			if !identifier(name) {
				return s, errors.New("invalid logical interface name")
			}
			order = append(order, ordered{name, v.Index()})
		}
	}
	sort.Slice(order, func(i, j int) bool { return order[i].index < order[j].index })
	ids := map[string]int{}
	for i, x := range order {
		if i > 0 && order[i-1].index == x.index {
			return s, errors.New("ambiguous interface order")
		}
		ids[x.name] = i + 1
	}
	for iface := range s.Config.WAN {
		if _, ok := ids[iface]; !ok {
			return s, fmt.Errorf("unknown WAN override %s", iface)
		}
	}
	seen := map[string]bool{}
	for _, name := range policy.List("use_member") {
		if !identifier(name) {
			return s, errors.New("invalid member name")
		}
		m, ok := u.Values[name]
		if !ok || m.Text(".type") != "member" {
			return s, errors.New("unknown policy member")
		}
		iface := m.Text("interface")
		id, ok := ids[iface]
		if !ok || seen[iface] || uint32(id) > n-3 {
			return s, errors.New("unknown, duplicate, or unrepresentable WAN")
		}
		seen[iface] = true
		v := u.Values[iface]
		weight, err := positive(m.Text("weight"), 1)
		if err != nil {
			return s, err
		}
		metric, err := positive(m.Text("metric"), 1)
		if err != nil {
			return s, err
		}
		c := Channel{Member: name, Interface: iface, ID: id, Metric: metric, BaselineWeight: weight, Enabled: v.Text("enabled") == "1", Family: v.Text("family"), ProbeState: "unknown"}
		if c.Family == "" {
			c.Family = "ipv4"
		}
		if c.Family != "ipv4" {
			s.Compatible = false
			s.CompatibilityError = "only IPv4 mwan3 interfaces are supported for apply"
		}
		st, err := a.Runner.Run(ctx, []string{"ubus", "call", "network.interface." + iface, "status"}, "")
		if err != nil {
			return s, err
		}
		var status struct {
			Up     bool   `json:"up"`
			Device string `json:"l3_device"`
			IPv4   []struct {
				Address string `json:"address"`
			} `json:"ipv4-address"`
		}
		if err = json.Unmarshal([]byte(st), &status); err != nil {
			return s, err
		}
		c.Device = status.Device
		if c.Device != "" && !deviceName(c.Device) {
			return s, errors.New("unsupported device name")
		}
		if len(status.IPv4) > 0 {
			c.SourceIP = status.IPv4[0].Address
			if net.ParseIP(c.SourceIP) == nil || net.ParseIP(c.SourceIP).To4() == nil {
				return s, errors.New("invalid source IPv4")
			}
		}
		b, e := a.ReadFile("/var/run/mwan3/iface_state/" + iface)
		c.Online = c.Enabled && status.Up && e == nil && strings.TrimSpace(string(b)) == "online" && c.Device != "" && c.SourceIP != ""
		s.Channels = append(s.Channels, c)
	}
	if len(s.Channels) == 0 {
		return s, errors.New("selected policy has no members")
	}
	ver, err := a.Runner.Run(ctx, []string{"opkg", "list-installed", "mwan3"}, "")
	if err != nil || strings.TrimSpace(ver) != "mwan3 - 2.11.16-r5" {
		s.Compatible = false
		s.CompatibilityError = "apply requires installed mwan3 2.11.16-r5"
	}
	ipver, err := a.Runner.Run(ctx, []string{"iptables", "--version"}, "")
	if err != nil || !strings.Contains(ipver, "(legacy)") {
		s.Compatible = false
		s.CompatibilityError = "apply requires iptables legacy"
	}
	raw, err := a.ReadFile("/etc/config/mwan3")
	if err != nil {
		return s, err
	}
	rawAuto, err := a.ReadFile("/etc/config/mwan3_autobalancer")
	if err != nil {
		return s, err
	}
	data, _ := json.Marshal(struct {
		Channels   []Channel
		Mask       uint32
		Policy     string
		LastResort string
	}{s.Channels, s.Mask, s.Config.Policy, s.LastResort})
	hash := sha256.New()
	hash.Write(raw)
	hash.Write(rawAuto)
	hash.Write(data)
	s.Generation = hex.EncodeToString(hash.Sum(nil))
	save, readErr := a.Runner.Run(ctx, []string{"iptables-save", "-t", "mangle"}, "")
	if readErr == nil {
		s.LiveSave = save
		s.Applied = CurrentWeights(save, s)
	}
	return s, nil
}

// Decode the same cumulative probabilities as the native mwan3 leaf; unknown rules remain unclaimed.
func CurrentWeights(save string, s Snapshot) []int {
	out := make([]int, len(s.Channels))
	lines, _ := chainLines(save, "mwan3_policy_"+s.Config.Policy)
	remaining := 1.0
	for _, line := range lines {
		r, err := parseLeafRule(line, "mwan3_policy_"+s.Config.Policy, s.Mask)
		if err != nil {
			return make([]int, len(s.Channels))
		}
		if !r.comment || r.out {
			continue
		}
		for i, c := range s.Channels {
			if c.Interface == r.iface && r.mark == uint32(c.ID)<<s.Shift {
				share := remaining * r.probability
				out[i] = int(math.Round(share * 1000))
				remaining -= share
			}
		}
	}
	return out
}
func positive(s string, def int) (int, error) {
	if s == "" {
		return def, nil
	}
	n, err := strconv.Atoi(s)
	if err != nil || n < 1 || n > 1000000 {
		return 0, errors.New("invalid member metric or weight")
	}
	return n, nil
}
func chainLines(save, chain string) ([]string, bool) {
	out := []string{}
	exists := false
	for _, line := range strings.Split(save, "\n") {
		if strings.HasPrefix(line, ":"+chain+" ") {
			exists = true
		}
		if strings.HasPrefix(line, "-A "+chain+" ") {
			out = append(out, line)
		}
	}
	return out, exists
}
func BuildRules(s Snapshot, w []int) (string, []string, error) {
	if len(w) != len(s.Channels) || !identifier(s.Config.Policy) {
		return "", nil, errors.New("invalid rule input")
	}
	chain := "mwan3_policy_" + s.Config.Policy
	total := 0
	for _, v := range w {
		if v < 0 {
			return "", nil, errors.New("negative weight")
		}
		total += v
	}
	if total != 0 && total != 1000 {
		return "", nil, errors.New("weights must sum to 1000")
	}
	rules := []string{}
	remaining := total
	for i, c := range s.Channels {
		if w[i] == 0 {
			continue
		}
		rule := fmt.Sprintf("-A %s -m mark --mark 0x0/0x%x", chain, s.Mask)
		if w[i] < remaining {
			rule += fmt.Sprintf(" -m statistic --mode random --probability %.11f", float64(w[i])/float64(remaining))
		}
		rule += fmt.Sprintf(" -m comment --comment \"%s %d %d\" -j MARK --set-xmark 0x%x/0x%x", c.Interface, w[i], remaining, uint32(c.ID)<<s.Shift, s.Mask)
		rules = append(rules, rule)
		remaining -= w[i]
	}
	if total == 0 {
		maxID := s.Mask >> s.Shift
		id := maxID
		switch s.LastResort {
		case "blackhole":
			id -= 2
		case "unreachable":
			id--
		}
		rules = append(rules, fmt.Sprintf("-A %s -m mark --mark 0x0/0x%x -j MARK --set-xmark 0x%x/0x%x", chain, s.Mask, id<<s.Shift, s.Mask))
	}
	return "*mangle\n-F " + chain + "\n" + strings.Join(rules, "\n") + "\nCOMMIT\n", rules, nil
}
func (a *Adapter) Apply(ctx context.Context, s Snapshot, w []int, explicit ...bool) error {
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if !s.Compatible {
		return errors.New(s.CompatibilityError)
	}
	if err := a.Recovery.Ready(); err != nil {
		return err
	}
	unlock, err := fileLock(ctx, a.LockPath)
	if err != nil {
		return err
	}
	defer unlock()
	if err = a.Recovery.Ready(); err != nil {
		return err
	}
	fresh, err := a.Discover(ctx)
	if err != nil {
		return err
	}
	if !fresh.Compatible || fresh.Generation != s.Generation {
		return errors.New("stale generation or incompatible current configuration")
	}
	before, err := a.Runner.Run(ctx, []string{"iptables-save", "-t", "mangle"}, "")
	if err != nil {
		return err
	}
	chain := "mwan3_policy_" + s.Config.Policy
	if !a.AllowedLeaf(before, fresh) {
		unlock()
		pauseErr := a.Pause(context.WithoutCancel(ctx))
		return fmt.Errorf("policy leaf conflict: neither current stock baseline nor owned chain; automatic mode paused: %v", pauseErr)
	}
	restore, rules, err := BuildRules(s, w)
	if err != nil {
		return err
	}
	if _, err = a.Runner.Run(ctx, []string{"iptables-restore", "--test", "--noflush"}, restore); err != nil {
		return err
	}
	currentSnapshot, err := a.Discover(ctx)
	if err != nil {
		return err
	}
	if currentSnapshot.Generation != s.Generation || !currentSnapshot.Compatible {
		return errors.New("generation changed during transaction validation")
	}
	if !a.AllowedLeaf(currentSnapshot.LiveSave, currentSnapshot) {
		unlock()
		pauseErr := a.Pause(context.WithoutCancel(ctx))
		return fmt.Errorf("policy leaf conflict during validation; automatic mode paused: %v", pauseErr)
	}
	oldLines, _ := chainLines(before, chain)
	currentLines, _ := chainLines(currentSnapshot.LiveSave, chain)
	if ruleHash(oldLines) != ruleHash(currentLines) {
		return errors.New("policy leaf changed during transaction validation")
	}
	before = currentSnapshot.LiveSave
	currentUCI, err := a.uci(ctx, "mwan3_autobalancer")
	if err != nil {
		return err
	}
	current, err := ParseConfig(currentUCI)
	if err != nil {
		return err
	}
	manual := len(explicit) > 0 && explicit[0]
	if !reflect.DeepEqual(current, s.Config) || (!manual && (!current.Enabled || current.Mode != "automatic")) {
		return errors.New("current apply opt-in/config changed; no transaction")
	}
	if err = a.Recovery.Arm(s.Config.Policy, ruleHash(rules)); err != nil {
		return err
	}
	// Every failure after this point may have committed and must actively recover independently.
	if _, err = a.Runner.Run(ctx, []string{"iptables-restore", "--noflush"}, restore); err != nil {
		unlock()
		return a.RecoverFailedApply(s.Config.Policy, fmt.Errorf("possibly committed transaction failed: %w", err))
	}
	after, err := a.Runner.Run(ctx, []string{"iptables-save", "-t", "mangle"}, "")
	if err != nil {
		unlock()
		return a.RecoverFailedApply(s.Config.Policy, fmt.Errorf("post-commit inspection failed: %w", err))
	}
	got, _ := chainLines(after, "mwan3_policy_"+s.Config.Policy)
	if !equivalentRules(got, rules) || otherRules(before, "mwan3_policy_"+s.Config.Policy) != otherRules(after, "mwan3_policy_"+s.Config.Policy) {
		unlock()
		return a.RecoverFailedApply(s.Config.Policy, errors.New("post-commit policy verification failed"))
	}
	// Store the inspected kernel representation, including probability quantization, for conflict checks.
	if err = a.Recovery.RecordChain(ruleHash(got)); err != nil {
		unlock()
		return a.RecoverFailedApply(s.Config.Policy, fmt.Errorf("verified chain ownership could not be recorded: %w", err))
	}
	return nil
}
func equivalentRules(a, b []string) bool {
	normalize := func(x []string) string {
		r := []string{}
		for _, line := range x {
			tokens, err := leafTokens(line)
			if err != nil {
				return "invalid leaf tokens"
			}
			for i := 0; i+1 < len(tokens); i++ {
				if tokens[i] == "--probability" {
					f, err := strconv.ParseFloat(tokens[i+1], 64)
					if err == nil {
						tokens[i+1] = fmt.Sprintf("%.8f", f)
					}
				}
			}
			r = append(r, strings.Join(tokens, "\x00"))
		}
		return strings.Join(r, "\n")
	}
	return normalize(a) == normalize(b)
}
func otherRules(save, chain string) string {
	out := []string{}
	for _, l := range strings.Split(save, "\n") {
		if strings.HasPrefix(l, "#") || strings.HasPrefix(l, "-A "+chain+" ") || strings.HasPrefix(l, ":"+chain+" ") {
			continue
		}
		if strings.HasPrefix(l, ":") {
			if i := strings.LastIndex(l, " ["); i >= 0 {
				l = l[:i]
			}
		}
		out = append(out, l)
	}
	return strings.Join(out, "\n")
}

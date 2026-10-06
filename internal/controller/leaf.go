package controller

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"math"
	"path/filepath"
	"strconv"
	"strings"
)

func ruleHash(lines []string) string {
	b := sha256.Sum256([]byte(strings.Join(lines, "\n")))
	return hex.EncodeToString(b[:])
}

type leafRule struct {
	iface         string
	weight, total int
	mark, mask    uint32
	probability   float64
	statistic     bool
	comment       bool
	outDevice     string
	out           bool
}

// Only this closed grammar is a native leaf: no jumps, extra matches, negations or foreign options.
func leafTokens(line string) ([]string, error) {
	tokens := []string{}
	var token strings.Builder
	quoted, escaped, started := false, false, false
	for _, r := range line {
		if escaped {
			token.WriteRune(r)
			escaped = false
			started = true
			continue
		}
		if r == '\\' {
			escaped = true
			continue
		}
		if r == '"' {
			quoted = !quoted
			started = true
			continue
		}
		if !quoted && (r == ' ' || r == '\t') {
			if started {
				tokens = append(tokens, token.String())
				token.Reset()
				started = false
			}
			continue
		}
		token.WriteRune(r)
		started = true
	}
	if quoted || escaped {
		return nil, errors.New("unterminated leaf token")
	}
	if started {
		tokens = append(tokens, token.String())
	}
	return tokens, nil
}
func parseLeafRule(line, chain string, mask uint32) (leafRule, error) {
	r := leafRule{probability: 1}
	tokens, err := leafTokens(line)
	if err != nil {
		return r, err
	}
	if len(tokens) < 3 || tokens[0] != "-A" || tokens[1] != chain {
		return r, errors.New("invalid leaf prefix")
	}
	markMatch, markTarget := false, false
	seen := map[string]bool{}
	for i := 2; i < len(tokens); {
		switch tokens[i] {
		case "-m":
			if i+1 >= len(tokens) {
				return r, errors.New("missing match")
			}
			module := tokens[i+1]
			if seen[module] {
				return r, errors.New("duplicate match")
			}
			seen[module] = true
			switch module {
			case "mark":
				if i+3 >= len(tokens) || tokens[i+2] != "--mark" {
					return r, errors.New("invalid mark match")
				}
				m, k, err := parseMark(tokens[i+3])
				if err != nil || m != 0 || k != mask {
					return r, errors.New("mark match must be zero/current mask")
				}
				markMatch = true
				i += 4
			case "statistic":
				if i+5 >= len(tokens) || tokens[i+2] != "--mode" || tokens[i+3] != "random" || tokens[i+4] != "--probability" {
					return r, errors.New("invalid statistic match")
				}
				f, err := strconv.ParseFloat(tokens[i+5], 64)
				if err != nil || math.IsNaN(f) || math.IsInf(f, 0) || f < 0 || f > 1 {
					return r, errors.New("invalid probability")
				}
				r.statistic = true
				r.probability = f
				i += 6
			case "comment":
				if i+3 >= len(tokens) || tokens[i+2] != "--comment" {
					return r, errors.New("invalid native comment")
				}
				words := strings.Fields(tokens[i+3])
				if len(words) != 3 {
					return r, errors.New("invalid native comment fields")
				}
				r.comment = true
				if words[0] == "out" {
					if !identifier(words[1]) || !deviceName(words[2]) || r.outDevice != words[2] {
						return r, errors.New("invalid offline output comment")
					}
					r.out = true
					r.iface = words[1]
				} else {
					if !identifier(words[0]) {
						return r, errors.New("invalid comment interface")
					}
					r.iface = words[0]
					r.weight, err = strconv.Atoi(words[1])
					if err != nil {
						return r, err
					}
					r.total, err = strconv.Atoi(words[2])
					if err != nil {
						return r, err
					}
				}
				i += 4
			default:
				return r, errors.New("foreign leaf match")
			}
		case "-o":
			if r.outDevice != "" || i+1 >= len(tokens) || !deviceName(tokens[i+1]) {
				return r, errors.New("invalid output device")
			}
			r.outDevice = tokens[i+1]
			i += 2
		case "-j":
			if markTarget || i+3 >= len(tokens) || tokens[i+1] != "MARK" || tokens[i+2] != "--set-xmark" {
				return r, errors.New("foreign leaf target")
			}
			r.mark, r.mask, err = parseMark(tokens[i+3])
			if err != nil || r.mask != mask {
				return r, errors.New("wrong mark mask")
			}
			markTarget = true
			i += 4
		default:
			return r, fmt.Errorf("foreign leaf token %q", tokens[i])
		}
	}
	if !markMatch || !markTarget {
		return r, errors.New("missing native mark match or target")
	}
	return r, nil
}
func parseMark(s string) (uint32, uint32, error) {
	parts := strings.Split(s, "/")
	if len(parts) != 2 {
		return 0, 0, errors.New("mark requires mask")
	}
	m, err := strconv.ParseUint(parts[0], 0, 32)
	if err != nil {
		return 0, 0, err
	}
	mask, err := strconv.ParseUint(parts[1], 0, 32)
	return uint32(m), uint32(mask), err
}
func NativeBaseline(save string, s Snapshot) bool {
	chain := "mwan3_policy_" + s.Config.Policy
	lines, exists := chainLines(save, chain)
	if !exists {
		return false
	}
	metric := int(^uint(0) >> 1)
	for _, c := range s.Channels {
		if c.Online && c.Enabled && c.Metric < metric {
			metric = c.Metric
		}
	}
	active := []Channel{}
	remaining := 0
	offline := map[string]Channel{}
	for _, c := range s.Channels {
		if c.Online && c.Enabled && c.Metric == metric {
			active = append(active, c)
			remaining += c.BaselineWeight
		}
		if !c.Online && c.Device != "" {
			offline[c.Interface] = c
		}
	}
	core := []leafRule{}
	for _, line := range lines {
		r, err := parseLeafRule(line, chain, s.Mask)
		if err != nil {
			return false
		}
		if r.out {
			c, ok := offline[r.iface]
			if !ok || len(core) > 0 || r.statistic || r.outDevice != c.Device || r.mark != s.Mask {
				return false
			}
			delete(offline, r.iface)
			continue
		}
		if r.outDevice != "" {
			return false
		}
		core = append(core, r)
	}
	if len(active) == 0 {
		if len(core) != 1 {
			return false
		}
		r := core[0]
		maxID := s.Mask >> s.Shift
		switch s.LastResort {
		case "blackhole":
			maxID -= 2
		case "unreachable":
			maxID--
		}
		return !r.comment && !r.statistic && r.mark == maxID<<s.Shift
	}
	if len(core) != len(active) {
		return false
	}
	for i, r := range core {
		c := active[len(active)-1-i]
		if !r.comment || r.iface != c.Interface || r.weight != c.BaselineWeight || r.total != remaining || r.mark != uint32(c.ID)<<s.Shift {
			return false
		}
		want := float64(c.BaselineWeight) / float64(remaining)
		if !r.statistic && want != 1 {
			return false
		}
		stock := float64(c.BaselineWeight*1000/remaining) / 1000
		if math.Abs(r.probability-stock) > 1e-8 {
			return false
		}
		remaining -= c.BaselineWeight
	}
	return remaining == 0
}
func (a *Adapter) OwnedLeaf(save string, s Snapshot) bool {
	var lease Lease
	if ReadJSON(filepath.Join(a.Recovery.Dir, "lease.json"), &lease) != nil || lease.Policy != s.Config.Policy || lease.ChainHash == "" || lease.RestoreRequested {
		return false
	}
	lines, exists := chainLines(save, "mwan3_policy_"+s.Config.Policy)
	return exists && ruleHash(lines) == lease.ChainHash
}
func (a *Adapter) AllowedLeaf(save string, s Snapshot) bool {
	return a.OwnedLeaf(save, s) || NativeBaseline(save, s)
}

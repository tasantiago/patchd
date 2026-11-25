package api

import (
	"regexp"
	"strings"

	"github.com/tasantiago/patchd/internal/store"
)

const floorRegexPrefix = "regex:"

type floorRegexRule struct {
	Spec, Name string
	RE *regexp.Regexp
}

func compileFloorRegex(names map[store.FloorKey]string) map[string][]floorRegexRule {
	out := map[string][]floorRegexRule{}
	for k, name := range names {
		if !strings.HasPrefix(k.Spec, floorRegexPrefix) || name == "" { continue }
		re, err := regexp.Compile(strings.TrimPrefix(k.Spec, floorRegexPrefix))
		if err != nil || re.MatchString("") { continue }
		out[k.Building] = append(out[k.Building], floorRegexRule{Spec:k.Spec, Name:name, RE:re})
	}
	return out
}

// Use the primary valid IPv4 only: secondary VPN/virtual adapter IPs should
// not silently move a machine between floors.
func matchingFloorRegex(e equip, rules []floorRegexRule) []floorRegexRule {
	if !e.ip.Is4() || e.ip.IsLoopback() || e.ip.IsLinkLocalUnicast() { return nil }
	var out []floorRegexRule
	for _, rule := range rules {
		if rule.RE.MatchString(e.ip.String()) { out = append(out, rule) }
	}
	return out
}

func regexFloorName(matches []floorRegexRule) (string, bool) {
	name := ""
	for _, rule := range matches {
		if name != "" && name != rule.Name { return "", true }
		name = rule.Name
	}
	return name, false
}

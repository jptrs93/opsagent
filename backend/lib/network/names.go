package network

import (
	"strconv"
	"strings"
)

func DNSLabel(s string) string {
	s = strings.ToLower(strings.TrimSpace(s))
	s = strings.ReplaceAll(s, "_", "-")
	var b strings.Builder
	lastDash := false
	for _, r := range s {
		valid := (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9')
		if valid {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if r == '-' && !lastDash && b.Len() > 0 {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if out == "" {
		return "deployment"
	}
	return out
}

func SpaceDNSName(id int32) string {
	return "space-" + strconv.Itoa(int(id))
}

func DeploymentDNSName(name string, spaceID int32) string {
	return DNSLabel(name) + "." + SpaceDNSName(spaceID) + ".internal"
}

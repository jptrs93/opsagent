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

// hostVethName is the host-side end of a container's veth pair: od<deployment>s<slot>.
func hostVethName(deploymentID int32, slot int) string {
	return "od" + strconv.Itoa(int(deploymentID)) + "s" + strconv.Itoa(slot)
}

// IsHostVethName reports whether name follows the hostVethName scheme, so a
// teardown that does not know the live deployments can still find every
// workload veth on the host.
func IsHostVethName(name string) bool {
	rest, ok := strings.CutPrefix(name, "od")
	if !ok {
		return false
	}
	dep, slot, ok := strings.Cut(rest, "s")
	return ok && allDigits(dep) && allDigits(slot)
}

func allDigits(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if r < '0' || r > '9' {
			return false
		}
	}
	return true
}

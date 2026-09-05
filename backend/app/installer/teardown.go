package installer

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
)

// Portable pieces of the host teardown: the purge target list and the
// mountinfo parsing the Linux unmount step relies on. The parts that need the
// kernel (containerd, cgroups, netlink, nftables, umount) live in
// teardown_linux.go with no-op stubs in teardown_other.go.

func warn(format string, a ...any) { fmt.Printf("    warning: "+format+"\n", a...) }

// purgeTargets lists every directory --purge deletes: the roots this binary
// knows by name plus any /var/lib/opendeploy-* sibling the runtime derived
// that a newer or older agent might have created.
func purgeTargets() []string {
	known := []string{dataDir, assetCacheDir, releasesDir, volumesDir, buildLogsDir, runLogsDir, logArchiveDir, metricsDir, containerdRoot, configDir}
	siblings, _ := filepath.Glob(siblingDirGlob)
	return mergePurgeTargets(known, siblings)
}

func mergePurgeTargets(known, siblings []string) []string {
	seen := make(map[string]bool, len(known)+len(siblings))
	out := make([]string, 0, len(known)+len(siblings))
	add := func(dir string) {
		dir = filepath.Clean(dir)
		if seen[dir] {
			return
		}
		seen[dir] = true
		out = append(out, dir)
	}
	for _, dir := range known {
		add(dir)
	}
	sort.Strings(siblings)
	for _, dir := range siblings {
		add(dir)
	}
	return out
}

// strayProcessRoots are the executable locations a leftover opendeploy-owned
// process runs from: the agent release binary (and the helpers it execs from
// its own path: log consumers, the backup child) and the bundled runtime
// (containerd shims, runc).
func strayProcessRoots() []string {
	return []string{dataDir, releasesDir}
}

func pathUnder(path, root string) bool {
	path = filepath.Clean(path)
	root = filepath.Clean(root)
	return path == root || strings.HasPrefix(path, root+"/")
}

func pathUnderAny(path string, roots []string) bool {
	for _, root := range roots {
		if pathUnder(path, root) {
			return true
		}
	}
	return false
}

// mountPointsUnder parses /proc/self/mountinfo content and returns every mount
// point at or below root, deepest first so they can be unmounted in order.
func mountPointsUnder(mountinfo, root string) []string {
	var points []string
	for _, line := range strings.Split(mountinfo, "\n") {
		fields := strings.Fields(line)
		if len(fields) < 5 {
			continue
		}
		point := unescapeMountPath(fields[4])
		if pathUnder(point, root) {
			points = append(points, point)
		}
	}
	sort.Slice(points, func(i, j int) bool {
		di, dj := strings.Count(points[i], "/"), strings.Count(points[j], "/")
		if di != dj {
			return di > dj
		}
		return points[i] > points[j]
	})
	return points
}

// unescapeMountPath decodes the \ooo octal escapes mountinfo uses for spaces,
// tabs, newlines, and backslashes in paths.
func unescapeMountPath(s string) string {
	if !strings.Contains(s, `\`) {
		return s
	}
	var b strings.Builder
	for i := 0; i < len(s); i++ {
		if s[i] == '\\' && i+3 < len(s) && isOctal(s[i+1]) && isOctal(s[i+2]) && isOctal(s[i+3]) {
			b.WriteByte((s[i+1]-'0')<<6 | (s[i+2]-'0')<<3 | (s[i+3] - '0'))
			i += 3
			continue
		}
		b.WriteByte(s[i])
	}
	return b.String()
}

func isOctal(c byte) bool { return c >= '0' && c <= '7' }

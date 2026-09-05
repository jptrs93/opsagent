package installer

import (
	"reflect"
	"testing"
)

func TestMountPointsUnderOrdersDeepestFirstAndDecodesEscapes(t *testing.T) {
	mountinfo := `36 35 98:0 / / rw,relatime - ext4 /dev/root rw
40 36 0:20 / /run/opendeploy-containerd tmpfs rw - tmpfs tmpfs rw
41 40 0:21 / /run/opendeploy-containerd/io.containerd.runtime.v2.task/opendeploy/opendeploy-1-2-3-4/rootfs rw - overlay overlay rw
42 41 98:0 /home/x /run/opendeploy-containerd/io.containerd.runtime.v2.task/opendeploy/opendeploy-1-2-3-4/rootfs/data\040dir rw - ext4 /dev/root rw
43 36 0:22 / /run/opendeploy-containerd2 rw - tmpfs tmpfs rw
44 36 0:23 / /run/netns/opendeploy-1-2-3-4 rw - nsfs nsfs rw
`
	got := mountPointsUnder(mountinfo, "/run/opendeploy-containerd")
	want := []string{
		"/run/opendeploy-containerd/io.containerd.runtime.v2.task/opendeploy/opendeploy-1-2-3-4/rootfs/data dir",
		"/run/opendeploy-containerd/io.containerd.runtime.v2.task/opendeploy/opendeploy-1-2-3-4/rootfs",
		"/run/opendeploy-containerd",
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mountPointsUnder = %q, want %q", got, want)
	}
	if got := mountPointsUnder(mountinfo, "/var/lib/opendeploy-containerd"); len(got) != 0 {
		t.Fatalf("mountPointsUnder for an unmounted root = %q, want none", got)
	}
}

func TestUnescapeMountPath(t *testing.T) {
	cases := map[string]string{
		`/plain`:            "/plain",
		`/a\040b`:           "/a b",
		`/tab\011x`:         "/tab\tx",
		`/back\134slash`:    `/back\slash`,
		`/trailing\04`:      `/trailing\04`,
		`/not\x41octal`:     `/not\x41octal`,
		`/two\040\040space`: "/two  space",
	}
	for in, want := range cases {
		if got := unescapeMountPath(in); got != want {
			t.Errorf("unescapeMountPath(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestMergePurgeTargetsAddsUnknownSiblingsOnce(t *testing.T) {
	known := []string{"/var/lib/opendeploy", "/var/lib/opendeploy-assets", "/etc/opendeploy"}
	siblings := []string{"/var/lib/opendeploy-metrics", "/var/lib/opendeploy-assets", "/var/lib/opendeploy-future"}
	got := mergePurgeTargets(known, siblings)
	want := []string{"/var/lib/opendeploy", "/var/lib/opendeploy-assets", "/etc/opendeploy", "/var/lib/opendeploy-future", "/var/lib/opendeploy-metrics"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("mergePurgeTargets = %q, want %q", got, want)
	}
}

func TestPurgeTargetsCoverEveryRuntimeRoot(t *testing.T) {
	targets := mergePurgeTargets(purgeTargets(), nil)
	for _, dir := range []string{dataDir, assetCacheDir, releasesDir, volumesDir, buildLogsDir, runLogsDir, logArchiveDir, metricsDir, containerdRoot, configDir} {
		found := false
		for _, target := range targets {
			if target == dir {
				found = true
			}
		}
		if !found {
			t.Errorf("purge targets miss %s", dir)
		}
	}
}

func TestPathUnder(t *testing.T) {
	if !pathUnder("/var/lib/opendeploy/bin/opendeploy", "/var/lib/opendeploy") {
		t.Fatal("child path should be under root")
	}
	if !pathUnder("/var/lib/opendeploy", "/var/lib/opendeploy/") {
		t.Fatal("root itself should be under root")
	}
	if pathUnder("/var/lib/opendeploy-releases/x", "/var/lib/opendeploy") {
		t.Fatal("sibling with a shared prefix must not count as under root")
	}
	if !pathUnderAny("/var/lib/opendeploy-releases/jptrs93/opsagent/v1/opendeploy-linux-amd64", strayProcessRoots()) {
		t.Fatal("release binary should be a stray process root")
	}
}

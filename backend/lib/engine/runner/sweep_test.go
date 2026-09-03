package runner

import "testing"

func TestParseContainerID(t *testing.T) {
	dep, version, instance, run, ok := parseContainerID("opendeploy-7-3-42-5")
	if !ok || dep != 7 || version != 3 || instance != 42 || run != 5 {
		t.Fatalf("parse = %d %d %d %d %v", dep, version, instance, run, ok)
	}
	for _, id := range []string{
		"opendeploy-7-v3",
		"opendeploy-7-3-42",
		"opendeploy-7-3-42-5-1",
		"opendeploy-7-3-42-0",
		"opendeploy-7-3-x-5",
		"other-7-3-42-5",
		"",
	} {
		if _, _, _, _, ok := parseContainerID(id); ok {
			t.Fatalf("%q parsed as a container id", id)
		}
	}
}

func TestForeignContainer(t *testing.T) {
	known := map[placement]struct{}{{deploymentID: 7, instanceID: 42}: {}}
	cases := map[string]bool{
		"opendeploy-7-3-42-5":  false,
		"opendeploy-7-9-42-1":  false,
		"opendeploy-7-3-43-5":  true,
		"opendeploy-8-3-42-5":  true,
		"opendeploy-7-v3":      true,
		"opendeploy-7-3-42-5x": true,
	}
	for id, want := range cases {
		if got := foreignContainer(id, known); got != want {
			t.Fatalf("foreign(%s) = %v, want %v", id, got, want)
		}
	}
}

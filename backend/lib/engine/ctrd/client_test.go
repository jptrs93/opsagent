package ctrd

import "testing"

func TestVersionAtLeast(t *testing.T) {
	cases := []struct {
		version string
		want    bool
	}{
		{"v2.3.5", true},
		{"2.3.0", true},
		{"v2.4.0-beta.0", true},
		{"v3.0.0", true},
		{"v2.2.8", false},
		{"v2.0.5", false},
		{"v1.7.35", false},
		{"", false},
		{"garbage", false},
		{"v2.3.5+unknown", true},
	}
	for _, tc := range cases {
		if got := versionAtLeast(tc.version, 2, 3); got != tc.want {
			t.Errorf("versionAtLeast(%q, 2, 3) = %v, want %v", tc.version, got, tc.want)
		}
	}
}

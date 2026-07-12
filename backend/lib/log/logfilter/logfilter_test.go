package logfilter

import "testing"

func TestMatchSearchStr(t *testing.T) {
	if !Match([]byte("INFO deployed version abc"), "deployed", "") {
		t.Fatal("expected matching substring to pass")
	}
	if Match([]byte("INFO deployed version abc"), "Deployed", "") {
		t.Fatal("expected search string to be case-sensitive")
	}
}

func TestMatchMinLevel(t *testing.T) {
	cases := []struct {
		name string
		line string
		min  string
		want bool
	}{
		{name: "warn includes warn", line: "WARN disk nearly full", min: "WARN", want: true},
		{name: "warn includes error", line: "ERROR disk full", min: "WARN", want: true},
		{name: "warn excludes info", line: "INFO disk ok", min: "WARN", want: false},
		{name: "debug includes info", line: "INFO disk ok", min: "DEBUG", want: true},
		{name: "error excludes warn", line: "WARN disk nearly full", min: "ERROR", want: false},
		{name: "unknown min ignored", line: "plain line", min: "TRACE", want: true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(subtest *testing.T) {
			if got := Match([]byte(tc.line), "", tc.min); got != tc.want {
				subtest.Fatalf("Match() = %v, want %v", got, tc.want)
			}
		})
	}
}

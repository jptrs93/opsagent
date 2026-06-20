package logconsumer

import (
	"strings"
	"testing"
	"time"
)

func TestProcessJSONLinesTransformsJSONToLogfmt(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	input := `{"time":"2026-06-15T14:30:01Z","level":"INFO","msg":"ready","service":"api","count":3,"ok":true,"meta":{"host":"a","attempt":2},"tags":["x"]}` + "\n"
	lines := processJSONLinesForTest(t, input, now)

	want := `time=2026-06-15T14:30:01Z level=INFO msg=ready count=3 meta.attempt=2 meta.host=a ok=true service=api tags="[\"x\"]"` + "\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func TestProcessJSONLinesDefaultsMissingTimeAndLevel(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processJSONLinesForTest(t, `{"msg":"no metadata","event":"start"}`+"\n", now)

	want := `time=2026-06-15T14:30:00Z level=WARN msg="no metadata" event=start` + "\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func TestProcessJSONLinesHandlesInvalidJSONAsUnformatted(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processJSONLinesForTest(t, `{"msg":`+"\n", now)

	want := `time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg="{\"msg\":"` + "\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func TestProcessJSONLinesRejectsMalformedJSONAcceptedByParser(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processJSONLinesForTest(t, `{"msg" "missing colon"}`+"\n", now)

	want := `time=2026-06-15T14:30:00Z level=ERROR fmt=unformatted msg="{\"msg\" \"missing colon\"}"` + "\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func TestProcessJSONLinesUnescapesJSONStringValues(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processJSONLinesForTest(t, `{"time":"t","level":"INFO","msg":"hello\n\"world\""}`+"\n", now)

	want := `time=t level=INFO msg="hello\n\"world\""` + "\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func TestProcessJSONLinesOnlyFlattensOneLevel(t *testing.T) {
	now := time.Date(2026, 6, 15, 14, 30, 0, 0, time.UTC)
	lines := processJSONLinesForTest(t, `{"time":"t","level":"INFO","msg":"m","outer":{"inner":{"deep":"v"}}}`+"\n", now)

	want := `time=t level=INFO msg=m outer.inner="{\"deep\":\"v\"}"` + "\n"
	if len(lines) != 1 || string(lines[0]) != want {
		t.Fatalf("lines = %#v, want one line %q", lines, want)
	}
}

func processJSONLinesForTest(t *testing.T, input string, now time.Time) [][]byte {
	t.Helper()
	out := make(chan []byte, 10)
	if err := processJSONLinesWithClock(strings.NewReader(input), out, func() time.Time { return now }); err != nil {
		t.Fatal(err)
	}
	close(out)
	var lines [][]byte
	for line := range out {
		lines = append(lines, line)
	}
	return lines
}

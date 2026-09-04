package logmanager

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

// lineViewSeeds are shapes the scanner must either answer exactly like
// parseLine or decline. They double as the fuzz corpus.
var lineViewSeeds = []string{
	``,
	`   `,
	`plain text line`,
	`  plain with spaces  ` + "\n",
	`{}`,
	`{ }`,
	`{"level":"INFO","msg":"started"}`,
	`{"time":"2026-06-15T14:30:01Z","level":"INFO","msg":"started"}` + "\n",
	`  {"level":"warn"}  ` + "\n",
	`{"msg":"no level here"}`,
	`{"level":30,"msg":"numeric level"}`,
	`{"level":true}`,
	`{"level":null}`,
	`{"level":-1.5e3,"msg":"exp"}`,
	`{"level":"warning","message":"alt msg key"}`,
	`{"level":"ERROR","msg":"has msg","message":"and message"}`,
	`{"level":" error  "}`,
	`{"level":"ERROR","broken`,
	`{"level":"INFO",}`,
	`{"level":"INFO" "msg":"x"}`,
	`{"level":"INFO"} trailing garbage`,
	`{"level":"INFO"}}`,
	`{"level":"INFO"}]`,
	`{"level":"a\"b","msg":"line\nbreak\ttab\\slash\/"}`,
	`{"level":"ERROR"}`,
	`{"lev\u0065l":"ERROR"}`,
	`{"level":"😀"}`,
	`{"level":"\ud83d"}`,
	`{"level":"\ude00"}`,
	`{"level":"\ud83dA"}`,
	`{"level":"\uZZZZ"}`,
	`{"level":"\x"}`,
	`{"level":"É"}`,
	"{\"level\":\"\xff\xfe\"}",
	"{\"\xff\":\"ERROR\"}",
	"{\"k\xc3\xa9y\":\"ERROR\",\"level\":\"WARN\"}",
	`{"level":"ctl` + "\x01" + `"}`,
	`{"level":{"nested":"ERROR"}}`,
	`{"level":["ERROR"]}`,
	`{"a":{"level":"ERROR"},"level":"INFO"}`,
	`{"a":[1,2,{"b":[true,false,null]}],"level":"WARN","msg":"deep"}`,
	`{"a":[],"b":{},"c":[[]],"level":"DEBUG"}`,
	`{"n":01,"level":"INFO"}`,
	`{"n":1.,"level":"INFO"}`,
	`{"n":-,"level":"INFO"}`,
	`{"n":+1,"level":"INFO"}`,
	`{"n":1e,"level":"INFO"}`,
	`{"n":1x,"level":"INFO"}`,
	`{"n":truex,"level":"INFO"}`,
	`{"n":tru,"level":"INFO"}`,
	`{"level":"INFO","level":"ERROR"}`,
	`{"_tags":"[\"Secondary\",\"ClusterSession\"]","level":"INFO","msg":"tags as string"}`,
	`{"_tags":["Secondary","ClusterSession"],"level":"INFO"}`,
	`{"field":"Value With CASE","level":"INFO"}`,
	`{"field":"Straße","level":"INFO"}`,
	`{"level":"INFO","msg":"Kelvin K sign"}`,
	"{\n\t\"level\" : \"INFO\" ,\r\n \"msg\" : \"ws\" \n}\n",
	`{"":"empty key","level":"INFO"}`,
	`{"level":""}`,
	`["level","ERROR"]`,
	`"level"`,
	`{"level":"INFO"` + "\x00",
	`{"level":"INFO","msg":"` + strings.Repeat("x", 300) + `"}`,
}

func lineViewFields(t testing.TB, line string) []string {
	t.Helper()
	fields := []string{"level", "msg", "message", "", "_tags", "field", "n", "a", "missing", "\xff", ""}
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var obj map[string]any
		if json.Unmarshal([]byte(trimmed), &obj) == nil {
			for k := range obj {
				fields = append(fields, k)
			}
		}
	}
	return fields
}

// checkLineView compares every access path of a visitRec that goes through
// the view against a visitRec that only ever uses parseLine.
func checkLineView(t testing.TB, sc *lineScanner, line string) {
	t.Helper()
	raw := []byte(line)
	slow := visitRec{rec: apigen.RawLogLine{Line: raw}}
	slow.ensureParsed()

	fast := visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
	if got, want := fast.levelValue(), slow.level; got != want {
		t.Fatalf("level(%q) = %q, want %q", line, got, want)
	}
	fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
	if got, want := fast.msgValue(), slow.msg; got != want {
		t.Fatalf("msg(%q) = %q, want %q", line, got, want)
	}
	for _, field := range lineViewFields(t, line) {
		fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
		gotV, gotOK := fast.fieldValue(field)
		wantV, wantOK := slow.fieldValue(field)
		if gotV != wantV || gotOK != wantOK {
			t.Fatalf("fieldValue(%q, %q) = %q, %v; want %q, %v", line, field, gotV, gotOK, wantV, wantOK)
		}
		fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
		if b, ok, isFast := fast.fieldBytes(field); isFast {
			if ok != wantOK || (ok && string(b) != wantV) {
				t.Fatalf("fieldBytes(%q, %q) = %q, %v; want %q, %v", line, field, b, ok, wantV, wantOK)
			}
		}
	}
	for _, needle := range []string{"", "info", "ERR", "started", "x", "ß", "k"} {
		for _, field := range []string{"", "level", "field", "msg"} {
			f := compiledFilter{field: field, op: "contains", value: strings.ToLower(needle), valueBytes: []byte(strings.ToLower(needle)), asciiValue: isASCII(strings.ToLower(needle))}
			fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
			got := f.match(&fast)
			v, ok := slow.fieldValue(field)
			want := ok && strings.Contains(strings.ToLower(v), f.value)
			if got != want {
				t.Fatalf("contains(%q, field %q, needle %q) = %v, want %v", line, field, needle, got, want)
			}
		}
	}

	level, msg := sc.shred(raw)
	if level != slow.level || msg != slow.msg {
		t.Fatalf("shred(%q) = %q, %q; want %q, %q", line, level, msg, slow.level, slow.msg)
	}
	if got := sc.levelOf(raw); got != slow.level {
		t.Fatalf("levelOf(%q) = %q, want %q", line, got, slow.level)
	}
}

func TestLineViewMatchesParseLine(t *testing.T) {
	sc := &lineScanner{}
	for _, line := range lineViewSeeds {
		checkLineView(t, sc, line)
	}
}

func FuzzLineViewMatchesParseLine(f *testing.F) {
	for _, line := range lineViewSeeds {
		f.Add(line)
	}
	f.Fuzz(func(t *testing.T, line string) {
		checkLineView(t, &lineScanner{}, line)
	})
}

// TestLineViewFastPathCoverage pins which shapes the view answers itself, so
// a change that silently pushes common lines to parseLine shows up.
func TestLineViewFastPathCoverage(t *testing.T) {
	sc := &lineScanner{}
	cases := []struct {
		line   string
		isJSON bool
		fb     bool
	}{
		{`{"level":"INFO","msg":"started"}`, true, false},
		{`{"level":"a\"b","msg":"line\nbreak"}`, true, false},
		{`{"level":"É"}`, true, false},
		{`{"a":[1,{"b":null}],"level":"INFO"}`, true, false},
		{`plain text`, false, false},
		{``, false, false},
		{`{"level":"INFO","broken`, false, true},
		{`{"lev\u0065l":"ERROR"}`, false, true},
		{"{\"\xff\":\"ERROR\"}", false, true},
	}
	for _, c := range cases {
		v := sc.view([]byte(c.line))
		if v.isJSON != c.isJSON || v.fallback != c.fb {
			t.Fatalf("view(%q): isJSON=%v fallback=%v; want %v %v", c.line, v.isJSON, v.fallback, c.isJSON, c.fb)
		}
	}
	// nested values are declined at access time, not scan time
	v := sc.view([]byte(`{"level":{"x":1},"msg":"m"}`))
	if !v.isJSON || v.fallback {
		t.Fatalf("nested level line should scan: %+v", v)
	}
	if _, ok := sc.levelFrom(&v); ok {
		t.Fatal("nested level should be declined")
	}
	if m, ok := sc.msgFrom(&v); !ok || m != "m" {
		t.Fatalf("msg alongside nested level = %q, %v", m, ok)
	}
}

func TestLineViewLevelScanAllocates(t *testing.T) {
	sc := &lineScanner{}
	lines := [][]byte{
		[]byte(`{"time":"2026-06-15T14:30:01Z","level":"INFO","msg":"started","_tags":"[\"A\",\"B\"]"}`),
		[]byte(`{"time":"2026-06-15T14:30:02Z","level":"ERROR","msg":"boom \"quoted\"","err":"x"}`),
		[]byte(`plain text`),
	}
	f := compiledFilter{field: "level", op: "in", values: []string{"ERROR"}}
	for _, l := range lines {
		v := visitRec{rec: apigen.RawLogLine{Line: l}, sc: sc}
		f.match(&v)
	}
	allocs := testing.AllocsPerRun(200, func() {
		for _, l := range lines {
			v := visitRec{rec: apigen.RawLogLine{Line: l}, sc: sc}
			f.match(&v)
			v.levelValue()
		}
	})
	if allocs != 0 {
		t.Fatalf("level filter allocated %v per run, want 0", allocs)
	}
	cf := compiledFilter{field: "", op: "contains", value: "boom", valueBytes: []byte("boom"), asciiValue: true}
	allocs = testing.AllocsPerRun(200, func() {
		for _, l := range lines {
			v := visitRec{rec: apigen.RawLogLine{Line: l}, sc: sc}
			cf.match(&v)
		}
	})
	if allocs != 0 {
		t.Fatalf("msg contains filter allocated %v per run, want 0", allocs)
	}
}

package logmanager

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/jptrs93/opsagent/backend/apigen"
)

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
	`{"level":null,"level":"INFO"}`,
	`{"level":"INFO","level":null}`,
	`{"msg":null,"message":"fallback"}`,
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
	`{"level":"ERROR"}`,
	`{"kéy":"ERROR","level":"WARN"}`,
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
	`{"n":2,"f":2.0,"e":1e3,"neg":-7,"big":9223372036854775807,"huge":9223372036854775808,"zero":-0}`,
	`{"a":{"b":{"c":1},"d":"x"},"a.b.c":2}`,
	`{"a.b.c":2,"a":{"b":{"c":1},"d":"x"}}`,
	`{"a":{"b":1},"a":{"c":2}}`,
	`{"a":{"b":1},"a":null}`,
	`{"a":null,"a":{"b":1}}`,
	`{"o":{"level":"nested","msg":"nested msg"}}`,
	`{"d1":{"d2":{"d3":{"d4":{"d5":{"d6":{"d7":{"d8":{"d9":{"d10":1}}}}}}}}}}`,
	`{"` + strings.Repeat("k", 300) + `":1,"ok":2}`,
	`{"p":{"` + strings.Repeat("k", 250) + `":{"x":1}},"ok":2}`,
	`{"arr":[1, 2 ,3],"obj":{"x":[{"y":null}]}}`,
	`{"t":true,"f":false,"s":"true","n":null}`,
	`{"e\"q":{"a\\b":1}}`,
	`{"a":{"":1},"":{"b":2}}`,
}

func lineViewFields(t testing.TB, line string, fields []shredField) []string {
	t.Helper()
	out := []string{"level", "msg", "message", "", "_tags", "field", "n", "a", "a.b", "a.b.c", "missing", "\xff", "o.level"}
	trimmed := strings.TrimSpace(line)
	if len(trimmed) > 0 && trimmed[0] == '{' {
		var obj map[string]any
		if json.Unmarshal([]byte(trimmed), &obj) == nil {
			for k := range obj {
				out = append(out, k)
			}
		}
	}
	for i := range fields {
		out = append(out, string(fields[i].key))
	}
	return out
}

func sameValue(a, b fieldValue) bool {
	if a.typ != b.typ {
		return false
	}
	switch a.typ {
	case typeInt:
		return a.i == b.i && string(a.text) == string(b.text)
	case typeFloat:
		return (a.f == b.f || (a.f != a.f && b.f != b.f)) && string(a.text) == string(b.text)
	case typeBool:
		return a.b == b.b
	default:
		return string(a.text) == string(b.text)
	}
}

func checkLineView(t testing.TB, sc *lineScanner, line string) {
	t.Helper()
	raw := []byte(line)
	slowLevel, slowMsg, slowFields := parseLine(raw)
	slow := visitRec{rec: apigen.RawLogLine{Line: raw}}
	slow.ensureParsed()

	fast := visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
	if got := fast.levelValue(); got != slowLevel {
		t.Fatalf("level(%q) = %q, want %q", line, got, slowLevel)
	}
	fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
	if got := fast.msgValue(); got != slowMsg {
		t.Fatalf("msg(%q) = %q, want %q", line, got, slowMsg)
	}
	for _, field := range lineViewFields(t, line, slowFields) {
		fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
		gotV, gotOK := fast.typedField(field)
		wantV, wantOK := slow.typedField(field)
		if gotOK != wantOK || (gotOK && !sameValue(gotV, wantV)) {
			t.Fatalf("typedField(%q, %q) = %+v, %v; want %+v, %v", line, field, gotV, gotOK, wantV, wantOK)
		}
	}
	for _, needle := range []string{"", "info", "ERR", "started", "x", "ß", "k", "2"} {
		for _, field := range []string{"", "level", "field", "msg", "n", "a.b.c"} {
			fs, err := compileFilters([]*apigen.LogFilter{{Field: field, Op: "contains", Value: needle}})
			if err != nil {
				t.Fatal(err)
			}
			fast = visitRec{rec: apigen.RawLogLine{Line: raw}, sc: sc}
			got := fs[0].match(&fast)
			v, ok := slow.typedField(field)
			want := ok && strings.Contains(strings.ToLower(v.String()), strings.ToLower(needle))
			if got != want {
				t.Fatalf("contains(%q, field %q, needle %q) = %v, want %v", line, field, needle, got, want)
			}
		}
	}

	level, msg, fields := sc.shred(raw)
	if level != slowLevel || msg != slowMsg {
		t.Fatalf("shred(%q) = %q, %q; want %q, %q", line, level, msg, slowLevel, slowMsg)
	}
	if len(fields) != len(slowFields) {
		t.Fatalf("shred(%q) fields = %s, want %s", line, describeFields(fields), describeFields(slowFields))
	}
	for i := range fields {
		if string(fields[i].key) != string(slowFields[i].key) || !sameValue(fields[i].val, slowFields[i].val) {
			t.Fatalf("shred(%q) fields = %s, want %s", line, describeFields(fields), describeFields(slowFields))
		}
	}
	if got := sc.levelOf(raw); got != slowLevel {
		t.Fatalf("levelOf(%q) = %q, want %q", line, got, slowLevel)
	}
}

func describeFields(fields []shredField) string {
	var b strings.Builder
	b.WriteString("[")
	for i := range fields {
		if i > 0 {
			b.WriteString(" ")
		}
		b.WriteString(string(fields[i].key))
		b.WriteString("=")
		b.WriteString(fieldTypeSuffix[fields[i].val.typ])
		b.WriteString(":")
		b.WriteString(fields[i].val.String())
	}
	b.WriteString("]")
	return b.String()
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
		{`{"level":"ERROR"}`, true, false},
		{"{\"\xff\":\"ERROR\"}", true, false},
		{`{"a":{"b":{"c":1}}}`, true, false},
		{`plain text`, false, false},
		{``, false, false},
		{`{"level":"INFO","broken`, false, true},
	}
	for _, c := range cases {
		v := sc.view([]byte(c.line))
		if v.isJSON != c.isJSON || v.fallback != c.fb {
			t.Fatalf("view(%q): isJSON=%v fallback=%v; want %v %v", c.line, v.isJSON, v.fallback, c.isJSON, c.fb)
		}
	}
	v := sc.view([]byte(`{"level":{"x":1},"msg":"m"}`))
	if lvl, ok := sc.levelFrom(&v); !ok || lvl != `{"X":1}` {
		t.Fatalf("object level = %q, %v", lvl, ok)
	}
	if m, ok := sc.msgFrom(&v); !ok || m != "m" {
		t.Fatalf("msg alongside object level = %q, %v", m, ok)
	}
}

func TestLineViewLevelScanAllocates(t *testing.T) {
	sc := &lineScanner{}
	lines := [][]byte{
		[]byte(`{"time":"2026-06-15T14:30:01Z","level":"INFO","msg":"started","_tags":"[\"A\",\"B\"]","n":{"a":1}}`),
		[]byte(`{"time":"2026-06-15T14:30:02Z","level":"ERROR","msg":"boom \"quoted\"","err":"x"}`),
		[]byte(`plain text`),
	}
	fs, err := compileFilters([]*apigen.LogFilter{{Field: "level", Op: "in", Values: []string{"ERROR"}}})
	if err != nil {
		t.Fatal(err)
	}
	f := fs[0]
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
	cfs, err := compileFilters([]*apigen.LogFilter{{Op: "contains", Value: "boom"}, {Field: "n.a", Op: "eq", Value: "1"}})
	if err != nil {
		t.Fatal(err)
	}
	allocs = testing.AllocsPerRun(200, func() {
		for _, l := range lines {
			v := visitRec{rec: apigen.RawLogLine{Line: l}, sc: sc}
			cfs[0].match(&v)
			cfs[1].match(&v)
		}
	})
	if allocs != 0 {
		t.Fatalf("msg contains and field eq filters allocated %v per run, want 0", allocs)
	}
}

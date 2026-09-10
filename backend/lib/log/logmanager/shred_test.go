package logmanager

import (
	"strings"
	"testing"
)

func fieldsOf(t *testing.T, line string) map[string]fieldValue {
	t.Helper()
	_, _, fields := parseLine([]byte(line))
	out := map[string]fieldValue{}
	for _, f := range fields {
		out[string(f.key)] = f.val
	}
	return out
}

func TestShredNumberRule(t *testing.T) {
	f := fieldsOf(t, `{"a":2,"b":2.0,"c":1e3,"d":-7,"e":9223372036854775807,"g":9223372036854775808,"h":-0,"i":"2"}`)
	check := func(key string, typ fieldType, text string) {
		t.Helper()
		v, ok := f[key]
		if !ok || v.typ != typ || v.String() != text {
			t.Fatalf("%s = %+v (%q), want type %d text %q", key, v, v.String(), typ, text)
		}
	}
	check("a", typeInt, "2")
	check("b", typeFloat, "2.0")
	check("c", typeFloat, "1e3")
	check("d", typeInt, "-7")
	check("e", typeInt, "9223372036854775807")
	check("g", typeFloat, "9223372036854775808")
	check("h", typeInt, "-0")
	check("i", typeStr, "2")
	if f["a"].i != 2 || f["b"].f != 2 || f["c"].f != 1000 || f["d"].i != -7 || f["g"].f != 9223372036854775808 {
		t.Fatalf("numeric values = %+v", f)
	}
}

func TestShredFlattenAndCaps(t *testing.T) {
	f := fieldsOf(t, `{"a":{"b":{"c":1},"d":"x"},"arr":[1, {"z":2}],"n":null,"t":true,"o":{"level":"nested"}}`)
	if v := f["a.b.c"]; v.typ != typeInt || v.i != 1 {
		t.Fatalf("a.b.c = %+v", v)
	}
	if v := f["a.d"]; v.typ != typeStr || v.String() != "x" {
		t.Fatalf("a.d = %+v", v)
	}
	if v := f["arr"]; v.typ != typeStr || v.String() != `[1, {"z":2}]` {
		t.Fatalf("arr = %+v", v)
	}
	if _, ok := f["n"]; ok {
		t.Fatal("null field should be absent")
	}
	if v := f["t"]; v.typ != typeBool || !v.b {
		t.Fatalf("t = %+v", v)
	}
	if v := f["o.level"]; v.typ != typeStr || v.String() != "nested" {
		t.Fatalf("o.level = %+v", v)
	}
	if _, ok := f["a"]; ok {
		t.Fatal("flattened object should not remain as a field")
	}

	deep := fieldsOf(t, `{"d1":{"d2":{"d3":{"d4":{"d5":{"d6":{"d7":{"d8":{"d9":{"d10":1}}}}}}}}}}`)
	if v, ok := deep["d1.d2.d3.d4.d5.d6.d7.d8"]; !ok || v.typ != typeStr || v.String() != `{"d9":{"d10":1}}` {
		t.Fatalf("depth cap = %+v", deep)
	}

	long := fieldsOf(t, `{"`+strings.Repeat("k", 300)+`":1,"ok":2}`)
	if len(long) != 1 || long["ok"].i != 2 {
		t.Fatalf("long key not dropped: %+v", long)
	}

	dup := fieldsOf(t, `{"a":{"b":1},"a.b":2,"c":1,"c":null}`)
	if v := dup["a.b"]; v.typ != typeInt || v.i != 2 {
		t.Fatalf("last wins = %+v", dup)
	}
	if _, ok := dup["c"]; ok {
		t.Fatal("later null should hide the earlier value")
	}
}

func TestShredLiftedKeys(t *testing.T) {
	level, msg, fields := parseLine([]byte(`{"level":"warn","msg":"hi","message":"alt","x":1}`))
	if level != "WARN" || msg != "hi" || len(fields) != 1 || string(fields[0].key) != "x" {
		t.Fatalf("lifted = %q %q %s", level, msg, describeFields(fields))
	}
	level, msg, _ = parseLine([]byte(`{"level":null,"msg":null,"message":"alt"}`))
	if level != "" || msg != "alt" {
		t.Fatalf("null lifted = %q %q", level, msg)
	}
	level, msg, _ = parseLine([]byte(`{"level":30,"msg":{"a":1}}`))
	if level != "30" || msg != `{"a":1}` {
		t.Fatalf("non-string lifted = %q %q", level, msg)
	}
}

func TestTallyPlanDense(t *testing.T) {
	tally := newKeyTally(maxTallyKeys)
	line := func(l string) {
		_, _, fields := parseLine([]byte(l))
		tally.add(fields)
	}
	line(`{"a":1,"b":"x","c":true}`)
	line(`{"a":"1","b":"y","c":false}`)
	line(`{"a":2,"b":"z"}`)
	line(`{"a":3}`)
	dense := tally.planDense(3)
	want := []keyVariant{{"a", typeInt, 3}, {"a", typeStr, 1}, {"b", typeStr, 3}}
	if len(dense) != len(want) {
		t.Fatalf("dense = %+v, want %+v", dense, want)
	}
	for i := range want {
		if dense[i] != want[i] {
			t.Fatalf("dense = %+v, want %+v", dense, want)
		}
	}
	dense = tally.planDense(2)
	if len(dense) != 2 || dense[0].key != "a" || dense[1].key != "a" {
		t.Fatalf("budget 2 should hold both variants of a: %+v", dense)
	}
	dense = tally.planDense(1)
	if len(dense) != 1 || dense[0].key != "b" {
		t.Fatalf("budget 1 should skip a (two variants) and admit b: %+v", dense)
	}
}

func TestTallyCap(t *testing.T) {
	old := maxTallyKeys
	maxTallyKeys = 2
	t.Cleanup(func() { maxTallyKeys = old })
	tally := newKeyTally(maxTallyKeys)
	_, _, fields := parseLine([]byte(`{"a":1,"b":2,"c":3}`))
	tally.add(fields)
	if len(tally.keys) != 2 || !tally.capped {
		t.Fatalf("tally = %d keys, capped %v", len(tally.keys), tally.capped)
	}
}

func TestLiteralSemantics(t *testing.T) {
	sc := &lineScanner{}
	cases := []struct {
		op    string
		lit   string
		text  bool
		val   fieldValue
		ok    bool
		match bool
	}{
		{"eq", "68", false, fieldValue{typ: typeInt, i: 68}, true, true},
		{"eq", "68", false, fieldValue{typ: typeFloat, f: 68}, true, true},
		{"eq", "68.0", false, fieldValue{typ: typeInt, i: 68}, true, true},
		{"eq", "68", false, stringValue([]byte("68")), true, true},
		{"eq", "68", true, fieldValue{typ: typeInt, i: 68}, true, false},
		{"eq", "68", true, stringValue([]byte("68")), true, true},
		{"eq", "true", false, boolValue(true), true, true},
		{"eq", "TRUE", false, boolValue(true), true, true},
		{"eq", "true", false, stringValue([]byte("true")), true, true},
		{"eq", "1", false, boolValue(true), true, false},
		{"eq", "b", false, stringValue([]byte(`["a","B"]`)), true, true},
		{"neq", "68", false, fieldValue{}, false, true},
		{"gt", "10", false, fieldValue{typ: typeInt, i: 11}, true, true},
		{"gt", "10", false, fieldValue{typ: typeFloat, f: 10}, true, false},
		{"gte", "10", false, fieldValue{typ: typeFloat, f: 10}, true, true},
		{"lt", "10", false, stringValue([]byte("5")), true, false},
		{"lte", "10", false, boolValue(false), true, false},
		{"contains", "6", false, fieldValue{typ: typeInt, i: 68}, true, true},
		{"contains", "ru", false, boolValue(true), true, true},
		{"contains", ".5", false, fieldValue{typ: typeFloat, f: 1.5}, true, true},
		{"in", "", false, fieldValue{}, false, true},
		{"exists", "", false, fieldValue{}, false, false},
		{"not_exists", "", false, fieldValue{}, false, true},
	}
	for _, c := range cases {
		f := compiledFilter{field: "x", op: c.op, lit: parseLiteral(c.lit, c.text)}
		if c.op == "in" {
			f.lits = []literal{parseLiteral(c.lit, c.text)}
		}
		if got := f.matchValue(c.val, c.ok, sc); got != c.match {
			t.Fatalf("%s %q (text=%v) on %+v ok=%v: got %v, want %v", c.op, c.lit, c.text, c.val, c.ok, got, c.match)
		}
	}
}

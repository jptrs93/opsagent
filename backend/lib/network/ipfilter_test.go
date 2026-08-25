package network

import (
	"net/netip"
	"testing"
)

func TestParseFilterEntry(t *testing.T) {
	valid := map[string]string{
		"203.0.113.7":     "203.0.113.7/32",
		" 203.0.113.7 ":   "203.0.113.7/32",
		"198.51.100.0/24": "198.51.100.0/24",
		"0.0.0.0/0":       "0.0.0.0/0",
		"2001:db8::1":     "2001:db8::1/128",
		"2001:DB8::/32":   "2001:db8::/32",
		"::/0":            "::/0",
		"198.51.100.0/32": "198.51.100.0/32",
	}
	for entry, want := range valid {
		prefix, err := ParseFilterEntry(entry)
		if err != nil {
			t.Fatalf("ParseFilterEntry(%q) failed: %v", entry, err)
		}
		if prefix.String() != want {
			t.Fatalf("ParseFilterEntry(%q) = %s, want %s", entry, prefix, want)
		}
	}
	invalid := []string{
		"",
		"office",
		"203.0.113.7/",
		"10.0.0.1/8",
		"2001:db8::1/32",
		"fe80::1%eth0",
		"::ffff:1.2.3.4",
		"::ffff:1.2.3.4/120",
		"203.0.113.7/33",
	}
	for _, entry := range invalid {
		if _, err := ParseFilterEntry(entry); err == nil {
			t.Fatalf("ParseFilterEntry(%q) succeeded, want error", entry)
		}
	}
}

func TestFilterEntryString(t *testing.T) {
	if got := FilterEntryString(netip.MustParsePrefix("203.0.113.7/32")); got != "203.0.113.7" {
		t.Fatalf("FilterEntryString = %q, want bare address", got)
	}
	if got := FilterEntryString(netip.MustParsePrefix("198.51.100.0/24")); got != "198.51.100.0/24" {
		t.Fatalf("FilterEntryString = %q, want CIDR", got)
	}
}

func TestSplitFilterPrefixes(t *testing.T) {
	v4, v6 := SplitFilterPrefixes([]string{"203.0.113.7", "bogus", "2001:db8::/32", "198.51.100.0/24"})
	if len(v4) != 2 || v4[0] != netip.MustParsePrefix("203.0.113.7/32") || v4[1] != netip.MustParsePrefix("198.51.100.0/24") {
		t.Fatalf("v4 = %v", v4)
	}
	if len(v6) != 1 || v6[0] != netip.MustParsePrefix("2001:db8::/32") {
		t.Fatalf("v6 = %v", v6)
	}
}

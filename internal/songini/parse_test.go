package songini

import "testing"

func TestParseBasic(t *testing.T) {
	f := Parse([]byte("[Song]\nname = Sample Song\nartist=Someone\nyear = 1994\n"))
	if v, _ := f.String("name"); v != "Sample Song" {
		t.Fatalf("name = %q", v)
	}
	if v, _ := f.String("artist"); v != "Someone" {
		t.Fatalf("artist = %q", v)
	}
	if y, ok := f.YearAsNumber(); !ok || y != 1994 {
		t.Fatalf("year = %d %v", y, ok)
	}
}

// Upstream stores modifiers with plain dictionary assignment, so the LAST
// occurrence of a key wins. Getting this backwards silently picks the wrong
// title for every chart that carries a duplicate.
func TestDuplicateKeyLastWins(t *testing.T) {
	f := Parse([]byte("[song]\nname = first\nname = second\n"))
	if v, _ := f.String("name"); v != "second" {
		t.Fatalf("name = %q, want %q", v, "second")
	}
	if len(f.Order) != 1 || f.Order[0] != "name" {
		t.Fatalf("Order = %v, want one entry", f.Order)
	}
}

func TestSectionHandling(t *testing.T) {
	// Keys outside [song] are ignored once any section has been seen.
	f := Parse([]byte("[other]\nname = wrong\n[Song]\nname = right\n"))
	if v, _ := f.String("name"); v != "right" {
		t.Fatalf("name = %q", v)
	}
	// Trailing junk after the bracket does not break the header.
	f = Parse([]byte("[Song] ; a comment\nname = ok\n"))
	if v, _ := f.String("name"); v != "ok" {
		t.Fatalf("name = %q", v)
	}
	// A file with no header at all is still read.
	f = Parse([]byte("name = headerless\n"))
	if v, _ := f.String("name"); v != "headerless" {
		t.Fatalf("name = %q", v)
	}
}

func TestMalformedLinesAreSkippedNotFatal(t *testing.T) {
	f := Parse([]byte("[song]\ngarbage line with no equals\n\n= novalue\nname = survives\n"))
	if v, _ := f.String("name"); v != "survives" {
		t.Fatalf("name = %q", v)
	}
	if len(f.Values) != 1 {
		t.Fatalf("Values = %v, want only the one good key", f.Values)
	}
}

func TestValueMayContainEquals(t *testing.T) {
	f := Parse([]byte("[song]\nlink_other = https://x.test/?a=1&b=2\n"))
	if v, _ := f.String("link_other"); v != "https://x.test/?a=1&b=2" {
		t.Fatalf("link_other = %q", v)
	}
}

func TestUnknownKeysArePreservedNotDropped(t *testing.T) {
	f := Parse([]byte("[song]\nname = x\nsome_future_key = y\n"))
	if v, _ := f.String("some_future_key"); v != "y" {
		t.Fatalf("unknown key was dropped")
	}
	if len(f.Unknown) != 1 || f.Unknown[0] != "some_future_key" {
		t.Fatalf("Unknown = %v", f.Unknown)
	}
}

func TestIntRejectsGarbageRatherThanReturningZero(t *testing.T) {
	f := Parse([]byte("[song]\nsong_length = banana\ndelay = 0\n"))
	if _, ok := f.Int("song_length"); ok {
		t.Fatal("garbage parsed as an integer")
	}
	v, ok := f.Int("delay")
	if !ok || v != 0 {
		t.Fatalf("delay = %d %v; a real zero must be distinguishable from absent", v, ok)
	}
}

func TestBoolLeniency(t *testing.T) {
	f := Parse([]byte("[song]\npro_drums = True\nmodchart = 0\nlyrics = yes\n"))
	for _, tc := range []struct {
		key  string
		want bool
	}{{"pro_drums", true}, {"modchart", false}, {"lyrics", true}} {
		got, ok := f.Bool(tc.key)
		if !ok || got != tc.want {
			t.Fatalf("%s = %v %v, want %v", tc.key, got, ok, tc.want)
		}
	}
}

func TestPreviewBothForms(t *testing.T) {
	f := Parse([]byte("[song]\npreview = 30000 45000\n"))
	s, e, ok := f.Preview()
	if !ok || s != 30000 || e != 45000 {
		t.Fatalf("preview = %d %d %v", s, e, ok)
	}
	f = Parse([]byte("[song]\npreview_start_time = 1000\npreview_end_time = 2000\n"))
	s, e, ok = f.Preview()
	if !ok || s != 1000 || e != 2000 {
		t.Fatalf("preview fallback = %d %d %v", s, e, ok)
	}
}

func TestYearIsFreeText(t *testing.T) {
	for _, tc := range []struct {
		raw  string
		want int
	}{{"1994", 1994}, {", 1994", 1994}, {"1994?", 1994}, {"circa 2003", 2003}} {
		f := Parse([]byte("[song]\nyear = " + tc.raw + "\n"))
		got, ok := f.YearAsNumber()
		if !ok || got != tc.want {
			t.Fatalf("year %q -> %d %v, want %d", tc.raw, got, ok, tc.want)
		}
		// The raw value must survive untouched for re-emit.
		if v, _ := f.String("year"); v != tc.raw {
			t.Fatalf("raw year mutated: %q", v)
		}
	}
}

func TestEncodings(t *testing.T) {
	// Latin-1 "Bjork" with an accented o - must not become U+FFFD.
	latin1 := append([]byte("[song]\nartist = Bj"), 0xF6)
	latin1 = append(latin1, []byte("rk\n")...)
	f := Parse(latin1)
	if v, _ := f.String("artist"); v != "Björk" {
		t.Fatalf("latin-1 artist = %q", v)
	}
	// UTF-8 with a BOM.
	f = Parse(append([]byte{0xEF, 0xBB, 0xBF}, []byte("[song]\nname = bom\n")...))
	if v, _ := f.String("name"); v != "bom" {
		t.Fatalf("utf-8 bom name = %q", v)
	}
	// UTF-16 LE with a BOM.
	le := []byte{0xFF, 0xFE}
	for _, r := range "[song]\nname = wide\n" {
		le = append(le, byte(r), 0x00)
	}
	f = Parse(le)
	if v, _ := f.String("name"); v != "wide" {
		t.Fatalf("utf-16 name = %q", v)
	}
}

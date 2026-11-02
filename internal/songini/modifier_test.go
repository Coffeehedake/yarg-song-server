package songini

import "testing"

// splitModifier reproduces YARGTextReader.ExtractModifierName followed by
// SkipWhitespaceAndEquals. Those two together are the whole of how YARG turns a song.ini line
// into a pair, and the reading they replaced - split on the first '=', trim both sides - got
// three separate things wrong.
//
// Each case below names which divergence it pins. The ones marked "unchanged" matter just as
// much: the overwhelming majority of real lines must come out EXACTLY as before, because the
// live library contains 3,476 song.ini files and not one of them exercises A, B or C. A change
// that fixed the three and moved anything else would be a regression dressed as a fix.
func TestSplitModifierMatchesExtractModifierName(t *testing.T) {
	for _, tc := range []struct {
		label    string
		line     string
		key, val string
		ok       bool
	}{
		// --- unchanged: ordinary lines, which is nearly every line in the wild ---
		{"plain pair", "name = Foo", "name", "Foo", true},
		{"no spaces", "name=Foo", "name", "Foo", true},
		{"tabs", "name\t=\tFoo", "name", "Foo", true},
		{"key is lowercased", "NAME = Foo", "name", "Foo", true},
		{"value keeps its case and inner spaces", "name = Foo  Bar", "name", "Foo  Bar", true},
		{"value may contain equals", "link = https://x.test/?a=1&b=2", "link", "https://x.test/?a=1&b=2", true},
		{"value trailing space trimmed", "name = Foo   ", "name", "Foo", true},
		{"leading whitespace on the line", "   name = Foo", "name", "Foo", true},
		{"empty value", "name =", "name", "", true},

		// --- A: the key ends at the first whitespace, NOT at the '=' ---
		// "album track = 5" is a typo for album_track. YARG sets the ALBUM to "track = 5",
		// which is what the player then sees. We used to store a key the game never reads.
		{"A: space in key", "album track = 5", "album", "track = 5", true},
		{"A: several words", "a b c = d", "a", "b c = d", true},

		// --- B: a line with no '=' at all is still a pair ---
		{"B: no equals", "genre Rock", "genre", "Rock", true},
		{"B: no equals, several words", "name The Long Song", "name", "The Long Song", true},

		// --- C: every '=' after the key is skipped, not kept in the value ---
		{"C: double equals", "name == Foo", "name", "Foo", true},
		{"C: equals with spaces between", "name = = Foo", "name", "Foo", true},

		// --- no key at all ---
		{"starts with equals", "= novalue", "", "", false},
		{"only equals", "===", "", "", false},
		{"blank", "   ", "", "", false},
		{"empty", "", "", "", false},

		// CR is 13, below the whitespace limit, so it ends the key like any other
		// whitespace - this is the behaviour corpus case 26-barecr-before-equals measured
		// against YARG's own songcache, not an inference.
		{"cr ends the key", "album\r = Second", "album", "Second", true},
		{"cr with no space", "song_length\r= 5", "song_length", "5", true},
	} {
		key, val, ok := splitModifier(tc.line)
		if ok != tc.ok || key != tc.key || val != tc.val {
			t.Errorf("%s: splitModifier(%q) = %q, %q, %v; want %q, %q, %v",
				tc.label, tc.line, key, val, ok, tc.key, tc.val, tc.ok)
		}
	}
}

// The three divergences are absent from the live library, which is the whole reason this was
// safe to change. That claim is a measurement and belongs in the docs, but the SHAPE of it can
// be pinned here: a well-formed "key = value" line must be read identically by both readings,
// so that the 3,476 files which contain only such lines cannot move.
func TestWellFormedLinesReadTheSameUnderBothRules(t *testing.T) {
	// The old rule, kept here only as the thing to agree with.
	oldSplit := func(line string) (string, string, bool) {
		for i := 0; i < len(line); i++ {
			if line[i] == '=' {
				k, v := line[:i], line[i+1:]
				for len(k) > 0 && (k[0] == ' ' || k[0] == '\t') {
					k = k[1:]
				}
				for len(k) > 0 && (k[len(k)-1] == ' ' || k[len(k)-1] == '\t') {
					k = k[:len(k)-1]
				}
				for len(v) > 0 && (v[0] == ' ' || v[0] == '\t') {
					v = v[1:]
				}
				for len(v) > 0 && (v[len(v)-1] == ' ' || v[len(v)-1] == '\t') {
					v = v[:len(v)-1]
				}
				if k == "" {
					return "", "", false
				}
				return lower(k), v, true
			}
		}
		return "", "", false
	}

	for _, line := range []string{
		"name = Foo Bar",
		"artist=Someone",
		"album\t=\tThe Album",
		"song_length = 1062206",
		"link_other = https://x.test/?a=1&b=2",
		"charter = A. Person",
		"  genre = Deathcore  ",
		"loading_phrase = It's a \"quoted\" thing, isn't it",
	} {
		ok1, ov, oo := oldSplit(line)
		nk, nv, no := splitModifier(line)
		if ok1 != nk || ov != nv || oo != no {
			t.Errorf("a well-formed line MOVED: %q\n  old: %q %q %v\n  new: %q %q %v",
				line, ok1, ov, oo, nk, nv, no)
		}
	}
}

func lower(s string) string {
	b := []byte(s)
	for i := range b {
		if b[i] >= 'A' && b[i] <= 'Z' {
			b[i] += 'a' - 'A'
		}
	}
	return string(b)
}

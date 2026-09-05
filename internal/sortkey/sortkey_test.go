package sortkey

import (
	"sort"
	"strings"
	"testing"
)

// The article list is what makes a browse-by-artist list recognisable. Every
// case here is a real shape a chart title takes.
func TestRemoveArticle(t *testing.T) {
	cases := []struct{ in, want string }{
		{"the beatles", "beatles"},
		{"el paso", "paso"},
		{"la bamba", "bamba"},
		{"le freak", "freak"},
		{"les miserables", "miserables"},
		{"los lobos", "lobos"},

		// The trailing space in every article is what stops these. Without it
		// a browse list files "Theatre" under "atre".
		{"theatre", "theatre"},
		{"lament", "lament"},
		{"election", "election"},
		{"lost", "lost"},

		// Only the first article goes, and only at the start.
		{"the the the", "the the"},
		{"a song about the sea", "a song about the sea"},

		// "a" and "an" are NOT in upstream's list. Reproduced rather than
		// improved: adding them would file songs where the client does not.
		{"a forest", "a forest"},
		{"an ending", "an ending"},

		{"", ""},
		{"the ", ""},
	}
	for _, c := range cases {
		if got := RemoveArticle(c.in); got != c.want {
			t.Errorf("RemoveArticle(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRemoveDiacritics(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Björk", "bjork"},
		{"Motörhead", "motorhead"},
		{"Beyoncé", "beyonce"},
		{"Sigur Rós", "sigur ros"},
		{"CAFÉ", "cafe"},

		// A combining sequence and its precomposed form must fold identically,
		// or the same artist sorts in two places depending on how the charter
		// typed the name.
		{"e\u0301", "e"}, // e + combining acute U+0301
		{"\u00e9", "e"},  // the precomposed U+00E9

		// Format characters go with the marks - upstream drops Cf too.
		{"a\u200db", "ab"}, // zero-width joiner

		// The uppercase-only substitution, reproduced deliberately. "Æ" is
		// expanded before normalisation; "æ" is not in the list, does not
		// decompose, and survives as a non-ASCII character.
		{"Ætherium", "aetherium"},
		{"ætherium", "ætherium"},
	}
	for _, c := range cases {
		if got := RemoveDiacritics(c.in); got != c.want {
			t.Errorf("RemoveDiacritics(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestRemoveUnwantedWhitespace(t *testing.T) {
	cases := []struct{ in, want string }{
		{"a b", "a b"},
		{"a  b", "a b"},
		{"a\t\t\tb", "a b"},
		{"a \t\n b", "a b"},
		{"   leading", "leading"},
		{"trailing   ", "trailing"},
		{"  both  ", "both"},
		{"   ", ""},
		{"", ""},
		{"a  b  c", "a b c"},

		// Multi-byte characters must survive a byte-level scan intact.
		{"  Björk   Guðmundsdóttir  ", "Björk Guðmundsdóttir"},
	}
	for _, c := range cases {
		if got := RemoveUnwantedWhitespace(c.in); got != c.want {
			t.Errorf("RemoveUnwantedWhitespace(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestStripRichTextTags(t *testing.T) {
	cases := []struct{ in, want string }{
		{"<color=#ff0000>Danger</color>", "Danger"},
		{"<b>Bold</b>", "Bold"},
		{"<size=200%>Big</size>", "Big"},
		{"a<br>b", "ab"},
		{"<font-weight=700>Heavy</font-weight>", "Heavy"},

		// Unrecognised angle brackets are text. "<3" is a heart and appears in
		// real chart titles; stripping it would move the song to another group.
		{"<3", "<3"},
		{"I <3 you", "I <3 you"},
		{"<notatag>x</notatag>", "<notatag>x</notatag>"},
		{"a < b", "a < b"},
		{"unterminated <b", "unterminated <b"},

		// Case-sensitive upstream, so case-sensitive here.
		{"<B>Bold</B>", "<B>Bold</B>"},

		{"no tags at all", "no tags at all"},
		{"", ""},
	}
	for _, c := range cases {
		if got := StripRichTextTags(c.in); got != c.want {
			t.Errorf("StripRichTextTags(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestGroupOf(t *testing.T) {
	cases := []struct {
		in   string
		want CharGroup
	}{
		{"", GroupEmpty},
		{"!bang", GroupASCIISymbol},
		{"(parens)", GroupASCIISymbol},
		{"9 crimes", GroupASCIINumber},
		{"abba", GroupASCIILetter},
		{"ætherium", GroupNonASCII},
		{"日本", GroupNonASCII},
	}
	for _, c := range cases {
		if got := GroupOf(c.in); got != c.want {
			t.Errorf("GroupOf(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

// The whole reason CompareStrings exists rather than a plain "<". If this test
// ever starts failing because someone simplified the comparison, the browse
// list has silently stopped agreeing with the client for astral characters.
func TestCompareStringsIsUTF16Ordinal(t *testing.T) {
	const emoji = "\U0001F600"  // surrogate pair D83D DE00 in UTF-16
	const privateUse = "\uE000" // a single code unit, above the surrogates

	if got := CompareStrings(emoji, privateUse); got >= 0 {
		t.Errorf("CompareStrings(emoji, U+E000) = %d, want negative: in UTF-16 the "+
			"high surrogate D83D sorts below E000", got)
	}
	if !(privateUse < emoji) {
		t.Fatal("premise of this test is gone: a Go string comparison no longer " +
			"disagrees with UTF-16 ordinal here")
	}

	// Ordinary cases must still behave.
	if CompareStrings("abc", "abd") >= 0 {
		t.Error("abc should sort before abd")
	}
	if CompareStrings("abc", "abc") != 0 {
		t.Error("equal strings should compare equal")
	}
	if CompareStrings("abcd", "abc") <= 0 {
		t.Error("a longer string with a common prefix sorts after")
	}
	if CompareStrings("", "a") >= 0 {
		t.Error("empty sorts first")
	}
}

// End to end: the ordering a browse list actually shows.
func TestKeyOrdering(t *testing.T) {
	in := []string{
		"Björk",
		"The Beatles",
		"blur",
		"9 Crimes",
		"!!!",
		"Los Lobos",
		"<color=#ff0000>Red</color>",
		"",
	}
	keys := make([]Key, len(in))
	for i, s := range in {
		keys[i] = New(s)
	}
	sort.SliceStable(keys, func(i, j int) bool { return keys[i].Compare(keys[j]) < 0 })

	got := make([]string, len(keys))
	for i, k := range keys {
		got[i] = k.Original
	}
	want := []string{
		"",                           // empty group
		"!!!",                        // symbol group
		"9 Crimes",                   // number group
		"The Beatles",                // letter group: article dropped -> "beatles"
		"Björk",                      // diacritic folded -> "bjork"
		"blur",                       //                     "blur"
		"Los Lobos",                  // article dropped   -> "lobos"
		"<color=#ff0000>Red</color>", // markup stripped   -> "red"
	}
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("order =\n  %v\nwant\n  %v", got, want)
	}
}

// The two derived forms answer different questions and must not collapse.
func TestSearchKeepsTheArticleThatSortDrops(t *testing.T) {
	k := New("The Beatles")
	if k.Search != "the beatles" {
		t.Errorf("Search = %q, want %q - a player searching \"the beatles\" must match", k.Search, "the beatles")
	}
	if k.Sort != "beatles" {
		t.Errorf("Sort = %q, want %q - a browse list must file the band under B", k.Sort, "beatles")
	}
	if k.Original != "The Beatles" {
		t.Errorf("Original = %q, want the value as the charter wrote it", k.Original)
	}
}

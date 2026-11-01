package songini

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A bare CR is not a line break to YARG, so text after it on that physical line
// is unreachable to the game - and must not become a value carrying a control
// character, which is what made one malformed line cost a whole song.
//
// The fixture is real-world shaped. The last line of song.ini in "04 Infant
// Annihilator - Behold the Kingdom of the Wretched Undying", in the Marathon
// Hero 2 setlist, reads as raw bytes `song_length = 1062206\rplaylist_track = 4`
// - the rest of the file ordinary CRLF, no trailing newline. YARG reads
// song_length = 1062206 and never sees playlist_track. Before this was handled
// the server read song_length as "1062206\rplaylist_track = 4", the .sng writer
// refused it as a value containing a newline, and the song returned HTTP 500.
// YARG loses a field; we lost the song.
//
// It is a FILE rather than a string literal on purpose, so the bytes travel
// through git. .gitattributes marks **/testdata/** as -text to keep them
// intact, and TestBareCRFixtureStillHasItsBareCR is what notices if that stops
// being true - a fixture quietly normalised to CRLF would make every assertion
// below pass on any implementation.
func TestBareCRTruncatesTheValueAsYARGDoes(t *testing.T) {
	f := Parse(readFixture(t))

	// What YARG reads: ExtractText sets stringEnd at the first '\r'.
	if v, ok := f.String("song_length"); !ok || v != "1062206" {
		t.Fatalf("song_length = %q %v, want \"1062206\"", v, ok)
	}
	// And it must parse as the integer the key is typed as.
	// "1062206\rplaylist_track = 4" did not, so the catalog lost the length
	// even on the path that never reaches the packer.
	if n, ok := f.Int("song_length"); !ok || n != 1062206 {
		t.Fatalf("song_length as int = %d %v, want 1062206", n, ok)
	}

	// The spliced-off pair is LOST, deliberately. YARGIniReader's loop calls
	// GotoNextLine before looking for another key, so it jumps to the next
	// '\n' and playlist_track is never read. Recovering it would be friendlier
	// and would put a number in the browse list that the game does not have.
	if v, ok := f.String("playlist_track"); ok {
		t.Fatalf("playlist_track = %q, want absent: YARG cannot reach it", v)
	}

	// Lost, but not silently. This is the only thing that can explain a
	// missing field to whoever is holding the chart.
	if len(f.Dropped) != 1 || f.Dropped[0] != "playlist_track = 4" {
		t.Fatalf("Dropped = %q, want one entry \"playlist_track = 4\"", f.Dropped)
	}

	// Nothing else in the file may be disturbed by the repair.
	for k, want := range map[string]string{
		"name":               "Behold the Spliced Line",
		"artist":             "Bare CR",
		"album":              "Mac Classic Endings",
		"album_track":        "5",
		"preview_start_time": "875540",
		"preview_end_time":   "905540",
	} {
		if v, ok := f.String(k); !ok || v != want {
			t.Fatalf("%s = %q %v, want %q", k, v, ok, want)
		}
	}

	assertWritable(t, "fixture", f)
}

// The fixture has to still be the thing under test.
func TestBareCRFixtureStillHasItsBareCR(t *testing.T) {
	raw := readFixture(t)
	if !bytes.Contains(raw, []byte("1062206\rplaylist_track")) {
		t.Fatalf("fixture no longer holds a BARE CR; tail is %q", raw[max(0, len(raw)-48):])
	}
	if bytes.Contains(raw, []byte("1062206\r\n")) {
		t.Fatal("the fixture's bare CR has been normalised to CRLF")
	}
}

// An ordinary CRLF file must report nothing dropped, or the issue the scanner
// raises off Dropped would fire on every song in a Windows-authored library.
func TestOrdinaryCRLFDropsNothing(t *testing.T) {
	f := Parse([]byte("[Song]\r\nname = X\r\nartist = Y\r\n\r\nalbum = Z\r\n"))
	if len(f.Dropped) != 0 {
		t.Fatalf("Dropped = %q, want empty", f.Dropped)
	}
	if v, _ := f.String("artist"); v != "Y" {
		t.Fatalf("artist = %q, want \"Y\"", v)
	}
}

// A bare CR can land anywhere on a line, not only between two pairs, and a key
// can carry a character the writer reserves without any CR at all. Every case
// here made the song unservable before the fix, and YARG reads every one.
func TestLineBreakVariantsNeverProduceAnUnwritableValue(t *testing.T) {
	for _, tc := range []struct {
		label     string
		raw       string
		key, want string
		wantOK    bool
	}{
		// ExtractText truncates a STRING at the first CR exactly as the
		// numeric path does. This is the case a Kind-aware fix would get wrong.
		{"cr inside a string value", "[Song]\r\nname = Foo\rBar\r\n", "name", "Foo", true},
		// ExtractModifierName breaks on any char <= WHITESPACE_LIMIT (32), and
		// CR is 13, so YARG reads the key "na", fails the table lookup and
		// discards the line. The next line must survive.
		{"cr inside a key", "[Song]\r\nna\rme = X\r\nartist = Y\r\n", "artist", "Y", true},
		{"cr inside a key drops it", "[Song]\r\nna\rme = X\r\nartist = Y\r\n", "na", "", false},
		// But a CR BETWEEN the key and its '=' is only whitespace to YARG:
		// ExtractModifierName stops on it and SkipWhitespaceAndEquals skips
		// it, so the pair is read. MEASURED - corpus case
		// 26-barecr-before-equals put this value in YARG v0.15.0's songcache
		// and left the preceding album out. The first version of this fix cut
		// the whole line at the CR and lost it; only the oracle said so.
		{"cr before the equals", "[Song]\r\nalbum = First\r\nalbum\r = Second\r\n", "album", "Second", true},
		{"cr before the equals, no space", "[Song]\r\nsong_length\r= 5\r\n", "song_length", "5", true},
		// song.ini has no comment syntax in YARG: it reads ";" as a key,
		// matches nothing in SONG_INI_OUTLINES and moves on.
		{"semicolon in a key", "[Song]\r\n; charted by someone = yes\r\nname = X\r\n", "name", "X", true},
		// Several splices on one line: only the first pair is reachable.
		{"two splices keep the first", "[Song]\r\ndelay = 7\rname = A\ralbum = B\r\n", "delay", "7", true},
		{"two splices lose the rest", "[Song]\r\ndelay = 7\rname = A\ralbum = B\r\n", "album", "", false},
		// A CR with nothing after it is a CRLF missing its LF.
		{"trailing bare cr", "[Song]\r\nname = X\r", "name", "X", true},
	} {
		f := Parse([]byte(tc.raw))
		if v, ok := f.String(tc.key); ok != tc.wantOK || v != tc.want {
			t.Errorf("%s: %s = %q %v, want %q %v", tc.label, tc.key, v, ok, tc.want, tc.wantOK)
		}
		assertWritable(t, tc.label, f)
	}
}

// A plain comment line - no '=' anywhere - was never part of this defect, and
// must not become part of the repair. It has no key to drop, so it reports
// nothing, and the issue the scanner raises off Dropped stays quiet.
func TestACommentLineWithNoEqualsIsStillJustSkipped(t *testing.T) {
	for _, raw := range []string{
		"[Song]\r\n; charted by someone\r\nname = X\r\n",
		"[Song]\r\n# charted by someone\r\nname = X\r\n",
		"[Song]\r\nthis line has no equals\r\nname = X\r\n",
	} {
		f := Parse([]byte(raw))
		if len(f.Dropped) != 0 {
			t.Errorf("%q: Dropped = %q, want empty", raw, f.Dropped)
		}
		if v, _ := f.String("name"); v != "X" {
			t.Errorf("%q: name = %q", raw, v)
		}
	}
}

// The .sng metadata path reaches the same writer, so it needs the same floor.
//
// This is a DELIBERATE divergence and the only one here. A .sng stores metadata
// length-prefixed, so IniModifierCollection.AddSng decodes a String value
// whole, CR included, and YARG would read "Foo\rBar" out of the original file.
// We truncate because we REPACK: the archive the client downloads is the one we
// wrote, so after truncation the catalog, the bytes we serve and the game all
// agree on "Foo". Carrying the CR through would instead mean relaxing the
// writer's guard to buy agreement we already have.
func TestFromMapCannotProduceAnUnwritablePair(t *testing.T) {
	f := FromMap(map[string]string{
		"name":        "Foo\rBar",
		"song_length": "1062206\nplaylist_track = 4",
		"na\rme":      "ignored",
		"charter":     "TheSheepQueen; Whizzer",
	}, []string{"name", "song_length", "na\rme", "charter"})

	if v, _ := f.String("name"); v != "Foo" {
		t.Fatalf("name = %q, want \"Foo\"", v)
	}
	if n, ok := f.Int("song_length"); !ok || n != 1062206 {
		t.Fatalf("song_length = %d %v, want 1062206", n, ok)
	}
	if _, ok := f.String("na\rme"); ok {
		t.Fatal("a key carrying a CR must not reach metadata")
	}
	// A ';' in a VALUE is legal and must stay. SngCli v0.3.0 round-trips
	// `charter = TheSheepQueen; Whizzer` unchanged and YARG reads values by
	// length prefix; rejecting it was a previous defect that cost a real song.
	if v, _ := f.String("charter"); v != "TheSheepQueen; Whizzer" {
		t.Fatalf("charter = %q, want the semicolon kept", v)
	}
	assertWritable(t, "FromMap", f)
}

func readFixture(t *testing.T) []byte {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join("testdata", "bare-cr.song.ini"))
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

// assertWritable is the whole point of the change, asserted directly rather
// than inferred from a pack succeeding: nothing the reader produces may carry
// a character sng.validateMeta refuses, because refusing it there costs the
// entire song.
func assertWritable(t *testing.T, label string, f *File) {
	t.Helper()
	for k, v := range f.Values {
		if strings.ContainsAny(v, "\r\n") {
			t.Errorf("%s: value of %q carries a line break: %q", label, k, v)
		}
		if strings.ContainsAny(k, "=;\r\n") {
			t.Errorf("%s: key %q carries a character the .sng writer reserves", label, k)
		}
	}
}

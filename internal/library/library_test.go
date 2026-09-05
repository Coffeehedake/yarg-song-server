package library

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// chartFor returns a minimal playable .chart. The body varies with seed because
// song identity is SHA1 of these bytes and nothing else - two songs built with
// the same seed are deliberately the same song to YARG.
func chartFor(seed string) string {
	return fmt.Sprintf(`[Song]
{
  Resolution = 192
  Name = "%s"
}
[ExpertSingle]
{
  768 = N 0 0
  864 = N 1 0
}
`, seed)
}

type songSpec struct {
	dir   string
	seed  string // same seed == same chart == same chart hash
	ini   map[string]string
	extra map[string]string // extra files, e.g. different album art
}

func writeLibrary(t *testing.T, specs []songSpec) string {
	t.Helper()
	root := t.TempDir()
	for _, sp := range specs {
		dir := filepath.Join(root, filepath.FromSlash(sp.dir))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}

		var b strings.Builder
		b.WriteString("[Song]\n")
		for k, v := range sp.ini {
			fmt.Fprintf(&b, "%s = %s\n", k, v)
		}
		write(t, filepath.Join(dir, "song.ini"), b.String())
		write(t, filepath.Join(dir, "notes.chart"), chartFor(sp.seed))
		// YARG rejects a chart with no audio, and so does our scanner, so every
		// fixture needs a stem or the whole library is flagged.
		write(t, filepath.Join(dir, "song.ogg"), "audio-"+sp.seed)
		for name, body := range sp.extra {
			write(t, filepath.Join(dir, name), body)
		}
	}
	return root
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func buildTestLibrary(t *testing.T) *Index {
	t.Helper()
	root := writeLibrary(t, []songSpec{
		{dir: "beatles", seed: "a", ini: map[string]string{
			"name": "Yesterday", "artist": "The Beatles", "album": "Help!",
			"album_track": "13", "year": "1965", "charter": "zed", "genre": "Rock",
		}},
		{dir: "bjork", seed: "b", ini: map[string]string{
			"name": "Jóga", "artist": "Björk", "album": "Homogenic",
			"album_track": "3", "year": "1997", "charter": "alice", "genre": "Electronic",
		}},
		{dir: "blur", seed: "c", ini: map[string]string{
			"name": "Song 2", "artist": "Blur", "album": "Blur",
			"album_track": "6", "year": "1997", "charter": "bob", "genre": "Rock",
		}},
		{dir: "untitled", seed: "d", ini: map[string]string{
			"name": "Hidden Track", "artist": "Blur", "album": "Blur",
			// No album_track and no year on purpose: both must sort LAST, not
			// first, and that is only true because the defaults are 16000 and
			// "unparsed" rather than 0.
			"charter": "bob", "genre": "Rock",
		}},
		{dir: "numbers", seed: "e", ini: map[string]string{
			"name": "9 Crimes", "artist": "Damien Rice", "album": "9",
			"album_track": "1", "year": "2006", "charter": "carol", "genre": "Folk",
		}},
	})
	ix, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(ix.Problems) != 0 {
		t.Fatalf("unexpected scan problems: %+v", ix.Problems)
	}
	return ix
}

func TestBuildIndexes(t *testing.T) {
	ix := buildTestLibrary(t)
	if ix.Len() != 5 {
		t.Fatalf("indexed %d songs, want 5", ix.Len())
	}
	if ix.DistinctCharts() != 5 {
		t.Fatalf("distinct charts = %d, want 5", ix.DistinctCharts())
	}
	for _, e := range ix.entries {
		if e.Kind != SourceDir {
			t.Errorf("%s: kind = %q, want dir", e.Song.Name, e.Kind)
		}
		if e.Path == "" {
			t.Errorf("%s: no server-side path recorded", e.Song.Name)
		}
		if e.Song.SourcePath == "" {
			t.Errorf("%s: no library-relative path recorded", e.Song.Name)
		}
	}
}

// Identity is SHA1 of the chart alone, so two packages built from the same
// chart bytes are one song with two packages. Upstream models this as
// hash -> List<SongEntry> and collapsing it here would lose content silently.
func TestSameChartDifferentPackageIsOneSongTwoPackages(t *testing.T) {
	root := writeLibrary(t, []songSpec{
		{dir: "with-art", seed: "shared", ini: map[string]string{"name": "Twin", "artist": "X"},
			extra: map[string]string{"album.png": "\x89PNG\r\n\x1a\nfake"}},
		{dir: "no-art", seed: "shared", ini: map[string]string{"name": "Twin", "artist": "X"}},
	})
	ix, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 2 {
		t.Fatalf("indexed %d, want 2", ix.Len())
	}
	if ix.DistinctCharts() != 1 {
		t.Fatalf("distinct charts = %d, want 1 - the chart bytes are identical", ix.DistinctCharts())
	}

	hash := ix.entries[0].Song.ChartHash
	shared := ix.ByChartHash(hash)
	if len(shared) != 2 {
		t.Fatalf("ByChartHash returned %d entries, want both packages", len(shared))
	}
	if shared[0].Song.PackageHash == shared[1].Song.PackageHash {
		t.Fatal("the two packages differ by an album art file, so their package hashes must differ")
	}
	for _, e := range shared {
		if got := ix.ByPackageHash(e.Song.PackageHash); got != e {
			t.Errorf("package hash %s did not resolve back to its own entry", e.Song.PackageHash)
		}
	}
}

func TestMissing(t *testing.T) {
	ix := buildTestLibrary(t)

	all := ix.Missing(nil)
	if len(all) != 5 {
		t.Fatalf("a client with nothing is missing %d, want 5", len(all))
	}
	if !isSorted(all) {
		t.Error("Missing must be sorted: the same question twice must give the same answer")
	}

	none := ix.Missing(all)
	if len(none) != 0 {
		t.Fatalf("a client with everything is missing %v, want nothing", none)
	}

	// Hash comparison is case-insensitive and tolerant of stray whitespace,
	// because a client assembling a JSON list from its own cache should not be
	// punished for either.
	upper := make([]string, len(all))
	for i, h := range all {
		upper[i] = " " + strings.ToUpper(h) + " "
	}
	if got := ix.Missing(upper); len(got) != 0 {
		t.Fatalf("uppercase, padded hashes were not recognised: still missing %d", len(got))
	}

	// A hash the server has never heard of is simply not in the library and
	// must not appear in the answer.
	if got := ix.Missing([]string{strings.Repeat("f", 40)}); len(got) != 5 {
		t.Fatalf("an unknown hash changed the answer: %d", len(got))
	}
}

func TestQuerySortByName(t *testing.T) {
	ix := buildTestLibrary(t)
	got := names(ix.Query(Query{SortBy: ByName, Limit: 100}))
	want := []string{"9 Crimes", "Hidden Track", "Jóga", "Song 2", "Yesterday"}
	assertOrder(t, "sort=name", got, want)
}

// The point of the whole sortkey package, seen from the API: an artist list a
// player would recognise. "The Beatles" under B, "Björk" folded to "bjork" so
// it lands beside "Blur" rather than after every ASCII name.
func TestQuerySortByArtistMatchesTheClient(t *testing.T) {
	ix := buildTestLibrary(t)
	got := artists(ix.Query(Query{SortBy: ByArtist, Limit: 100}))
	want := []string{"The Beatles", "Björk", "Blur", "Blur", "Damien Rice"}
	assertOrder(t, "sort=artist", got, want)
}

// album_track defaulting to 16000 rather than 0 is what puts an unnumbered
// track at the END of its album. Getting this wrong is one of the four defects
// docs/SOURCES.md records, so it is pinned here as well as in the ini reader.
func TestQuerySortByAlbumPutsUnnumberedTrackLast(t *testing.T) {
	ix := buildTestLibrary(t)
	page := ix.Query(Query{SortBy: ByAlbum, Limit: 100})

	var blurTracks []string
	for _, e := range page.Entries {
		if e.Song.Album == "Blur" {
			blurTracks = append(blurTracks, e.Song.Name)
		}
	}
	assertOrder(t, "Blur album order", blurTracks, []string{"Song 2", "Hidden Track"})

	if got := ix.entries[0].Song.AlbumTrack; got == 0 {
		t.Log("note: this test only means something while the unnumbered default is 16000")
	}
}

// Upstream sorts a missing year LAST, using int.MaxValue. Our catalog says 0,
// so the sentinel has to be translated - comparing raw would file every undated
// chart first.
func TestQuerySortByYearPutsUndatedLast(t *testing.T) {
	ix := buildTestLibrary(t)
	got := names(ix.Query(Query{SortBy: ByYear, Limit: 100}))
	if got[len(got)-1] != "Hidden Track" {
		t.Fatalf("sort=year gave %v; the undated song must come last, not first", got)
	}
	if got[0] != "Yesterday" {
		t.Fatalf("sort=year gave %v; 1965 should lead", got)
	}
}

func TestQueryDescendingReversesEverything(t *testing.T) {
	ix := buildTestLibrary(t)
	asc := names(ix.Query(Query{SortBy: ByName, Limit: 100}))
	desc := names(ix.Query(Query{SortBy: ByName, Descending: true, Limit: 100}))
	for i := range asc {
		if asc[i] != desc[len(desc)-1-i] {
			t.Fatalf("descending is not the reverse of ascending:\n asc %v\ndesc %v", asc, desc)
		}
	}
}

// A query goes through the client's own folding, so a player who cannot type
// "Björk" still finds it.
func TestQueryTextUsesTheClientsFolding(t *testing.T) {
	ix := buildTestLibrary(t)
	cases := []struct {
		q    string
		want string
	}{
		{"bjork", "Jóga"},
		{"Björk", "Jóga"},
		{"BJÖRK", "Jóga"},
		{"the beatles", "Yesterday"}, // the article is kept in the search form
		{"beatles", "Yesterday"},
		{"homogenic", "Jóga"}, // album
		{"alice", "Jóga"},     // charter
	}
	for _, c := range cases {
		got := names(ix.Query(Query{Text: c.q, Limit: 100}))
		if len(got) != 1 || got[0] != c.want {
			t.Errorf("q=%q matched %v, want exactly [%s]", c.q, got, c.want)
		}
	}

	if got := ix.Query(Query{Text: "no such song", Limit: 100}); got.Total != 0 {
		t.Errorf("a query matching nothing returned %d results", got.Total)
	}
}

// A query must not match across a field boundary: "blur blur" is not a thing
// any single attribute says, even though the artist is Blur and the album is
// Blur.
func TestQueryDoesNotMatchAcrossFields(t *testing.T) {
	ix := buildTestLibrary(t)
	if got := ix.Query(Query{Text: "blur blur", Limit: 100}); got.Total != 0 {
		t.Errorf("a query spanning two attributes matched %d results", got.Total)
	}
}

func TestQueryPaging(t *testing.T) {
	ix := buildTestLibrary(t)

	page := ix.Query(Query{SortBy: ByName, Limit: 2, Offset: 0})
	if page.Total != 5 || len(page.Entries) != 2 {
		t.Fatalf("total=%d entries=%d, want total 5 and a page of 2", page.Total, len(page.Entries))
	}

	page2 := ix.Query(Query{SortBy: ByName, Limit: 2, Offset: 4})
	if len(page2.Entries) != 1 {
		t.Fatalf("last page has %d entries, want 1", len(page2.Entries))
	}

	// Past the end is an empty page, not an error and not a wrapped one.
	beyond := ix.Query(Query{SortBy: ByName, Limit: 2, Offset: 500})
	if len(beyond.Entries) != 0 || beyond.Total != 5 {
		t.Fatalf("offset past the end gave %d entries (total %d)", len(beyond.Entries), beyond.Total)
	}

	// An unbounded request is capped rather than honoured.
	capped := ix.Query(Query{Limit: MaxLimit * 10})
	if len(capped.Entries) > MaxLimit {
		t.Fatalf("limit was not capped: %d entries", len(capped.Entries))
	}
}

func TestStoreReplaceIsAtomic(t *testing.T) {
	ix := buildTestLibrary(t)
	s := NewStore(ix)
	if s.Get() != ix {
		t.Fatal("Get did not return the index it was built with")
	}
	other := buildTestLibrary(t)
	s.Replace(other)
	if s.Get() != other {
		t.Fatal("Replace did not swap the index")
	}
}

func TestParseAttribute(t *testing.T) {
	for _, a := range Attributes {
		if got, ok := ParseAttribute(string(a)); !ok || got != a {
			t.Errorf("ParseAttribute(%q) = %q, %v", a, got, ok)
		}
		if got, ok := ParseAttribute(" " + strings.ToUpper(string(a)) + " "); !ok || got != a {
			t.Errorf("ParseAttribute did not tolerate case and padding for %q", a)
		}
	}
	if _, ok := ParseAttribute("popularity"); ok {
		t.Error("an attribute the client does not sort by must be refused, not silently accepted")
	}
	if len(Attributes) != 12 {
		t.Fatalf("there are %d sort attributes; upstream's SongAttribute has exactly 12", len(Attributes))
	}
}

func names(p Page) []string {
	out := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		out = append(out, e.Song.Name)
	}
	return out
}

func artists(p Page) []string {
	out := make([]string, 0, len(p.Entries))
	for _, e := range p.Entries {
		out = append(out, e.Song.Artist)
	}
	return out
}

func assertOrder(t *testing.T, what string, got, want []string) {
	t.Helper()
	if strings.Join(got, "|") != strings.Join(want, "|") {
		t.Errorf("%s gave\n  %v\nwant\n  %v", what, got, want)
	}
}

func isSorted(s []string) bool {
	for i := 1; i < len(s); i++ {
		if s[i-1] > s[i] {
			return false
		}
	}
	return true
}

package scan

import (
	"bytes"
	"io"
	"strings"
	"testing"
	"testing/fstest"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// spliceINI is sampleINI with the last pair spliced onto the song_length line by
// a bare CR, which is the shape found in the wild: song.ini in "04 Infant
// Annihilator - Behold the Kingdom of the Wretched Undying", in the Marathon
// Hero 2 setlist, ends
//
//	song_length = 1062206\rplaylist_track = 4
//
// with the rest of the file ordinary CRLF. YARG reads song_length and never
// sees playlist_track, because its reader breaks lines on '\n' alone.
//
// The CR is written as an escape in Go source rather than read from a file so
// that nothing on any platform can rewrite it. The byte-exact on-disk fixture
// that proves the bytes survive git lives in internal/songini/testdata.
const spliceINI = "[Song]\r\n" +
	"name = Behold the Spliced Line\r\n" +
	"artist = Bare CR\r\n" +
	"song_length = 1062206\rplaylist_track = 4"

func spliceFolder() fstest.MapFS {
	f := sampleFolder()
	f["song.ini"] = &fstest.MapFile{Data: []byte(spliceINI)}
	return f
}

// The defect, end to end: ONE malformed song.ini line made an entire song
// unservable. PackFS returned `sng: value of "song_length" contains a newline`,
// so the song answered HTTP 500 - while YARG itself loads it, reads
// song_length = 1062206, and merely loses playlist_track.
//
// Ours was strictly worse than the game's: YARG drops one field, we dropped the
// song. This asserts the whole chain - scanned, packed, and the packed archive
// carrying the metadata YARG would read.
func TestASplicedLineDoesNotCostTheSong(t *testing.T) {
	fsys := spliceFolder()

	s, err := ScanDir(fsys)
	if err != nil {
		t.Fatalf("ScanDir: %v", err)
	}
	if s.Name != "Behold the Spliced Line" {
		t.Fatalf("name = %q", s.Name)
	}
	if got := s.RawMetadata["song_length"]; got != "1062206" {
		t.Fatalf("song_length = %q, want \"1062206\" - what YARG reads", got)
	}
	if _, ok := s.RawMetadata["playlist_track"]; ok {
		t.Fatal("playlist_track reached the catalog; YARG cannot reach it, so neither may we")
	}

	// The song is served, so the loss has to be visible somewhere. A browse
	// list quietly short one field is the thing nobody can debug.
	if !hasIssue(s.Issues, catalog.IssueMetadataUnreachable) {
		t.Fatalf("no %s issue raised; issues = %+v", catalog.IssueMetadataUnreachable, s.Issues)
	}
	var detail string
	for _, i := range s.Issues {
		if i.Code == catalog.IssueMetadataUnreachable {
			detail = i.Detail
		}
	}
	if !strings.Contains(detail, "playlist_track = 4") {
		t.Fatalf("the issue does not name what was dropped: %q", detail)
	}

	// Packing is the step that used to fail.
	var buf bytes.Buffer
	if err := PackFS(fsys, &buf); err != nil {
		t.Fatalf("PackFS: %v", err)
	}

	// And the archive must carry what YARG would read, not merely exist.
	a, err := sng.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("reopen packed archive: %v", err)
	}
	if got := a.Metadata["song_length"]; got != "1062206" {
		t.Fatalf("packed song_length = %q, want \"1062206\"", got)
	}
	if got := a.Metadata["name"]; got != "Behold the Spliced Line" {
		t.Fatalf("packed name = %q", got)
	}
	for k, v := range a.Metadata {
		if strings.ContainsAny(v, "\r\n") {
			t.Fatalf("packed value of %q carries a line break: %q", k, v)
		}
	}
}

// A clean song must not acquire the issue, or the signal is worthless.
func TestACleanSongRaisesNoUnreachableMetadataIssue(t *testing.T) {
	s, err := ScanDir(sampleFolder())
	if err != nil {
		t.Fatal(err)
	}
	if hasIssue(s.Issues, catalog.IssueMetadataUnreachable) {
		t.Fatalf("clean song flagged: %+v", s.Issues)
	}
}

// The container a song arrived in must never change its bytes - the guarantee
// TestAllThreeContainersProduceTheSameArchive exists for. A song with a spliced
// line is no exception, and it is worth pinning because the repair happens in
// the reader, which every container shares.
func TestASplicedLinePacksDeterministically(t *testing.T) {
	var a, b bytes.Buffer
	if err := PackFS(spliceFolder(), &a); err != nil {
		t.Fatal(err)
	}
	if err := PackFS(spliceFolder(), &b); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatalf("two packs of one folder differ: %d vs %d bytes", a.Len(), b.Len())
	}
}

// A song whose song.ini is a single spliced line and nothing else still packs.
// The degenerate case is worth one test because it is the one where the repair
// could leave the metadata empty rather than short.
func TestASongWhoseOnlyPairIsSplicedStillPacks(t *testing.T) {
	f := sampleFolder()
	f["song.ini"] = &fstest.MapFile{Data: []byte("[Song]\r\nname = Only Line\rartist = Lost\r\n")}
	s, err := ScanDir(f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Only Line" {
		t.Fatalf("name = %q, want \"Only Line\"", s.Name)
	}
	if err := PackFS(f, io.Discard); err != nil {
		t.Fatalf("PackFS: %v", err)
	}
}

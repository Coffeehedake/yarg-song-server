package scan

import (
	"archive/zip"
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// The byte that cost vault2 an archive: 0xA0, which is "á" in CP437 and a non-breaking space in
// CP1252. The folder is "Maná - Oye Mi Amor", a real band, and only one of those two decodings
// says so.
const legacyFolder = "Man\xa0 - Oye Mi Amor"

func TestDecodeEntryNameLeavesValidUTF8Alone(t *testing.T) {
	// Names that already work must not be "corrected". A decoder that ran on everything would
	// turn correctly stored accents into mojibake, which is the same defect pointing the other
	// way and far harder to notice because nothing fails.
	for _, name := range []string{
		"Song/notes.mid",
		"Green Day - ¡Viva la Gloria!/song.ini",
		"Maná - Oye Mi Amor/song.ogg",
		`Song\notes.mid`,
		"日本語/notes.chart",
	} {
		if got := decodeEntryName(name); got != name {
			t.Errorf("decodeEntryName(%q) = %q, want it unchanged", name, got)
		}
	}
}

func TestDecodeEntryNameReadsALegacyNameAsCP437(t *testing.T) {
	got := decodeEntryName(legacyFolder + "/notes.mid")
	want := "Maná - Oye Mi Amor/notes.mid"
	if got != want {
		t.Errorf("decodeEntryName = %q, want %q", got, want)
	}
}

// writeLegacyZip builds a zip holding two songs, one of which has a folder name stored in a
// legacy encoding with the UTF-8 flag unset - exactly the shape measured in "Rock Band 3.zip".
func writeLegacyZip(t *testing.T) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "legacy.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	songs := map[string]map[string]string{
		"Plain Song": {
			"song.ini":  "[song]\nname = Plain\nartist = Ordinary\n",
			"notes.mid": "plain chart bytes",
			"song.ogg":  "plain audio",
		},
		legacyFolder: {
			"song.ini":  "[song]\nname = Oye Mi Amor\nartist = Mana\n",
			"notes.mid": "legacy chart bytes",
			"song.ogg":  "legacy audio",
		},
	}

	for folder, files := range songs {
		for name, body := range files {
			h := &zip.FileHeader{Name: folder + "/" + name, Method: zip.Deflate}
			// What an archiver that predates the UTF-8 flag produces: raw bytes, flag unset.
			h.NonUTF8 = true
			w, err := zw.CreateHeader(h)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestALegacyEncodedSongIsFoundRatherThanLost(t *testing.T) {
	// The regression test for the real defect. Before archiveFS this returned ONE song and an
	// error naming the other, because fs.Sub refused the legacy path - and in the wild that
	// turned an 83-song archive into zero songs and one error.
	got, err := ScanContainerAll(writeLegacyZip(t))
	if err != nil {
		t.Fatalf("ScanContainerAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d songs, want 2", len(got))
	}

	var legacy *ContainerSong
	for i := range got {
		if got[i].Song.Name == "Oye Mi Amor" {
			legacy = &got[i]
		}
	}
	if legacy == nil {
		t.Fatalf("the legacy-named song is missing; got %v", got)
	}
	if want := "Maná - Oye Mi Amor"; legacy.Inner != want {
		t.Errorf("inner path = %q, want %q", legacy.Inner, want)
	}
}

func TestALegacyEncodedSongCanStillBePacked(t *testing.T) {
	// Being indexed is not enough. The inner path recorded at scan time is what the packer
	// uses to find the song again, so if decoding were applied in one place and not the other
	// the song would browse and then 404.
	p := writeLegacyZip(t)
	songs, err := ScanContainerAll(p)
	if err != nil {
		t.Fatal(err)
	}

	for _, cs := range songs {
		var buf bytes.Buffer
		if err := PackPathInner(p, cs.Inner, &buf); err != nil {
			t.Fatalf("pack %q: %v", cs.Inner, err)
		}
		a, err := sng.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
		if err != nil {
			t.Fatalf("packed %q is not a readable .sng: %v", cs.Inner, err)
		}
		if got := a.Metadata["name"]; got != cs.Song.Name {
			t.Errorf("packing %q produced %q, want %q", cs.Inner, got, cs.Song.Name)
		}
	}
}

func TestArchiveFSShowsDirectoriesThatHaveNoEntryOfTheirOwn(t *testing.T) {
	// Plenty of archives store only file entries. A view that listed nothing for a directory
	// with no entry of its own would hide every song in them.
	a := newArchiveFS([]archiveItem{
		{stored: "Song/notes.mid", size: 3, open: nopOpen("abc")},
		{stored: "Song/extras/readme.txt", size: 1, open: nopOpen("x")},
	})

	entries, err := fs.ReadDir(a, ".")
	if err != nil {
		t.Fatalf("ReadDir(.): %v", err)
	}
	if len(entries) != 1 || entries[0].Name() != "Song" || !entries[0].IsDir() {
		t.Fatalf("root listing = %v, want one directory named Song", names(entries))
	}

	inner, err := fs.ReadDir(a, "Song")
	if err != nil {
		t.Fatalf("ReadDir(Song): %v", err)
	}
	if got := names(inner); len(got) != 2 || got[0] != "extras" || got[1] != "notes.mid" {
		t.Errorf("Song listing = %v, want [extras notes.mid]", got)
	}

	body, err := fs.ReadFile(a, "Song/notes.mid")
	if err != nil || string(body) != "abc" {
		t.Errorf("ReadFile = %q, %v; want \"abc\", nil", body, err)
	}
}

func TestArchiveFSDoesNotTreatADirectoryAsAFile(t *testing.T) {
	// A 7z may record a directory without a trailing slash. If that became a zero-byte file
	// while its children made it a directory too, a lookup would answer differently depending
	// on which map was consulted first.
	a := newArchiveFS([]archiveItem{
		{stored: "Song", size: 0, open: nopOpen("")},
		{stored: "Song/notes.mid", size: 3, open: nopOpen("abc")},
	})

	f, err := a.Open("Song")
	if err != nil {
		t.Fatalf("Open(Song): %v", err)
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	if !info.IsDir() {
		t.Error("Song opened as a file, but entries live under it")
	}
}

func nopOpen(body string) func() (io.ReadCloser, error) {
	return func() (io.ReadCloser, error) { return io.NopCloser(bytes.NewReader([]byte(body))), nil }
}

func names(entries []fs.DirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

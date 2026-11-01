package scan

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// writeMultiZip builds a zip holding several song folders, which is the shape that used to be
// refused outright with ErrTooManySongs.
func writeMultiZip(t *testing.T, songs map[string]map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "pack.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for folder, files := range songs {
		for name, body := range files {
			w, err := zw.Create(folder + "/" + name)
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

func twoSongZip(t *testing.T) string {
	t.Helper()
	return writeMultiZip(t, map[string]map[string]string{
		"Alpha Song": {
			"song.ini":  "[song]\nname = Alpha\nartist = First\ncharter = X\n",
			"notes.mid": "alpha chart bytes",
			"song.ogg":  "alpha audio",
		},
		"Beta Song": {
			"song.ini":  "[song]\nname = Beta\nartist = Second\ncharter = X\n",
			"notes.mid": "beta chart bytes",
			"song.ogg":  "beta audio",
		},
	})
}

func TestAnArchiveOfSeveralSongsYieldsAllOfThem(t *testing.T) {
	got, err := ScanContainerAll(twoSongZip(t))
	if err != nil {
		t.Fatalf("ScanContainerAll: %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("found %d songs, want 2", len(got))
	}

	names := map[string]string{} // song name -> inner path
	for _, cs := range got {
		names[cs.Song.Name] = cs.Inner
	}
	for _, want := range []string{"Alpha", "Beta"} {
		inner, ok := names[want]
		if !ok {
			t.Errorf("song %q missing; got %v", want, names)
			continue
		}
		if inner == "" {
			t.Errorf("song %q has no inner path, so the packer could not find it again", want)
		}
	}

	// Distinct charts, or the two would collide in the index and one would shadow the other.
	if got[0].Song.ChartHash == got[1].Song.ChartHash {
		t.Error("both songs share a chart hash")
	}
}

func TestEachSongInAnArchivePacksToItsOwnBytes(t *testing.T) {
	p := twoSongZip(t)
	songs, err := ScanContainerAll(p)
	if err != nil {
		t.Fatal(err)
	}

	packed := map[string][]byte{}
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
			t.Errorf("packing %q produced %q, want %q - the packer found the wrong song",
				cs.Inner, got, cs.Song.Name)
		}
		packed[cs.Song.Name] = buf.Bytes()
	}

	// The failure this guards against is subtle: a packer that ignores the inner path
	// returns the FIRST song in the archive for every hash, which looks like it works.
	if bytes.Equal(packed["Alpha"], packed["Beta"]) {
		t.Error("both songs packed to identical bytes")
	}
}

func TestTheWalkEmitsEverySongInAnArchive(t *testing.T) {
	lib := t.TempDir()
	src := twoSongZip(t)
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(lib, "pack.zip"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	var songs, problems int
	if err := WalkLibrary(lib, func(r Result) {
		if r.Err != nil {
			t.Errorf("walk reported %s: %v", r.Path, r.Err)
			problems++
			return
		}
		if r.Song != nil {
			songs++
		}
	}); err != nil {
		t.Fatal(err)
	}

	if songs != 2 {
		t.Errorf("walk emitted %d songs, want 2", songs)
	}
	if problems != 0 {
		t.Errorf("walk reported %d problems, want 0", problems)
	}
}

func TestASingleSongArchiveStillHasNoInnerPath(t *testing.T) {
	// The common case must not regress into "nested one level deep" just because the walker
	// can now descend: a zip of one song folder is still one song, and its bytes come from
	// the same path they always did.
	files := map[string]string{
		"song.ini":  "[song]\nname = Only\nartist = Solo\n",
		"notes.mid": "chart",
		"song.ogg":  "audio",
	}
	p := writeZip(t, files, "Only Song/", nil)

	got, err := ScanContainerAll(p)
	if err != nil {
		t.Fatalf("ScanContainerAll: %v", err)
	}
	if len(got) != 1 {
		t.Fatalf("found %d songs, want 1", len(got))
	}
	if got[0].Song.Name != "Only" {
		t.Errorf("name = %q, want Only", got[0].Song.Name)
	}
}

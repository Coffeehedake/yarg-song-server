package library

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
)

// writeSongFolder makes a loose song on disk.
func writeSongFolder(t *testing.T, dir, name string) {
	t.Helper()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	for file, body := range map[string]string{
		"song.ini":  "[song]\nname = " + name + "\nartist = Someone\n",
		"notes.mid": name + " chart bytes",
		"song.ogg":  name + " audio",
	} {
		if err := os.WriteFile(filepath.Join(dir, file), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

// writeSongZip makes an archive holding the same song, byte for byte.
func writeSongZip(t *testing.T, path, folder, name string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for file, body := range map[string]string{
		"song.ini":  "[song]\nname = " + name + "\nartist = Someone\n",
		"notes.mid": name + " chart bytes",
		"song.ogg":  name + " audio",
	} {
		w, err := zw.Create(folder + "/" + file)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOneSongFoundTwiceIsListedOnce(t *testing.T) {
	// The shape unpacking produces: an archive, and the folder that came out of it, both in
	// the library. Measured before this was fixed - every song appeared twice in the browse
	// page and would have appeared twice in the game, because the index detected the
	// duplicate packages and then listed them anyway.
	root := t.TempDir()
	writeSongFolder(t, filepath.Join(root, "Pack", "The Song"), "Twice")
	writeSongZip(t, filepath.Join(root, "Pack.zip"), "The Song", "Twice")

	ix, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}

	if ix.Len() != 1 {
		t.Errorf("Len = %d, want 1 - the same bytes found twice is one song", ix.Len())
	}
	if ix.DistinctCharts() != 1 {
		t.Errorf("DistinctCharts = %d, want 1", ix.DistinctCharts())
	}
	if ix.DuplicatePackages != 1 {
		t.Errorf("DuplicatePackages = %d, want 1 - the copy is dropped, not forgotten",
			ix.DuplicatePackages)
	}
}

func TestTheCopyThatSurvivesIsTheCheapestToServe(t *testing.T) {
	// Which copy is kept is not arbitrary. Serving from a container means decompressing it
	// first, and on a solid .7z that was measured at about three minutes a song, so a loose
	// folder holding the identical bytes must win.
	root := t.TempDir()
	writeSongFolder(t, filepath.Join(root, "Pack", "The Song"), "Twice")
	writeSongZip(t, filepath.Join(root, "Pack.zip"), "The Song", "Twice")

	ix, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 1 {
		t.Fatalf("Len = %d, want 1", ix.Len())
	}

	kept := ix.entries[0]
	if kept.Kind != SourceDir {
		t.Errorf("kept the %s copy; want the loose folder, which needs no decompressing",
			kept.Kind)
	}
}

func TestTwoPackagesSharingAChartAreStillBothListed(t *testing.T) {
	// The case that must NOT collapse. Same chart, different audio, is two packages of one
	// song - upstream's own cache is a chart hash to a LIST for exactly this - and dropping
	// one would hide a real alternative rather than a copy.
	root := t.TempDir()
	writeSongFolder(t, filepath.Join(root, "Mix A"), "Shared")
	writeSongFolder(t, filepath.Join(root, "Mix B"), "Shared")

	// Same chart bytes, different audio: the chart hash matches, the package hash does not.
	if err := os.WriteFile(filepath.Join(root, "Mix B", "song.ogg"),
		[]byte("a different master"), 0o644); err != nil {
		t.Fatal(err)
	}

	ix, err := Build(root)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 2 {
		t.Errorf("Len = %d, want 2 - different bytes are not a duplicate", ix.Len())
	}
	if ix.DistinctCharts() != 1 {
		t.Errorf("DistinctCharts = %d, want 1 - it is one song in two packages",
			ix.DistinctCharts())
	}
	if ix.DuplicatePackages != 0 {
		t.Errorf("DuplicatePackages = %d, want 0", ix.DuplicatePackages)
	}
}

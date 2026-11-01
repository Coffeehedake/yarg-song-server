package unpack

import (
	"archive/zip"
	"bytes"
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// songZipBytes builds, in memory, a zip of one song folder - the shape that sits INSIDE the
// packs this is about.
func songZipBytes(t *testing.T, name string) []byte {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for file, body := range map[string]string{
		"song.ini":  "[song]\nname = " + name + "\nartist = Someone\n",
		"notes.mid": name + " chart bytes",
		"song.ogg":  name + " audio",
	} {
		w, err := zw.Create(name + "/" + file)
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
	return buf.Bytes()
}

// writePackOfPacks builds an archive whose entries are archives, which is what "Rock Band 2
// DLC" turned out to be: 931 entries, almost all of them .7z with one song in each, and zero
// songs visible at the level songRoots looks.
func writePackOfPacks(t *testing.T, path string, songs ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, s := range songs {
		w, werr := zw.Create("Pack/" + s + ".zip")
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr := w.Write(songZipBytes(t, s)); werr != nil {
			t.Fatal(werr)
		}
	}
	// A loose file alongside, so the test is not only about archives.
	w, _ := zw.Create("Pack/readme.txt")
	_, _ = w.Write([]byte("about this pack"))
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestCheckSaysHowManyArchivesAreInsideWhenNoSongsAre(t *testing.T) {
	// "0 songs" on its own reads as empty or broken, and the real pack is neither. Measured
	// on vault2: Rock Band 2 DLC reports zero songs across sixteen parts and holds 931 entries.
	root := t.TempDir()
	writePackOfPacks(t, filepath.Join(root, "Pack.zip"), "Alpha", "Beta", "Gamma")

	plan, err := (Options{LibraryRoot: root}).Inspect("Pack.zip")
	if err != nil {
		t.Fatal(err)
	}
	if plan.Songs != 0 {
		t.Errorf("songs = %d; this pack has none at the level roots are read", plan.Songs)
	}
	if plan.NestedArchives != 3 {
		t.Errorf("nested_archives = %d, want 3 - otherwise the report says nothing is here",
			plan.NestedArchives)
	}
}

func TestArchivesInsideAnArchiveAreOpenedToo(t *testing.T) {
	root := t.TempDir()
	writePackOfPacks(t, filepath.Join(root, "Pack.zip"), "Alpha", "Beta", "Gamma")

	res, err := (Options{LibraryRoot: root}).Run("Pack.zip")
	if err != nil {
		t.Fatal(err)
	}
	if res.NestedOpened != 3 {
		t.Errorf("opened %d inner archives, want 3 (%v)", res.NestedOpened, res.NestedProblems)
	}
	if res.SongsFound != 3 {
		t.Errorf("songs = %d, want 3 - the songs are a level down and must still be counted",
			res.SongsFound)
	}

	for _, s := range []string{"Alpha", "Beta", "Gamma"} {
		p := filepath.Join(root, "Pack", "Pack", s, s, "notes.mid")
		if _, serr := os.Stat(p); serr != nil {
			t.Errorf("%s was not opened: %v", s, serr)
		}
	}
}

func TestTheInnerArchivesAreKeptButIgnored(t *testing.T) {
	// Kept, because nothing here deletes an operator's files. Ignored, because leaving 931
	// archives beside the songs they produced would have the library index both.
	root := t.TempDir()
	writePackOfPacks(t, filepath.Join(root, "Pack.zip"), "Alpha", "Beta")

	if _, err := (Options{LibraryRoot: root}).Run("Pack.zip"); err != nil {
		t.Fatal(err)
	}

	parked := filepath.Join(root, "Pack", scan.IgnoredDirName)
	entries, err := os.ReadDir(parked)
	if err != nil {
		t.Fatalf("the inner archives were not parked: %v", err)
	}
	if len(entries) != 2 {
		t.Errorf("parked %d archives, want 2", len(entries))
	}

	// And the walk does not see them, so no song is listed twice.
	songs := 0
	if err := scan.WalkLibrary(root, func(r scan.Result) {
		if r.Err == nil && r.Song != nil {
			songs++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if songs != 2 {
		t.Errorf("the library sees %d songs, want 2 - a parked archive must not count again", songs)
	}
}

func TestALooseFileBesideTheInnerArchivesSurvives(t *testing.T) {
	// Opening the archives must not disturb anything else that came out of the pack.
	root := t.TempDir()
	writePackOfPacks(t, filepath.Join(root, "Pack.zip"), "Alpha")

	if _, err := (Options{LibraryRoot: root}).Run("Pack.zip"); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(root, "Pack", "Pack", "readme.txt")); err != nil {
		t.Errorf("readme.txt did not survive: %v", err)
	}
}

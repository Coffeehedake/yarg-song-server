package unpack

import (
	"archive/zip"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// writePartZip builds one slice of a split export: a zip whose entries all sit under a shared
// top-level folder, holding exactly the files it is given.
func writePartZip(t *testing.T, path, top string, files map[string]string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for name, body := range files {
		w, werr := zw.Create(top + "/" + name)
		if werr != nil {
			t.Fatal(werr)
		}
		if _, werr := w.Write([]byte(body)); werr != nil {
			t.Fatal(werr)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
}

// splitLibrary builds the shape that is actually dangerous: part one holds the charts, so it
// looks like a complete archive of songs, while the audio lives in part two.
func splitLibrary(t *testing.T) (root string, o Options) {
	t.Helper()
	root = t.TempDir()

	writePartZip(t, filepath.Join(root, "Discography-20261101T183503Z-1-001.zip"), "Discography",
		map[string]string{
			"Song One/song.ini":  "[song]\nname = One\nartist = Band\n",
			"Song One/notes.mid": "one chart bytes",
			"Song Two/song.ini":  "[song]\nname = Two\nartist = Band\n",
			"Song Two/notes.mid": "two chart bytes",
		})
	writePartZip(t, filepath.Join(root, "Discography-20261101T183503Z-1-002.zip"), "Discography",
		map[string]string{
			"Song One/song.ogg": "one audio",
			"Song Two/song.ogg": "two audio",
		})

	return root, Options{LibraryRoot: root}
}

func TestOnePartOfASetIsRefusedRatherThanUnpacked(t *testing.T) {
	// The measured case. Part one reports its songs and looks complete, so unpacking it
	// succeeds, passes the song-count check, and leaves a library missing every audio file.
	// Failing loudly is a far better problem than a library that is quietly wrong.
	root, o := splitLibrary(t)

	_, err := o.Inspect("Discography-20261101T183503Z-1-001.zip")
	if err == nil {
		t.Fatal("unpacking one part of a set was allowed")
	}
	if !strings.Contains(err.Error(), "part of a 2-part set") {
		t.Errorf("the refusal does not explain itself: %v", err)
	}
	if !strings.Contains(err.Error(), "run-set") {
		t.Errorf("the refusal does not say what to do instead: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(root, "Discography")); !os.IsNotExist(serr) {
		t.Error("something was unpacked despite the refusal")
	}
}

func TestASetUnpacksIntoOneDirectory(t *testing.T) {
	root, o := splitLibrary(t)

	sets, err := FindSets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 1 || len(sets[0].Parts) != 2 {
		t.Fatalf("sets = %+v, want one set of two parts", sets)
	}

	res, err := o.RunSet(root, sets[0])
	if err != nil {
		t.Fatal(err)
	}
	if res.SongsFound != 2 {
		t.Errorf("songs = %d, want 2", res.SongsFound)
	}

	// One directory, and every song whole - the chart from part one beside the audio from
	// part two. Unpacked separately these would be two directories, neither playable.
	//
	// The repeated "Discography/Discography" is not a typo and not this feature's doing: an
	// archive carrying its own top-level folder lands inside a destination named after it.
	// That is true of every unpack and is a separate, known wart; asserting the flattened
	// path here would be asserting a behaviour that does not exist.
	for _, f := range []string{
		"Discography/Discography/Song One/notes.mid", "Discography/Discography/Song One/song.ogg",
		"Discography/Discography/Song Two/notes.mid", "Discography/Discography/Song Two/song.ogg",
	} {
		if _, serr := os.Stat(filepath.Join(root, f)); serr != nil {
			t.Errorf("%s is missing: %v", f, serr)
		}
	}
	if _, serr := os.Stat(filepath.Join(root, "Discography (2)")); !os.IsNotExist(serr) {
		t.Error("the parts landed in separate directories")
	}
}

func TestASinglePartExportIsAnOrdinaryArchive(t *testing.T) {
	// Plenty of Takeout exports come back as one file. Treating those as a one-part "set"
	// would refuse to unpack a perfectly ordinary archive - measured on the real library,
	// where six of nine Takeout-named archives were single.
	root := t.TempDir()
	writePartZip(t, filepath.Join(root, "Album-20261101T182804Z-1-001.zip"), "Album",
		map[string]string{
			"Song/song.ini":  "[song]\nname = Only\nartist = Band\n",
			"Song/notes.mid": "chart bytes",
			"Song/song.ogg":  "audio",
		})

	sets, err := FindSets(root)
	if err != nil {
		t.Fatal(err)
	}
	if len(sets) != 0 {
		t.Errorf("a single-part export was treated as a set: %+v", sets)
	}

	o := Options{LibraryRoot: root}
	if _, err := o.Run("Album-20261101T182804Z-1-001.zip"); err != nil {
		t.Errorf("a single-part export would not unpack: %v", err)
	}
}

func TestTwoPartsClaimingTheSameFileIsRefused(t *testing.T) {
	// Merging is not overwriting. Two parts holding the same path means the set is not what it
	// appears to be, and keeping whichever arrived last would hide that.
	root := t.TempDir()
	common := map[string]string{
		"Song/song.ini":  "[song]\nname = One\nartist = Band\n",
		"Song/notes.mid": "chart bytes",
	}
	writePartZip(t, filepath.Join(root, "Dup-20261101T183503Z-1-001.zip"), "Dup", common)
	writePartZip(t, filepath.Join(root, "Dup-20261101T183503Z-1-002.zip"), "Dup", common)

	o := Options{LibraryRoot: root}
	sets, err := FindSets(root)
	if err != nil || len(sets) != 1 {
		t.Fatalf("sets = %+v, err = %v", sets, err)
	}
	if _, err := o.RunSet(root, sets[0]); err == nil {
		t.Fatal("two parts claiming the same file was allowed")
	}
	if _, serr := os.Stat(filepath.Join(root, "Dup")); !os.IsNotExist(serr) {
		t.Error("a half-merged directory was kept")
	}
}

func TestPartsAreOrderedNumericallyNotLexically(t *testing.T) {
	// "-010" sorts before "-002" as text. Extraction order is not merely cosmetic: it decides
	// which part is read first, and a solid archive read out of order is the pathology this
	// whole area exists to avoid.
	root := t.TempDir()
	for _, n := range []string{"001", "002", "010"} {
		writePartZip(t, filepath.Join(root, "Big-20261101T183503Z-1-"+n+".zip"), "Big",
			map[string]string{"f" + n + ".txt": n})
	}
	sets, err := FindSets(root)
	if err != nil || len(sets) != 1 {
		t.Fatalf("sets = %+v, err = %v", sets, err)
	}
	want := []string{
		"Big-20261101T183503Z-1-001.zip",
		"Big-20261101T183503Z-1-002.zip",
		"Big-20261101T183503Z-1-010.zip",
	}
	for i, p := range sets[0].Parts {
		if p != want[i] {
			t.Errorf("part %d = %s, want %s", i, p, want[i])
		}
	}
}

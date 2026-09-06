package scan

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// The song used throughout. Kept identical between the loose and archived
// forms, because the whole question these tests answer is whether the CONTAINER
// changes anything, and any difference in content would mask that.
func songFiles() map[string]string {
	return map[string]string{
		"song.ini":  "[Song]\nname=Container Test\nartist=Measurement\n",
		"notes.mid": "MThd\x00\x00\x00\x06\x00\x01\x00\x01\x01\xe0MTrk\x00\x00\x00\x04\x00\xff\x2f\x00",
		"song.ogg":  "not really ogg, but the packer copies bytes",
		"album.png": "not really png either",
	}
}

func writeLoose(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// writeZip builds a zip. `prefix` puts the song in a subfolder, which is how
// almost every zipped song in the wild is actually shaped. `order` fixes the
// entry order so a test can vary it deliberately.
func writeZip(t *testing.T, files map[string]string, prefix string, order []string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "song.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	if order == nil {
		for n := range files {
			order = append(order, n)
		}
	}
	for _, name := range order {
		w, err := zw.Create(prefix + name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := w.Write([]byte(files[name])); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	return p
}

func packTo(t *testing.T, path string) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := PackPath(path, &buf); err != nil {
		t.Fatalf("PackPath(%s): %v", path, err)
	}
	return buf.Bytes()
}

// THE test for this feature. If a song packs differently because somebody
// zipped it, then reorganising a library silently invalidates every client's
// copy of every song in it - the same class of failure as the random mask,
// arrived at from a different direction.
func TestAZippedSongPacksToTheSameBytesAsTheLooseFolder(t *testing.T) {
	files := songFiles()
	loose := packTo(t, writeLoose(t, files))
	zipped := packTo(t, writeZip(t, files, "Container Test/", nil))

	if !bytes.Equal(loose, zipped) {
		t.Fatalf("a zipped song packed to %d bytes and the same song loose packed to %d; "+
			"changing how a library is stored must not change what clients receive",
			len(zipped), len(loose))
	}
}

// The same claim for 7z, and the reason the fixture is committed rather than
// generated: the sevenzip library is READ-only, so Go cannot build a .7z at test
// time. testdata/song.7z was made by py7zr from exactly the bytes songFiles()
// returns - if you regenerate it, regenerate it from those bytes or this test
// starts failing for reasons that have nothing to do with 7z.
func TestA7zSongPacksToTheSameBytesAsTheLooseFolder(t *testing.T) {
	loose := packTo(t, writeLoose(t, songFiles()))
	seven := packTo(t, "testdata/song.7z")

	if !bytes.Equal(loose, seven) {
		t.Fatalf("a 7z song packed to %d bytes and the same song loose packed to %d",
			len(seven), len(loose))
	}
}

// The strongest form of the claim: three containers, one set of bytes. This is
// what lets an operator restructure a library - unzip everything, or zip it -
// without every client re-downloading every song.
func TestAllThreeContainersProduceTheSameArchive(t *testing.T) {
	files := songFiles()
	got := map[string][]byte{
		"loose folder": packTo(t, writeLoose(t, files)),
		"zip":          packTo(t, writeZip(t, files, "Container Test/", nil)),
		"7z":           packTo(t, "testdata/song.7z"),
	}
	ref := got["loose folder"]
	for name, b := range got {
		if !bytes.Equal(ref, b) {
			t.Errorf("%s differs from the loose folder (%d vs %d bytes)", name, len(b), len(ref))
		}
	}
}

// Zip entry order is whatever the tool that made the archive chose, and it is
// not stable across tools. The packer sorts, so it must not leak through.
func TestZipEntryOrderDoesNotChangeTheArchive(t *testing.T) {
	files := songFiles()
	forward := writeZip(t, files, "S/", []string{"song.ini", "notes.mid", "song.ogg", "album.png"})
	reverse := writeZip(t, files, "S/", []string{"album.png", "song.ogg", "notes.mid", "song.ini"})

	if !bytes.Equal(packTo(t, forward), packTo(t, reverse)) {
		t.Fatal("the order of entries inside the zip changed the packed archive")
	}
}

// The three shapes a zipped song actually arrives in.
func TestSongIsFoundAtTheRootAndOneFolderDown(t *testing.T) {
	files := songFiles()
	for _, tc := range []struct{ name, prefix string }{
		{"chart at the archive root", ""},
		{"one wrapper folder", "Song Name/"},
		{"two wrapper folders", "Downloads/Song Name/"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			song, err := ScanContainer(writeZip(t, files, tc.prefix, nil))
			if err != nil {
				t.Fatalf("ScanContainer: %v", err)
			}
			if song.Name != "Container Test" {
				t.Errorf("name = %q", song.Name)
			}
		})
	}
}

// A zip holding several songs is refused rather than resolved. Picking one
// would publish it and silently drop the rest, and the operator would have no
// way to tell that had happened.
func TestAZipOfSeveralSongsIsRefusedRatherThanGuessed(t *testing.T) {
	p := filepath.Join(t.TempDir(), "pack.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, folder := range []string{"First Song/", "Second Song/"} {
		for name, body := range songFiles() {
			w, err := zw.Create(folder + name)
			if err != nil {
				t.Fatal(err)
			}
			w.Write([]byte(body))
		}
	}
	zw.Close()
	f.Close()

	_, err = ScanContainer(p)
	if !errors.Is(err, ErrTooManySongs) {
		t.Fatalf("err = %v, want ErrTooManySongs", err)
	}
}

// A library contains archives that are not songs. Those are not errors, and
// reporting them as problems would bury the real ones.
func TestAZipOfSomethingElseIsNotASong(t *testing.T) {
	p := writeZip(t, map[string]string{"readme.txt": "hello", "photo.jpg": "x"}, "", nil)
	if _, err := ScanContainer(p); !errors.Is(err, ErrNoChart) {
		t.Fatalf("err = %v, want ErrNoChart", err)
	}
}

// `_rb3con` is the important one: those files usually carry NO extension at
// all, so anything matching on filepath.Ext misses the shape that turns up most.
func TestRockBandPackagesAreRecognisedByShapeNotJustExtension(t *testing.T) {
	for _, name := range []string{
		"song.con", "SONG.CON",
		"somesong_rb3con", "Some Song_RB3CON",
		"pack.pkg", "default.xex",
	} {
		if !IsRockBandPackage(name) {
			t.Errorf("%q not recognised as a console package", name)
		}
	}
	for _, name := range []string{
		"song.sng", "song.zip", "song.7z", "notes.mid", "reconstruction.txt",
	} {
		if IsRockBandPackage(name) {
			t.Errorf("%q wrongly treated as a console package", name)
		}
	}
}

// Recognised and REPORTED. A console package that is silently ignored looks
// exactly like a server that failed to scan.
func TestAConsolePackageIsReportedWithAReason(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "Some Song_rb3con"), []byte("CON "), 0o644); err != nil {
		t.Fatal(err)
	}

	var got []Result
	if err := WalkLibrary(root, func(r Result) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 {
		t.Fatalf("got %d results, want exactly one refusal", len(got))
	}
	if !errors.Is(got[0].Err, ErrRockBandPackage) {
		t.Fatalf("err = %v, want ErrRockBandPackage", got[0].Err)
	}
	if got[0].Song != nil {
		t.Error("a refused package must not produce a song")
	}
}

// The walk has to find a song inside an archive, not just next to one.
func TestWalkLibraryFindsAZippedSong(t *testing.T) {
	root := t.TempDir()
	files := songFiles()
	src := writeZip(t, files, "Song/", nil)
	body, err := os.ReadFile(src)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Some Song.zip"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	var songs []Result
	if err := WalkLibrary(root, func(r Result) { songs = append(songs, r) }); err != nil {
		t.Fatal(err)
	}
	if len(songs) != 1 || songs[0].Song == nil {
		t.Fatalf("got %+v, want one song", songs)
	}
	if songs[0].Song.Name != "Container Test" {
		t.Errorf("name = %q", songs[0].Song.Name)
	}
}

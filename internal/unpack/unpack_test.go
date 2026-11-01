package unpack

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// The byte that cost vault2 an archive: 0xA0, "á" in CP437.
const legacyFolder = "Man\xa0 - Oye Mi Amor"

// writeZip builds a library containing one archive of several songs.
func writeZip(t *testing.T, name string, songs map[string]map[string]string, legacy bool) string {
	t.Helper()
	root := t.TempDir()
	f, err := os.Create(filepath.Join(root, name))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for folder, files := range songs {
		for n, body := range files {
			h := &zip.FileHeader{Name: folder + "/" + n, Method: zip.Deflate}
			h.NonUTF8 = legacy
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
	return root
}

func twoSongs() map[string]map[string]string {
	return map[string]map[string]string{
		"Alpha Song": {
			"song.ini":  "[song]\nname = Alpha\nartist = First\n",
			"notes.mid": "alpha chart bytes",
			"song.ogg":  "alpha audio",
		},
		"Beta Song": {
			"song.ini":  "[song]\nname = Beta\nartist = Second\n",
			"notes.mid": "beta chart bytes",
			"song.ogg":  "beta audio",
		},
	}
}

func TestUnpackingProducesLooseFoldersForEverySong(t *testing.T) {
	root := writeZip(t, "pack.zip", twoSongs(), false)

	res, err := (Options{LibraryRoot: root}).Run("pack.zip")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Songs != 2 || res.SongsFound != 2 {
		t.Errorf("archive held %d songs, %d found after extracting; want 2 and 2",
			res.Songs, res.SongsFound)
	}

	for _, folder := range []string{"Alpha Song", "Beta Song"} {
		p := filepath.Join(root, "pack", folder, "notes.mid")
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s was not written: %v", p, err)
		}
	}
}

func TestUnpackingNeverTouchesTheArchive(t *testing.T) {
	// The operator deletes the archive, later, having seen it worked. Not us, and not now.
	root := writeZip(t, "pack.zip", twoSongs(), false)
	archive := filepath.Join(root, "pack.zip")

	before, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := (Options{LibraryRoot: root}).Run("pack.zip"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	after, err := os.ReadFile(archive)
	if err != nil {
		t.Fatalf("the archive is gone after unpacking: %v", err)
	}
	if string(before) != string(after) {
		t.Error("the archive changed during unpacking")
	}
}

func TestUnpackingWritesDecodedNamesAndSaysSoFirst(t *testing.T) {
	songs := map[string]map[string]string{
		legacyFolder: {
			"song.ini":  "[song]\nname = Oye Mi Amor\nartist = Mana\n",
			"notes.mid": "legacy chart bytes",
			"song.ogg":  "legacy audio",
		},
	}
	root := writeZip(t, "rb.zip", songs, true)

	// Checking must disclose the permanent consequence BEFORE anything is written, which is
	// the entire reason check and unpack are two operations.
	plan, err := (Options{LibraryRoot: root}).Inspect("rb.zip")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if len(plan.NameChanges) == 0 {
		t.Fatal("Inspect reported no name changes for an archive full of them")
	}
	var sawFolder bool
	for _, c := range plan.NameChanges {
		if c.Written == "Maná - Oye Mi Amor/song.ini" {
			sawFolder = true
		}
	}
	if !sawFolder {
		t.Errorf("name changes did not mention the decoded folder: %+v", plan.NameChanges)
	}

	if _, err := (Options{LibraryRoot: root}).Run("rb.zip"); err != nil {
		t.Fatalf("Run: %v", err)
	}
	if _, err := os.Stat(filepath.Join(root, "rb", "Maná - Oye Mi Amor", "notes.mid")); err != nil {
		t.Errorf("the decoded folder was not created: %v", err)
	}
}

func TestUnpackingNeverOverwritesWhatIsAlreadyThere(t *testing.T) {
	root := writeZip(t, "pack.zip", twoSongs(), false)

	// Somebody extracted it by hand already, or another archive shares the name.
	existing := filepath.Join(root, "pack")
	if err := os.MkdirAll(existing, 0o755); err != nil {
		t.Fatal(err)
	}
	keep := filepath.Join(existing, "do-not-lose-me.txt")
	if err := os.WriteFile(keep, []byte("mine"), 0o644); err != nil {
		t.Fatal(err)
	}

	res, err := (Options{LibraryRoot: root}).Run("pack.zip")
	if err != nil {
		t.Fatalf("Run: %v", err)
	}
	if res.Dest == "pack" {
		t.Error("unpacked straight into the existing directory")
	}
	body, err := os.ReadFile(keep)
	if err != nil || string(body) != "mine" {
		t.Errorf("the existing directory was disturbed: %q, %v", body, err)
	}
}

func TestUnpackingRefusesAPathOutsideTheLibrary(t *testing.T) {
	root := writeZip(t, "pack.zip", twoSongs(), false)

	for _, bad := range []string{
		"../escape.zip",
		"../../etc/passwd",
		"sub/../../escape.zip",
	} {
		if _, err := (Options{LibraryRoot: root}).Inspect(bad); err == nil {
			t.Errorf("Inspect(%q) was allowed", bad)
		}
		if _, err := (Options{LibraryRoot: root}).Run(bad); err == nil {
			t.Errorf("Run(%q) was allowed", bad)
		}
	}
}

func TestAHalfWrittenDirectoryIsNotIndexed(t *testing.T) {
	// The property that makes a crash survivable. Without it the walk finds a song folder
	// whose audio has not arrived yet and publishes a chart hash for it.
	root := t.TempDir()
	staging := filepath.Join(root, "pack"+scan.InProgressSuffix, "Alpha Song")
	if err := os.MkdirAll(staging, 0o755); err != nil {
		t.Fatal(err)
	}
	for n, body := range map[string]string{
		"song.ini":  "[song]\nname = Alpha\nartist = First\n",
		"notes.mid": "alpha chart bytes",
	} {
		if err := os.WriteFile(filepath.Join(staging, n), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var songs int
	if err := scan.WalkLibrary(root, func(r scan.Result) {
		if r.Err == nil && r.Song != nil {
			songs++
		}
	}); err != nil {
		t.Fatal(err)
	}
	if songs != 0 {
		t.Errorf("the walk indexed %d songs out of a directory still being written", songs)
	}
}

func TestInspectDecompressesNothing(t *testing.T) {
	// Checking has to be cheap enough to run from a phone on a whim. It reads headers: the
	// entry count and byte total come from the central directory, and the song count from the
	// stored names.
	root := writeZip(t, "pack.zip", twoSongs(), false)

	plan, err := (Options{LibraryRoot: root}).Inspect("pack.zip")
	if err != nil {
		t.Fatalf("Inspect: %v", err)
	}
	if plan.Songs != 2 {
		t.Errorf("Songs = %d, want 2", plan.Songs)
	}
	if plan.Entries != 6 {
		t.Errorf("Entries = %d, want 6", plan.Entries)
	}
	if plan.Bytes == 0 {
		t.Error("Bytes = 0, so the operator cannot tell whether the disk will take it")
	}
	if plan.Dest != "pack" {
		t.Errorf("Dest = %q, want %q", plan.Dest, "pack")
	}
	if _, err := os.Stat(filepath.Join(root, "pack")); !os.IsNotExist(err) {
		t.Error("Inspect created something")
	}
}

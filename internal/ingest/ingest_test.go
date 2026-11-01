package ingest

import (
	"archive/zip"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// aged writes a file and backdates it past the quiet period, because a test that waited a real
// minute for each case would not be run.
func aged(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func agedSongFolder(t *testing.T, dir, name string) {
	t.Helper()
	aged(t, filepath.Join(dir, "song.ini"), "[song]\nname = "+name+"\nartist = Someone\n")
	aged(t, filepath.Join(dir, "notes.mid"), name+" chart bytes")
	aged(t, filepath.Join(dir, "song.ogg"), name+" audio")
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(dir, old, old); err != nil {
		t.Fatal(err)
	}
}

func agedZip(t *testing.T, path string, folders ...string) {
	t.Helper()
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, folder := range folders {
		for name, body := range map[string]string{
			"song.ini":  "[song]\nname = " + folder + "\nartist = Someone\n",
			"notes.mid": folder + " chart bytes",
			"song.ogg":  folder + " audio",
		} {
			w, werr := zw.Create(folder + "/" + name)
			if werr != nil {
				t.Fatal(werr)
			}
			if _, werr := w.Write([]byte(body)); werr != nil {
				t.Fatal(werr)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	old := time.Now().Add(-10 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatal(err)
	}
}

func newLibrary(t *testing.T) (root, in string, o Options) {
	t.Helper()
	root = t.TempDir()
	in = filepath.Join(root, scan.IncomingDirName)
	if err := os.MkdirAll(in, 0o755); err != nil {
		t.Fatal(err)
	}
	return root, in, Options{LibraryRoot: root}
}

func TestASongFolderIsMovedIntoTheLibrary(t *testing.T) {
	root, in, o := newLibrary(t)
	agedSongFolder(t, filepath.Join(in, "Dropped Song"), "Dropped")

	res, err := o.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Taken) != 1 || res.Taken[0].Err != "" {
		t.Fatalf("taken = %+v", res.Taken)
	}
	if _, err := os.Stat(filepath.Join(root, "Dropped Song", "notes.mid")); err != nil {
		t.Errorf("not in the library: %v", err)
	}
	// Moved, not copied: a drop folder that still holds everything ever dropped into it stops
	// being a drop folder.
	if _, err := os.Stat(filepath.Join(in, "Dropped Song")); !os.IsNotExist(err) {
		t.Error("the original is still in the drop folder")
	}
}

func TestAnArchiveIsUnpackedAndThenParked(t *testing.T) {
	root, in, o := newLibrary(t)
	agedZip(t, filepath.Join(in, "Pack.zip"), "Alpha", "Beta")

	res, err := o.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Taken) != 1 || res.Taken[0].Err != "" {
		t.Fatalf("taken = %+v", res.Taken)
	}
	if res.Taken[0].Songs != 2 {
		t.Errorf("songs = %d, want 2", res.Taken[0].Songs)
	}
	for _, folder := range []string{"Alpha", "Beta"} {
		if _, err := os.Stat(filepath.Join(root, "Pack", folder, "notes.mid")); err != nil {
			t.Errorf("%s was not unpacked: %v", folder, err)
		}
	}
	// Parked rather than deleted, and out of the drop folder either way.
	if _, err := os.Stat(filepath.Join(root, scan.IgnoredDirName, "Pack.zip")); err != nil {
		t.Errorf("the archive was not parked: %v", err)
	}
	if _, err := os.Stat(filepath.Join(in, "Pack.zip")); !os.IsNotExist(err) {
		t.Error("the archive is still in the drop folder")
	}
}

func TestSomethingStillArrivingIsLeftAlone(t *testing.T) {
	// The failure this prevents is the quiet one: a truncated chart still hashes to something,
	// and that something is published as a song no client can play.
	_, in, o := newLibrary(t)

	// Written now, so it is inside the quiet period.
	fresh := filepath.Join(in, "Arriving Song")
	if err := os.MkdirAll(fresh, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range map[string]string{
		"song.ini":  "[song]\nname = Arriving\nartist = Someone\n",
		"notes.mid": "chart",
		"song.ogg":  "audio",
	} {
		if err := os.WriteFile(filepath.Join(fresh, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	res, err := o.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Taken) != 0 {
		t.Errorf("took in %d items that were still arriving", len(res.Taken))
	}
	if len(res.Skipped) != 1 || res.Skipped[0].Reason == "" {
		t.Fatalf("skipped = %+v, want one with a reason", res.Skipped)
	}
}

func TestAnUnfinishedDownloadIsLeftAlone(t *testing.T) {
	// A paused download can sit untouched for hours, so the quiet period alone would take it.
	_, in, o := newLibrary(t)
	aged(t, filepath.Join(in, "Song.sng.part"), "half a file")
	aged(t, filepath.Join(in, "Other.zip.crdownload"), "half a file")

	res, err := o.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Taken) != 0 {
		t.Errorf("took in %d unfinished downloads", len(res.Taken))
	}
	if len(res.Skipped) != 2 {
		t.Errorf("skipped %d, want 2", len(res.Skipped))
	}
}

func TestAnArrivalNeverOverwritesWhatIsAlreadyThere(t *testing.T) {
	// Two people can drop a folder with the same name and mean different songs.
	root, in, o := newLibrary(t)
	agedSongFolder(t, filepath.Join(root, "Dropped Song"), "Original")
	agedSongFolder(t, filepath.Join(in, "Dropped Song"), "Newcomer")

	if _, err := o.Run(); err != nil {
		t.Fatal(err)
	}

	body, err := os.ReadFile(filepath.Join(root, "Dropped Song", "song.ini"))
	if err != nil {
		t.Fatal(err)
	}
	if want := "name = Original"; !contains(string(body), want) {
		t.Errorf("the original was overwritten: %q", body)
	}
	if _, err := os.Stat(filepath.Join(root, "Dropped Song (2)", "notes.mid")); err != nil {
		t.Errorf("the newcomer was not kept alongside: %v", err)
	}
}

func TestSomethingThatIsNotASongIsReportedRatherThanMoved(t *testing.T) {
	// A drop folder collects whatever was on the USB stick. Silence would leave somebody
	// wondering why nothing happened.
	_, in, o := newLibrary(t)
	aged(t, filepath.Join(in, "notes.txt"), "just my notes")
	aged(t, filepath.Join(in, "Empty Folder", "readme.md"), "nothing here")

	res, err := o.Run()
	if err != nil {
		t.Fatal(err)
	}
	if len(res.Taken) != 0 {
		t.Errorf("took in %d things that are not songs", len(res.Taken))
	}
	if len(res.Skipped) != 2 {
		t.Fatalf("skipped %d, want 2: %+v", len(res.Skipped), res.Skipped)
	}
	for _, s := range res.Skipped {
		if s.Reason == "" {
			t.Errorf("%s was skipped with no reason given", s.Name)
		}
	}
}

func TestInspectWritesNothing(t *testing.T) {
	root, in, o := newLibrary(t)
	agedSongFolder(t, filepath.Join(in, "Dropped Song"), "Dropped")
	agedZip(t, filepath.Join(in, "Pack.zip"), "Alpha")

	plan, err := o.Inspect()
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Items) != 2 {
		t.Fatalf("items = %d, want 2", len(plan.Items))
	}
	if _, err := os.Stat(filepath.Join(root, "Dropped Song")); !os.IsNotExist(err) {
		t.Error("Inspect moved something")
	}
	if _, err := os.Stat(filepath.Join(in, "Pack.zip")); err != nil {
		t.Error("Inspect disturbed the drop folder")
	}
}

func contains(s, sub string) bool {
	return len(s) >= len(sub) && (func() bool {
		for i := 0; i+len(sub) <= len(s); i++ {
			if s[i:i+len(sub)] == sub {
				return true
			}
		}
		return false
	})()
}

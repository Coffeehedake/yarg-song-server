package scan

import (
	"os"
	"path/filepath"
	"testing"
)

// plantSong writes a playable-looking song folder at dir.
func plantSong(t *testing.T, dir, name string) {
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

func songsFound(t *testing.T, root string) int {
	t.Helper()
	n := 0
	if err := WalkLibrary(root, func(r Result) {
		if r.Err == nil && r.Song != nil {
			n++
		}
	}); err != nil {
		t.Fatal(err)
	}
	return n
}

func TestReservedDirectoriesAreNotIndexed(t *testing.T) {
	// Each of these would cause a different kind of harm if walked: a song published from a
	// half-extracted archive, an archive paid for on every restart, and a chart hash
	// published for a file still being copied in over the network.
	for _, dir := range []string{
		IgnoredDirName,
		IncomingDirName,
		"Some Pack" + InProgressSuffix,
	} {
		t.Run(dir, func(t *testing.T) {
			root := t.TempDir()
			plantSong(t, filepath.Join(root, "Real Song"), "Real")
			plantSong(t, filepath.Join(root, dir, "Hidden Song"), "Hidden")

			if got := songsFound(t, root); got != 1 {
				t.Errorf("found %d songs, want 1 - %s must not be indexed", got, dir)
			}
		})
	}
}

func TestReservedDirectoriesAreSkippedAtAnyDepth(t *testing.T) {
	// Buried, because a library is somebody's folder tree and nobody keeps these at the top.
	root := t.TempDir()
	plantSong(t, filepath.Join(root, "Real Song"), "Real")
	plantSong(t, filepath.Join(root, "Rock Band 3", IncomingDirName, "Hidden"), "Hidden")
	plantSong(t, filepath.Join(root, "A", "B", IgnoredDirName, "Hidden Too"), "HiddenToo")

	if got := songsFound(t, root); got != 1 {
		t.Errorf("found %d songs, want 1", got)
	}
}

func TestAReservedDirectoryGivenAsTheRootIsStillScanned(t *testing.T) {
	// Pointing the scanner straight at one is deliberate - looking at what is parked, or
	// scanning a staging area before taking it in. Refusing to read the directory it was
	// explicitly handed would be obtuse, and would make the parked archives unexaminable.
	root := filepath.Join(t.TempDir(), IgnoredDirName)
	plantSong(t, filepath.Join(root, "Parked Song"), "Parked")

	if got := songsFound(t, root); got != 1 {
		t.Errorf("found %d songs, want 1 - the root itself is never skipped", got)
	}
}

func TestIsReservedDirNamesOnlyWhatItShould(t *testing.T) {
	for _, name := range []string{"_packed", "_incoming", "Thing.unpacking"} {
		if !IsReservedDir(name) {
			t.Errorf("IsReservedDir(%q) = false, want true", name)
		}
	}
	// Near misses must NOT be reserved, or a real song folder disappears without a word.
	for _, name := range []string{
		"packed", "incoming", "_packed songs", "Green Day _packed",
		"unpacking", "_Packed", "my_incoming",
	} {
		if IsReservedDir(name) {
			t.Errorf("IsReservedDir(%q) = true, want false", name)
		}
	}
}

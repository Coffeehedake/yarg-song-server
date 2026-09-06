package scan

// Ingesting archives means reading files an operator downloaded from the
// internet. These tests pin the behaviour that makes that safe, and the one
// failure mode that was found by probing rather than by reasoning.
//
// Every assertion here was MEASURED first. The behaviours are Go's, not ours -
// which is exactly why they need pinning: we now depend on them for a security
// property, and a dependency you did not write is one that can change under you.

import (
	"archive/zip"
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// rawZip writes entry names VERBATIM. zip.Writer.Create sanitises names; a
// hostile archive is written by something that does not, so the test has to
// bypass the sanitising path or it proves nothing.
func rawZip(t *testing.T, entries map[string]string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "probe.zip")
	f, err := os.Create(p)
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for name, body := range entries {
		w, err := zw.CreateHeader(&zip.FileHeader{Name: name, Method: zip.Store})
		if err != nil {
			continue // refused at write time is also a fine outcome
		}
		if _, err := w.Write([]byte(body)); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()
	return p
}

// A malicious archive must not get a path traversal into the .sng we serve.
//
// We never extract to disk, so the classic zip-slip does not apply, but the
// names inside the archive we PRODUCE are attacker-influenced and reach every
// client. Go's zip filesystem drops entries that fail fs.ValidPath, which is
// what makes this safe; this test is here so that if that ever changes, it
// changes loudly.
func TestTraversalEntriesNeverReachTheServedArchive(t *testing.T) {
	entries := map[string]string{
		"../../../../etc/passwd": "root:x:0:0",
		"Song/../../escape.txt":  "escaped",
		"/etc/shadow":            "x",
	}
	for k, v := range songFiles() {
		entries["Song/"+k] = v
	}

	var buf bytes.Buffer
	if err := PackPath(rawZip(t, entries), &buf); err != nil {
		t.Fatalf("PackPath: %v", err)
	}

	for _, forbidden := range []string{"passwd", "shadow", "escape", "..", "etc/"} {
		if bytes.Contains(buf.Bytes(), []byte(forbidden)) {
			t.Errorf("the served archive contains %q, which came from a hostile entry name", forbidden)
		}
	}
	// And the legitimate files are still all there - a defence that also drops
	// the real content would be a different bug.
	for _, want := range []string{"song.ogg", "notes.mid", "album.png"} {
		if !bytes.Contains(buf.Bytes(), []byte(want)) {
			t.Errorf("legitimate file %q missing from the served archive", want)
		}
	}
}

// A corrupt archive is reported and the scan continues. One bad file must not
// abandon a walk of ten thousand songs.
func TestACorruptArchiveIsReportedAndTheWalkContinues(t *testing.T) {
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "bad.zip"), []byte("PK\x03\x04 not a zip"), 0o644); err != nil {
		t.Fatal(err)
	}
	good := filepath.Join(root, "Good Song")
	if err := os.MkdirAll(good, 0o755); err != nil {
		t.Fatal(err)
	}
	for name, body := range songFiles() {
		if err := os.WriteFile(filepath.Join(good, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}

	var songs, problems int
	if err := WalkLibrary(root, func(r Result) {
		if r.Err != nil {
			problems++
		} else if r.Song != nil {
			songs++
		}
	}); err != nil {
		t.Fatalf("WalkLibrary returned a fatal error for one bad file: %v", err)
	}
	if songs != 1 {
		t.Errorf("got %d songs, want the good one to survive the bad archive", songs)
	}
	if problems != 1 {
		t.Errorf("got %d problems, want the corrupt archive reported", problems)
	}
}

// FOUND BY PROBING, not by reasoning: some Windows tools write zip entries with
// BACKSLASH separators. Go's zip filesystem surfaces a directory for those but
// no readable file underneath, so the chart is never found - and because "no
// chart in a zip" is silently ignored (libraries are full of archives that are
// not songs), a perfectly good song used to disappear with no message at all.
//
// That is the same failure as silently skipping a Rock Band package, which this
// project already decided was unacceptable. So an archive that VISIBLY contains
// a song and still cannot be read is now reported.
func TestAnArchiveThatVisiblyHoldsASongIsNeverSilentlyIgnored(t *testing.T) {
	entries := map[string]string{}
	for k, v := range songFiles() {
		entries["Song\\"+k] = v // backslashes, as the old tools write them
	}
	p := rawZip(t, entries)

	_, err := ScanContainer(p)
	if !errors.Is(err, ErrUnreadableArchive) {
		t.Fatalf("err = %v, want ErrUnreadableArchive", err)
	}

	// And the walk must surface it rather than swallow it.
	root := t.TempDir()
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "Some Song.zip"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	var got []Result
	if err := WalkLibrary(root, func(r Result) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 1 || !errors.Is(got[0].Err, ErrUnreadableArchive) {
		t.Fatalf("got %+v, want one ErrUnreadableArchive report", got)
	}
}

// The other half of that decision: an archive of genuinely unrelated files must
// still be ignored quietly, or every holiday-photos zip in a library becomes a
// problem entry and the real problems get buried.
func TestAnArchiveOfNonSongFilesIsStillIgnoredQuietly(t *testing.T) {
	root := t.TempDir()
	p := rawZip(t, map[string]string{"holiday/photo.jpg": "x", "holiday/notes.txt.bak": "y"})
	body, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "photos.zip"), body, 0o644); err != nil {
		t.Fatal(err)
	}

	var got []Result
	if err := WalkLibrary(root, func(r Result) { got = append(got, r) }); err != nil {
		t.Fatal(err)
	}
	if len(got) != 0 {
		t.Fatalf("got %+v, want silence for an archive that is not a song", got)
	}
}

// An empty archive is not a song and not a problem.
func TestAnEmptyArchiveIsNotASong(t *testing.T) {
	if _, err := ScanContainer(rawZip(t, map[string]string{})); !errors.Is(err, ErrNoChart) {
		t.Fatalf("err = %v, want ErrNoChart", err)
	}
}

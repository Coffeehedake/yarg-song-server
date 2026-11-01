package scan

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bodgit/sevenzip"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// password is the YARN archive password, derived by the published formula. Duplicated in this
// test rather than imported so internal/scan does not depend on internal/yarn, which would be
// a dependency from the scanner to a package that makes HTTP requests.
func password() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	out := make([]byte, 0, 64)
	for i := int32(0); i < 64; i++ {
		a := byte(5 + i*104729)
		b := byte(9 + i*224737)
		out = append(out, letters[(a%b)%52])
	}
	return string(out)
}

func extractOneYargsong(t *testing.T, dir string) string {
	t.Helper()
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "yarn-probe-*.7z"))
	if len(matches) == 0 {
		t.Skip("no cached YARN archive; run the yarn probe first")
	}
	r, err := sevenzip.OpenReaderWithPassword(matches[0], password())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	dst := filepath.Join(dir, r.File[0].Name)
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	defer rc.Close()
	f, err := os.Create(dst)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if _, err := io.Copy(f, rc); err != nil {
		t.Fatal(err)
	}
	return dst
}

// TestARealYargsongIndexesAndPacks is the whole wiring in one go: a .yargsong sitting in a
// library folder must be found by the walk, scanned into a song with a chart hash, and packed
// into a .sng a client can read.
//
// Indexing alone would not be enough - a song in the catalogue the server cannot produce bytes
// for is worse than one it never listed.
//
//	YARN_PROBE=1 go test ./internal/scan/ -run TestARealYargsong -v
func TestARealYargsongIndexesAndPacks(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run")
	}

	lib := t.TempDir()
	path := extractOneYargsong(t, lib)
	t.Logf("library contains %s", filepath.Base(path))

	var found []Result
	err := WalkLibrary(lib, func(r Result) { found = append(found, r) })
	if err != nil {
		t.Fatalf("WalkLibrary: %v", err)
	}

	var songs []Result
	for _, r := range found {
		if r.Err != nil {
			t.Errorf("walk reported %s: %v", r.Path, r.Err)
		}
		if r.Song != nil {
			songs = append(songs, r)
		}
	}
	if len(songs) != 1 {
		t.Fatalf("walk found %d songs, want 1", len(songs))
	}

	s := songs[0].Song
	t.Logf("indexed %q by %q chart=%s", s.Name, s.Artist, s.ChartHash)
	if s.Name == "" {
		t.Error("no name")
	}
	if s.ChartHash == "" {
		t.Error("no chart hash - the song could not answer /api/v1/have")
	}

	// And it must be servable, not merely listed.
	var buf bytes.Buffer
	if err := PackPath(path, &buf); err != nil {
		t.Fatalf("PackPath: %v", err)
	}
	t.Logf("packed %d bytes", buf.Len())

	a, err := sng.Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("the packed bytes are not a readable .sng: %v", err)
	}
	if got := a.Metadata["name"]; got != s.Name {
		t.Errorf("packed .sng says name=%q, index says %q", got, s.Name)
	}
	t.Logf("packed .sng parses: name=%q version=%d", a.Metadata["name"], a.Version)
}

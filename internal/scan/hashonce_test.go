package scan

import (
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"testing"
)

// deterministicSong writes a song whose bytes are fixed, so its hashes are too.
//
// Content is written as explicit bytes with \n and no timestamps anywhere, because
// packageHash is SHA-256 over "<lowercased name>:<sha256 of content>" lines and
// nothing else. Any platform that rewrote these bytes would move the pinned hash,
// which is the whole point of pinning it.
func deterministicSong(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	write := func(name, body string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("song.ini", "[song]\nname = Hash Once\nartist = Fixture\nalbum = Pinned\n")
	write("notes.chart", "[Song]\n{\n  Offset = 0\n}\n[ExpertSingle]\n{\n  768 = N 0 0\n}\n")
	write("song.ogg", "audio-bytes-for-the-stem-that-must-be-hashed-exactly-once")
	write("album.png", "not-really-a-png-but-the-bytes-are-what-matter")
	write("extra.bin", "an unclaimed leftover, which collectAssets carries as Other")
	// MIXED CASE ON PURPOSE. packageHash lowercases the name before hashing it,
	// and with an all-lowercase fixture that step is a no-op - so the pin could not
	// tell whether it happened. Red-proofing caught exactly that: deleting the
	// strings.ToLower left the pin GREEN. This file makes the lowercasing
	// load-bearing, so dropping it moves the hash and the pin fails.
	write("ReadMe.TXT", "mixed-case name, so the hash depends on lowercasing it")
	return dir
}

// pinnedPackageHash is package_hash for deterministicSong.
//
// VALIDATED AGAINST THE SPEC, not against our own code: an independent
// implementation of the documented algorithm - SHA-256 over the sorted
// "<lowercased name>:<sha256 of content>" lines - produces the same value. So
// this pin is not merely "whatever the code did when it was written".
// The hash-once change was additionally shown not to move the value: an earlier
// all-lowercase version of this fixture was pinned on the OLD code and still
// passed after the refactor.
//
// package_hash is the ETag AND the sync identity: if it moves, every client's
// cached copy of every song silently stops matching the server. Nothing in this
// repo pinned it before 2026-11-02, which is exactly why a change that alters
// how the hash is COMPUTED without altering its VALUE needed this first.
const pinnedPackageHash = "ec39edc6a292076a7b37fff629588000dbba8a98dfa05e799426f635d7996f62"

// pinnedChartHash is SHA1(chart bytes) for the same fixture - song identity.
const pinnedChartHash = "c51f5ba7e1e393c4a37d26a76248e6e9f9d5e7d2"

func TestPackageHashIsPinnedToAKnownValue(t *testing.T) {
	s, err := ScanDir(os.DirFS(deterministicSong(t)))
	if err != nil {
		t.Fatal(err)
	}
	if s.PackageHash != pinnedPackageHash {
		t.Errorf("package_hash MOVED\n  got  %s\n  want %s\nThis is the ETag and the sync identity; a change here invalidates every client's copy of every song.",
			s.PackageHash, pinnedPackageHash)
	}
	if s.ChartHash != pinnedChartHash {
		t.Errorf("chart_hash MOVED\n  got  %s\n  want %s", s.ChartHash, pinnedChartHash)
	}
}

// countingFS counts Open per file.
//
// It deliberately does NOT embed fs.FS: os.DirFS implements ReadFileFS, and an
// embedded field would promote that method, letting fs.ReadFile bypass the Open
// counter entirely and report a comfortable, wrong answer.
type countingFS struct {
	inner fs.FS
	opens map[string]int
}

func newCountingFS(inner fs.FS) *countingFS {
	return &countingFS{inner: inner, opens: map[string]int{}}
}

func (c *countingFS) Open(name string) (fs.File, error) {
	if name != "." {
		c.opens[name]++
	}
	return c.inner.Open(name)
}

func (c *countingFS) ReadDir(name string) ([]fs.DirEntry, error) {
	return fs.ReadDir(c.inner, name)
}

// TestEachFileIsHashedExactlyOnce is the measurement of the defect, not a
// description of the fix.
//
// scan() called collectAssets and then packageHash over the same name list, and
// both went through assetFor, which is io.Copy(sha256.New(), f) - a full-byte
// read. So every file except the chart and song.ini was read and hashed TWICE on
// every index. Measured on the live library: rchar 238.92 GB against 118.08 GB of
// assets, a ratio of 2.023. See ADR-008.
//
// This test FAILED on the code that shipped before it, reporting 2 for the audio,
// the art and the leftover. That failure is what makes its green worth anything.
func TestEachFileIsHashedExactlyOnce(t *testing.T) {
	c := newCountingFS(os.DirFS(deterministicSong(t)))
	if _, err := ScanDir(c); err != nil {
		t.Fatal(err)
	}

	// song.ini is read once by ScanDir to parse metadata and once by the package
	// hash. Left alone deliberately: it is about a kilobyte, and plumbing its
	// bytes out of ScanDir to save one open would complicate the one path that
	// must stay easy to follow. Audio is where the bytes are.
	want := map[string]int{
		"notes.chart": 1, // read once for identity+preparse, digest primed from those bytes
		"song.ogg":    1,
		"album.png":   1,
		"extra.bin":   1,
		"ReadMe.TXT":  1,
		"song.ini":    2,
	}

	names := make([]string, 0, len(c.opens))
	for n := range c.opens {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		w, known := want[n]
		if !known {
			t.Errorf("unexpected file opened: %s (%d times)", n, c.opens[n])
			continue
		}
		if c.opens[n] != w {
			t.Errorf("%s opened %d time(s), want %d", n, c.opens[n], w)
		}
	}
	for n, w := range want {
		if _, ok := c.opens[n]; !ok {
			t.Errorf("%s was never opened, want %d", n, w)
		}
	}
}

// TestPackingStillAgreesWithTheCatalogue guards the OTHER caller.
//
// packInto derives the .sng mask from packageHash so the archive matches the
// ETag the server sends. That path hashes once already and was not changed, but
// it shares the hash construction - so if the two ever disagree, a client's
// resume across a cache eviction breaks and no single-client test notices.
func TestPackingStillAgreesWithTheCatalogue(t *testing.T) {
	dir := deterministicSong(t)
	s, err := ScanDir(os.DirFS(dir))
	if err != nil {
		t.Fatal(err)
	}
	viaPack, err := packageHash(os.DirFS(dir), []string{
		"ReadMe.TXT", "album.png", "extra.bin", "notes.chart", "song.ini", "song.ogg",
	})
	if err != nil {
		t.Fatal(err)
	}
	if viaPack != s.PackageHash {
		t.Fatalf("the pack path and the scan path disagree about package_hash\n  pack %s\n  scan %s",
			viaPack, s.PackageHash)
	}
}

// TestPrimedChartDigestMatchesAssetFor guards the one new way package_hash could
// move: the chart's digest is now seeded from bytes already in memory rather than
// read back off disk, so putBytes and assetFor have to agree exactly.
//
// The pinned hash would also catch a disagreement, but only as "the hash moved",
// which sends the next person looking at the wrong thing. This names it.
func TestPrimedChartDigestMatchesAssetFor(t *testing.T) {
	dir := deterministicSong(t)
	fsys := os.DirFS(dir)

	raw, err := fs.ReadFile(fsys, "notes.chart")
	if err != nil {
		t.Fatal(err)
	}
	d := newAssetDigests(fsys)
	d.putBytes("notes.chart", raw)
	primed, err := d.get("notes.chart")
	if err != nil {
		t.Fatal(err)
	}

	read, err := assetFor(fsys, "notes.chart")
	if err != nil {
		t.Fatal(err)
	}

	if primed != *read {
		t.Fatalf("primed digest disagrees with assetFor\n  primed %+v\n  read   %+v", primed, *read)
	}
}

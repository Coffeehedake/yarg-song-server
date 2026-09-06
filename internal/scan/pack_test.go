package scan

import (
	"bytes"
	"encoding/hex"
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// These tests exist because a deployment found what no unit test did.
//
// On 2026-09-06 two machines synced the same 22 songs from the same server and
// 16 of the 22 archives had different SHA-256s. 16 was exactly the number the
// server had to re-pack rather than serve from cache. The cause was a fresh
// crypto/rand mask generated inside sng.Write on every call, which made PackDir
// non-deterministic and made several claims in our own docs false.
//
// The mask is not key material - it is stored in plaintext in the .sng header -
// so nothing is lost by deriving it. What is gained is that the same folder
// packs to the same bytes on every machine, every run, forever.

func songFolder(t *testing.T, extra map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"song.ini":  "[Song]\nname=Determinism\nartist=Measurement\n",
		"notes.mid": "MThd\x00\x00\x00\x06\x00\x01\x00\x01\x01\xe0MTrk\x00\x00\x00\x04\x00\xff\x2f\x00",
		"song.ogg":  "not really ogg, but the packer copies bytes",
	}
	for k, v := range extra {
		files[k] = v
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	return dir
}

func packTwice(t *testing.T, dir string) ([]byte, []byte) {
	t.Helper()
	var a, b bytes.Buffer
	if err := PackDir(dir, &a); err != nil {
		t.Fatalf("first pack: %v", err)
	}
	if err := PackDir(dir, &b); err != nil {
		t.Fatalf("second pack: %v", err)
	}
	return a.Bytes(), b.Bytes()
}

func TestPackingTheSameFolderTwiceGivesTheSameBytes(t *testing.T) {
	a, b := packTwice(t, songFolder(t, nil))
	if !bytes.Equal(a, b) {
		t.Fatalf("two packs of one folder differ: %d and %d bytes, first difference at %d\n"+
			"a mask begins %s\nb mask begins %s",
			len(a), len(b), firstDiff(a, b),
			hex.EncodeToString(maskOf(a)), hex.EncodeToString(maskOf(b)))
	}
}

// The bound-enforcement bug taught this lesson: a test that only exercises the
// happy path proves less than it looks like it proves. So assert the actual
// consequence the deployment saw - two independent packs of the SAME content
// produce the SAME archive, which is what makes an ETag and a Range resume
// honest across an eviction.
func TestAnEvictedArchiveRepacksToTheSameBytes(t *testing.T) {
	dir := songFolder(t, nil)

	first := new(bytes.Buffer)
	if err := PackDir(dir, first); err != nil {
		t.Fatalf("pack: %v", err)
	}
	// Simulate the eviction: throw the archive away and pack again from the
	// folder, exactly as packcache does on the next request.
	second := new(bytes.Buffer)
	if err := PackDir(dir, second); err != nil {
		t.Fatalf("repack: %v", err)
	}
	if !bytes.Equal(first.Bytes(), second.Bytes()) {
		t.Fatal("a re-pack after eviction does not reproduce the archive, so the ETag " +
			"promises bytes the server will not serve and a Range resume across an " +
			"eviction splices two different archives")
	}
}

func TestDifferentContentGetsADifferentMask(t *testing.T) {
	a := new(bytes.Buffer)
	b := new(bytes.Buffer)
	if err := PackDir(songFolder(t, nil), a); err != nil {
		t.Fatalf("pack a: %v", err)
	}
	if err := PackDir(songFolder(t, map[string]string{"song.ogg": "different audio entirely"}), b); err != nil {
		t.Fatalf("pack b: %v", err)
	}
	if bytes.Equal(maskOf(a.Bytes()), maskOf(b.Bytes())) {
		t.Fatal("two different packages share a mask; the mask is not derived from the content")
	}
}

// Determinism is worth nothing if the archive stopped being readable, and the
// cheapest way to be sure is to read it back with our own reader rather than to
// reason about the header. Real YARG is still the oracle; this is the guard that
// runs on every commit.
func TestADeterministicallyMaskedArchiveStillReadsBack(t *testing.T) {
	dir := songFolder(t, nil)
	var buf bytes.Buffer
	if err := PackDir(dir, &buf); err != nil {
		t.Fatalf("pack: %v", err)
	}

	raw := buf.Bytes()
	arc, err := sng.Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open packed archive: %v", err)
	}
	if got := arc.Metadata["name"]; got != "Determinism" {
		t.Errorf("metadata name = %q, want Determinism", got)
	}
	want, err := os.ReadFile(filepath.Join(dir, "notes.mid"))
	if err != nil {
		t.Fatal(err)
	}
	got, err := arc.ReadFile("notes.mid")
	if err != nil {
		t.Fatalf("read notes.mid out of the archive: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Error("the chart does not survive a round trip through the deterministic mask")
	}
}

func maskOf(b []byte) []byte {
	if len(b) < sng.HeaderSize {
		return nil
	}
	return b[sng.HeaderSize-sng.MaskSize : sng.HeaderSize]
}

func firstDiff(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

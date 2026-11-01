package yargsong

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"testing"

	"github.com/bodgit/sevenzip"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// TestDecodedPayloadIsAnSngBehindADecoyTag proves what the RB3CON tag actually is.
//
// YARG's SngFile.TryLoadFromFile seeks past the first six bytes of a decoded .yargsong without
// ever comparing them to SNGPKG, so those bytes are free for YARC to put anything in - and
// they put "RB3CON". Everything after them is an ordinary SNG: version 1, mask, metadata,
// listings.
//
// The test writes SNGPKG over the decoy and hands the result to this project's own SNG reader.
// If that parses and yields real metadata, the payload was an SNG all along, and nothing about
// Rock Band or CON packages is involved.
//
//	YARN_PROBE=1 go test ./internal/yargsong/ -run TestDecodedPayloadIsAnSng -v
func TestDecodedPayloadIsAnSngBehindADecoyTag(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run")
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "yarn-probe-*.7z"))
	if len(matches) == 0 {
		t.Skip("no cached YARN archives")
	}

	r, err := sevenzip.OpenReaderWithPassword(matches[0], archivePassword())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	dir := t.TempDir()
	enc := filepath.Join(dir, "entry.yargsong")
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(enc)
	_, cerr := io.Copy(f, rc)
	rc.Close()
	f.Close()
	if cerr != nil {
		t.Fatal(cerr)
	}

	y, err := Open(enc)
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := io.ReadAll(y)
	y.Close()
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("decoded %d bytes, tag %q, next 4 bytes %x (SNG version, little endian)",
		len(decoded), string(decoded[:6]), decoded[6:10])

	// Swap the decoy for the real magic and let the project's own reader judge it.
	patched := append([]byte("SNGPKG"), decoded[6:]...)

	a, err := sng.Open(bytes.NewReader(patched), int64(len(patched)))
	if err != nil {
		t.Fatalf("our SNG reader refused the patched payload: %v", err)
	}

	t.Logf("PARSED AS SNG. version=%d metadata keys=%d", a.Version, len(a.Metadata))
	for _, k := range a.MetaOrder {
		t.Logf("  metadata %-16s %s", k, a.Metadata[k])
	}

	ents, err := fs.ReadDir(a, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	for i, e := range ents {
		if i >= 10 {
			t.Logf("  ... and %d more", len(ents)-10)
			break
		}
		fi, _ := e.Info()
		t.Logf("  contains %-34s %d bytes", e.Name(), fi.Size())
	}
}

// TestOpenArchiveReadsARealYargsong is the shipped entry point doing the whole job: decode,
// skip the decoy, parse as SNG, and list what is inside. If this works the scanner needs
// nothing from this package but a call.
//
//	YARN_PROBE=1 go test ./internal/yargsong/ -run TestOpenArchive -v
func TestOpenArchiveReadsARealYargsong(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run")
	}
	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "yarn-probe-*.7z"))
	if len(matches) == 0 {
		t.Skip("no cached YARN archives")
	}

	r, err := sevenzip.OpenReaderWithPassword(matches[0], archivePassword())
	if err != nil {
		t.Fatal(err)
	}
	defer r.Close()

	dir := t.TempDir()
	enc := filepath.Join(dir, "entry.yargsong")
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	f, _ := os.Create(enc)
	_, cerr := io.Copy(f, rc)
	rc.Close()
	f.Close()
	if cerr != nil {
		t.Fatal(cerr)
	}

	a, reader, err := OpenArchive(enc)
	if err != nil {
		t.Fatalf("OpenArchive: %v", err)
	}
	defer reader.Close()

	name := a.Metadata["name"]
	artist := a.Metadata["artist"]
	t.Logf("OpenArchive gave: %q by %q (sng version %d)", name, artist, a.Version)
	if name == "" {
		t.Error("no name in metadata")
	}

	ents, err := fs.ReadDir(a, ".")
	if err != nil {
		t.Fatalf("ReadDir: %v", err)
	}
	var hasChart bool
	for _, e := range ents {
		if e.Name() == "notes.mid" || e.Name() == "notes.chart" {
			hasChart = true
		}
	}
	if !hasChart {
		t.Errorf("no chart file among %d entries - the server could not hash this song", len(ents))
	}
	t.Logf("entries: %d, chart present: %v", len(ents), hasChart)
}

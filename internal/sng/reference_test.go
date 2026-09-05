package sng

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"io"
	"os"
	"testing"
)

// The archive in testdata was produced by SngCli v0.3.0 - the reference encoder
// from mdsitton/SngFileFormat, the same tool that defines the format - from a
// song folder we wrote. It is the only test here that proves the reader agrees
// with something OTHER than our own understanding of the format.
//
// mask_test.go and read_test.go build their own .sng with an encoder written
// from the same reading of the spec as the reader. That catches bounds, offset
// and masking bugs, but two components written from one misreading agree with
// each other perfectly. This file is the check on that.
//
// Regenerating the fixture (a chart, a song.ini, a real PNG and a real WAV in a
// folder, then):
//
//	SngCli.exe encode -i <folder-of-song-folders> -o <out> --noStatusBar -t 1
const referenceArchive = "testdata/reference-sngcli-v0.3.0.sng"

func openReference(t *testing.T) (*Archive, func()) {
	t.Helper()
	f, err := os.Open(referenceArchive)
	if err != nil {
		t.Fatalf("open reference archive: %v", err)
	}
	st, err := f.Stat()
	if err != nil {
		f.Close()
		t.Fatal(err)
	}
	a, err := Open(f, st.Size())
	if err != nil {
		f.Close()
		t.Fatalf("Open: %v", err)
	}
	return a, func() { f.Close() }
}

func TestReadsReferenceEncoderOutput(t *testing.T) {
	a, done := openReference(t)
	defer done()

	if a.Version != Version1 {
		t.Fatalf("version = %d, want %d", a.Version, Version1)
	}

	for k, want := range map[string]string{
		"name":      "Reference Song",
		"artist":    "Reference Artist",
		"album":     "Reference Album",
		"genre":     "Rock",
		"charter":   "SngCli Fixture",
		"year":      ", 2003?",
		"preview":   "30000 45000",
		"pro_drums": "True",
	} {
		if got := a.Metadata[k]; got != want {
			t.Errorf("metadata[%q] = %q, want %q", k, got, want)
		}
	}

	// A key we do not model must still come through, or a repack loses it.
	if got := a.Metadata["some_future_key"]; got != "preserved" {
		t.Errorf("unmodelled key was lost: %q", got)
	}

	// song.ini is NOT a contained file: its keys live in the metadata section.
	for _, n := range a.Names() {
		if n == "song.ini" {
			t.Error("song.ini appeared as a contained file; it belongs in the metadata section")
		}
	}
}

// The chart bytes must survive the archive untouched, because the song's
// identity is a hash of exactly those bytes. If the reader mis-unmasks even one
// byte, this hash moves and the client would never match our catalog.
func TestReferenceChartRoundTripsByteExact(t *testing.T) {
	a, done := openReference(t)
	defer done()

	got, err := a.ReadFile("notes.chart")
	if err != nil {
		t.Fatalf("read chart from archive: %v", err)
	}
	want, err := os.ReadFile("testdata/reference-notes.chart")
	if err != nil {
		t.Fatalf("read chart from disk: %v", err)
	}
	if !bytes.Equal(got, want) {
		t.Fatalf("chart bytes differ: archive %d bytes, original %d bytes", len(got), len(want))
	}

	sum := sha1.Sum(got)
	const wantHash = "30b18fee1d336a6b83c2fd7e134487d013710e14"
	if h := hex.EncodeToString(sum[:]); h != wantHash {
		t.Fatalf("chart hash = %s, want %s", h, wantHash)
	}
}

// Streaming a contained file in awkward chunks must match reading it whole.
// This is the same masking-origin trap as read_test.go, but against bytes we
// did not encode ourselves.
func TestReferenceChunkedReadMatches(t *testing.T) {
	a, done := openReference(t)
	defer done()

	for _, name := range a.Names() {
		whole, err := a.ReadFile(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		f, err := a.Open(name)
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		var streamed bytes.Buffer
		buf := make([]byte, 13) // not a divisor of the 256-byte mask table
		for {
			n, err := f.Read(buf)
			streamed.Write(buf[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s: %v", name, err)
			}
		}
		_ = f.Close()
		if !bytes.Equal(streamed.Bytes(), whole) {
			t.Fatalf("%s: chunked read differs from whole read", name)
		}
	}
}

// SngCli emits audio under a .mp3 name regardless of the source container: the
// fixture's song.wav came back as song.mp3 with identical bytes and an intact
// RIFF/WAVE header. So a real .sng can carry a file whose EXTENSION LIES about
// its contents.
//
// Mirroring YARG, we classify stems by name, so this does not break the scan.
// It is recorded here because anything that later DECODES audio must sniff the
// container rather than trust the extension, and because our own writer must
// not reproduce this behaviour.
func TestExtensionCanLieAboutContainer(t *testing.T) {
	a, done := openReference(t)
	defer done()

	data, err := a.ReadFile("song.mp3")
	if err != nil {
		t.Skipf("fixture has no song.mp3 (names: %v)", a.Names())
	}
	if len(data) < 12 || string(data[0:4]) != "RIFF" || string(data[8:12]) != "WAVE" {
		t.Fatalf("fixture changed: song.mp3 no longer carries WAVE bytes (first 12: %q)", data[:min(12, len(data))])
	}
}

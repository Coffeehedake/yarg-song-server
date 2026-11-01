package yargsong

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bodgit/sevenzip"
)

// archivePassword is duplicated here rather than imported, because internal/yarn importing
// this package (so the installer can index what it installs) would make the reverse import a
// cycle. Eight lines of arithmetic in a test file is the cheaper of the two problems.
func archivePassword() string {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	out := make([]byte, 0, 64)
	for i := int32(0); i < 64; i++ {
		a := byte(5 + i*104729)
		b := byte(9 + i*224737)
		out = append(out, letters[(a%b)%52])
	}
	return string(out)
}

// TestDecodesToAKnownContainer is the whole question: does the port produce the file that is
// inside? If the transliteration of the Crawford arithmetic were wrong anywhere this would
// produce plausible garbage, which is why the check is a known magic rather than "it did not
// crash".
//
// It expects RB3CON, and that is a MEASURED result rather than the obvious guess. This test
// first asserted SNGPKG, on the assumption that a .yargsong wraps a .sng; it does not. Every
// entry surveyed across the cached packs decodes to a Rock Band 3 console package - see
// TestSurveyPayloadMagics. Producing a valid six-character magic by accident is on the order
// of one in 2^48, so RB3CON is the payload, not a symptom of a broken port.
//
//	YARN_PROBE=1 go test ./internal/yargsong/ -v
func TestDecodesToAKnownContainer(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run (needs a cached YARN archive)")
	}

	archive := filepath.Join(os.TempDir(), "yarn-probe-childrens-keys-3.7z")
	if _, err := os.Stat(archive); err != nil {
		t.Skipf("no cached archive at %s", archive)
	}

	r, err := sevenzip.OpenReaderWithPassword(archive, archivePassword())
	if err != nil {
		t.Fatalf("open archive: %v", err)
	}
	defer r.Close()
	if len(r.File) == 0 {
		t.Fatal("archive is empty")
	}

	dir := t.TempDir()
	extracted := filepath.Join(dir, "song.yargsong")
	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(extracted)
	if err != nil {
		rc.Close()
		t.Fatal(err)
	}
	if _, err := io.Copy(f, rc); err != nil {
		t.Fatal(err)
	}
	rc.Close()
	f.Close()
	t.Logf("extracted %s", r.File[0].Name)

	y, err := Open(extracted)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer y.Close()

	head := make([]byte, 16)
	if _, err := io.ReadFull(y, head); err != nil {
		t.Fatalf("read decoded: %v", err)
	}
	t.Logf("decoded first 16 bytes: %x", head)
	t.Logf("as text: %q", string(head[:6]))

	if string(head[:6]) != "RB3CON" {
		t.Fatalf("decoded payload starts %q, want RB3CON - the port is wrong, or YARN changed what it ships", string(head[:6]))
	}
	t.Logf("payload size: %d bytes", y.Size())

	// ReadAt must agree with Read, or the scanner reading at offsets gets different bytes
	// from the scanner reading sequentially - a defect that would surface as random
	// corruption much later.
	at := make([]byte, 16)
	if _, err := y.ReadAt(at, 0); err != nil && err != io.EOF {
		t.Fatalf("ReadAt: %v", err)
	}
	if string(at) != string(head) {
		t.Errorf("ReadAt(0) = %x, Read = %x - they disagree", at, head)
	}
}

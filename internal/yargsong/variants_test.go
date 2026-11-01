package yargsong

import (
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/bodgit/sevenzip"
)

// TestProbeDecodeVariants exists because "RB3CON" is suspicious: YARG.Core looks for "CON "
// and has never heard of RB3CON, so either the payload is something I have not identified or
// the decode has a consistent offset error that happens to spell something.
//
// It prints the raw header, the decode as written, and the decode with the position term
// shifted, so the right answer can be recognised rather than argued about.
//
//	YARN_PROBE=1 go test ./internal/yargsong/ -run TestProbeDecodeVariants -v
func TestProbeDecodeVariants(t *testing.T) {
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

	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatal(err)
	}
	raw, err := io.ReadAll(io.LimitReader(rc, 128))
	rc.Close()
	if err != nil {
		t.Fatal(err)
	}

	t.Logf("file: %s", r.File[0].Name)
	t.Logf("raw first 32 bytes:  %x", raw[:32])
	t.Logf("signature: %q  seed byte: 0x%02x", string(raw[:8]), raw[8])
	t.Logf("set (15 bytes): %x", raw[9:24])

	k, err := deriveKeys(raw[8], raw[9:24])
	if err != nil {
		t.Fatalf("deriveKeys: %v", err)
	}
	t.Logf("keys a=%d b=%d c=%d", k.a, k.b, k.c)

	payload := raw[HeaderSize:]

	show := func(label string, off int64) {
		buf := append([]byte(nil), payload...)
		decode(buf, off, k)
		n := 24
		if len(buf) < n {
			n = len(buf)
		}
		t.Logf("  %-26s %q   %x", label, printable(buf[:8]), buf[:n])
	}

	show("as written (off=0)", 0)
	show("off=HeaderSize(24)", HeaderSize)
	show("off=-24", -24)
	show("off=8", 8)
	show("off=9", 9)
}

func printable(b []byte) string {
	out := make([]byte, len(b))
	for i, c := range b {
		if c >= 32 && c < 127 {
			out[i] = c
		} else {
			out[i] = '.'
		}
	}
	return string(out)
}

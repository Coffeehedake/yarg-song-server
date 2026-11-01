package scan

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// A real 7z starts with these six bytes. The fixtures below are built by hand rather than
// committed, because the archive that prompted this test is YARN's content and does not belong
// in this repository - and a signature plus garbage reproduces the behaviour exactly.
var sevenZMagic = []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}

func writeFile(t *testing.T, dir, name string, b []byte) string {
	t.Helper()
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, b, 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestA7zWeCannotOpenIsNamedRatherThanLeakingLZMAInternals(t *testing.T) {
	dir := t.TempDir()

	// Signature present, contents meaningless: indistinguishable to us from a password
	// protected archive, which is the case that actually happens now that YARN publishes
	// every setlist as an encrypted .7z.
	body := append(append([]byte{}, sevenZMagic...), []byte("not really a 7z body")...)
	p := writeFile(t, dir, "encrypted-looking.7z", body)

	_, _, err := OpenContainer(p)
	if err == nil {
		t.Fatal("OpenContainer accepted a 7z it cannot read")
	}
	if !errors.Is(err, ErrUnopenable7z) {
		t.Fatalf("error was %v, want it to wrap ErrUnopenable7z", err)
	}

	// The whole point is what the operator reads. "newRangeDecoder: first byte not zero" is
	// what they used to get, and it tells them nothing about what to do.
	msg := err.Error()
	for _, want := range []string{"password-protected", "yarn-install"} {
		if !strings.Contains(msg, want) {
			t.Errorf("error message does not mention %q: %s", want, msg)
		}
	}
}

func TestAFileThatMerelyEndsIn7zIsNotDignifiedWithThatError(t *testing.T) {
	dir := t.TempDir()

	// No signature. This is junk somebody renamed, not an archive we failed to open, and
	// telling them it is probably password-protected would be a confident wrong answer.
	p := writeFile(t, dir, "junk.7z", []byte("this is a text file wearing a hat"))

	_, _, err := OpenContainer(p)
	if err == nil {
		t.Fatal("OpenContainer accepted a non-archive")
	}
	if errors.Is(err, ErrUnopenable7z) {
		t.Errorf("a file without the 7z signature was reported as an unopenable archive: %v", err)
	}
}

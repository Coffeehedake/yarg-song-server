package config

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func tempConf(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "yarg-song-server.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

func read(t *testing.T, p string) string {
	t.Helper()
	b, err := os.ReadFile(p)
	if err != nil {
		t.Fatal(err)
	}
	return string(b)
}

// The file belongs to the operator. Changing one setting must not touch a
// single other byte — not their comments, not their other settings, not their
// blank lines.
func TestEveryOtherLineSurvives(t *testing.T) {
	body := "# my notes\n" +
		"listen = :9999\n" +
		"\n" +
		"# a comment I wrote about the cache\n" +
		"pack_cache_max = 4G\n" +
		"check_uploads = no\n"
	p := tempConf(t, body)

	if err := SetKey(p, "check_uploads", "yes"); err != nil {
		t.Fatal(err)
	}

	got := read(t, p)
	want := strings.Replace(body, "check_uploads = no", "check_uploads = yes", 1)
	if got != want {
		t.Errorf("file changed beyond the one setting:\n--- got ---\n%s\n--- want ---\n%s", got, want)
	}
}

// The shipped template has every setting commented out. A first write must not
// leave the file saying two different things, and it must not destroy the
// documentation either.
func TestWritingIntoTheShippedTemplateLeavesItReadable(t *testing.T) {
	p := tempConf(t, Example)

	if err := SetKey(p, "check_uploads", "yes"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)

	// The explanatory comment is still there.
	if !strings.Contains(got, "# check_uploads = no") {
		t.Error("the commented template line was destroyed")
	}
	// And the server reads back the new value, which is the only thing that
	// actually matters about where the line went.
	c := Defaults()
	if err := LoadFile(&c, p); err != nil {
		t.Fatal(err)
	}
	if !c.CheckUploads {
		t.Error("the server does not read back the value that was written")
	}
}

// Toggling twice must not leave two lines. The second write has to find the
// line the first one added.
func TestASecondChangeRewritesRatherThanAccumulates(t *testing.T) {
	p := tempConf(t, Example)

	for _, v := range []string{"yes", "no", "yes"} {
		if err := SetKey(p, "check_uploads", v); err != nil {
			t.Fatal(err)
		}
	}

	got := read(t, p)
	live := 0
	for _, l := range strings.Split(got, "\n") {
		if m := setLine.FindStringSubmatch(l); m != nil && m[3] == "check_uploads" && m[2] == "" {
			live++
		}
	}
	if live != 1 {
		t.Errorf("%d uncommented check_uploads lines after three writes, want 1\n%s", live, got)
	}

	c := Defaults()
	if err := LoadFile(&c, p); err != nil {
		t.Fatal(err)
	}
	if !c.CheckUploads {
		t.Error("the final value is not what was written last")
	}
}

// LAST ONE WINS in this format, so rewriting the first of several would leave a
// later line silently overriding it: the file would say one thing and the
// server would do another. This is the case a naive "find and replace the first
// match" gets wrong, and it looks correct in every other test.
func TestTheLINEThatWinsIsTheOneRewritten(t *testing.T) {
	p := tempConf(t, "check_uploads = no\nlisten = :9999\ncheck_uploads = no\n")

	if err := SetKey(p, "check_uploads", "yes"); err != nil {
		t.Fatal(err)
	}

	c := Defaults()
	if err := LoadFile(&c, p); err != nil {
		t.Fatal(err)
	}
	if !c.CheckUploads {
		t.Errorf("the server still reads the old value; the wrong line was rewritten:\n%s", read(t, p))
	}
}

// A config edited on Windows should not come back with mixed line endings.
func TestCRLFFilesStayCRLF(t *testing.T) {
	p := tempConf(t, "# notes\r\ncheck_uploads = no\r\n")

	if err := SetKey(p, "check_uploads", "yes"); err != nil {
		t.Fatal(err)
	}
	got := read(t, p)
	if strings.Contains(strings.ReplaceAll(got, "\r\n", ""), "\n") {
		t.Errorf("a bare LF was introduced into a CRLF file: %q", got)
	}
	if !strings.Contains(got, "check_uploads = yes\r\n") {
		t.Errorf("value not written with the file's own line ending: %q", got)
	}
}

// Persisting something the server cannot read back next time would turn a
// convenience into a server that will not start.
func TestAValueTheParserWouldRejectIsRefused(t *testing.T) {
	p := tempConf(t, "check_uploads = no\n")
	before := read(t, p)

	if err := SetKey(p, "check_uploads", "maybe"); err == nil {
		t.Error("wrote a value the parser rejects")
	}
	if read(t, p) != before {
		t.Error("the file was modified by a write that should have been refused")
	}
}

// An unwritable file must fail cleanly and leave the original intact. The
// runtime change still stands; the caller reports that it was not saved.
func TestAnUnwritableFileFailsWithoutDamage(t *testing.T) {
	if runtime.GOOS == "windows" {
		// A read-only FILE is still replaceable by a rename on Windows; the
		// directory permission is what stops it, and emulating that here says
		// more about the test than the code.
		t.Skip("directory permissions do not model this the same way on Windows")
	}
	dir := t.TempDir()
	p := filepath.Join(dir, "yarg-song-server.conf")
	if err := os.WriteFile(p, []byte("check_uploads = no\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(dir, 0o500); err != nil { // no write on the directory
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Chmod(dir, 0o700) })

	if err := SetKey(p, "check_uploads", "yes"); err == nil {
		t.Error("reported success against an unwritable directory")
	}
	if read(t, p) != "check_uploads = no\n" {
		t.Error("the original file was damaged by a failed write")
	}
}

// No temp files left lying beside the operator's config, on either path.
func TestNoTempFilesAreLeftBehind(t *testing.T) {
	p := tempConf(t, Example)
	if err := SetKey(p, "browse_ui", "no"); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "check_uploads", "maybe"); err == nil {
		t.Fatal("expected a refusal")
	}

	entries, err := os.ReadDir(filepath.Dir(p))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".yss-conf-") {
			t.Errorf("left a temp file behind: %s", e.Name())
		}
	}
}

// The file keeps the permissions it had. Pressing a switch should not quietly
// change who can read the config.
func TestPermissionsAreCarriedOver(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("unix permission bits are not modelled on Windows")
	}
	p := tempConf(t, "check_uploads = no\n")
	if err := os.Chmod(p, 0o640); err != nil {
		t.Fatal(err)
	}
	if err := SetKey(p, "check_uploads", "yes"); err != nil {
		t.Fatal(err)
	}
	st, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	if st.Mode().Perm() != 0o640 {
		t.Errorf("mode became %o, want 640", st.Mode().Perm())
	}
}

func TestYesNoMatchesTheTemplateSpelling(t *testing.T) {
	if YesNo(true) != "yes" || YesNo(false) != "no" {
		t.Error("booleans should be written the way the template writes them")
	}
}

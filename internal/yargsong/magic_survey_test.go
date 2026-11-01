package yargsong

import (
	"io"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/bodgit/sevenzip"
)

// TestSurveyPayloadMagics decodes every entry in whatever cached YARN archives are lying
// around and reports what each one actually is.
//
// Written after a single sample decoded to RB3CON rather than the expected SNGPKG. One sample
// cannot tell "the port is wrong" from "the payload is not what I assumed", and the difference
// decides whether this server can index these songs at all.
//
//	YARN_PROBE=1 go test ./internal/yargsong/ -run TestSurveyPayloadMagics -v
func TestSurveyPayloadMagics(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run")
	}

	matches, _ := filepath.Glob(filepath.Join(os.TempDir(), "yarn-probe-*.7z"))
	if len(matches) == 0 {
		t.Skip("no cached YARN archives")
	}

	counts := map[string]int{}
	dir := t.TempDir()

	for _, archive := range matches {
		r, err := sevenzip.OpenReaderWithPassword(archive, archivePassword())
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(archive), err)
			continue
		}

		for _, e := range r.File {
			rc, err := e.Open()
			if err != nil {
				t.Errorf("%s: %v", e.Name, err)
				continue
			}
			p := filepath.Join(dir, "entry.yargsong")
			f, err := os.Create(p)
			if err != nil {
				rc.Close()
				t.Fatal(err)
			}
			_, cerr := io.Copy(f, rc)
			rc.Close()
			f.Close()
			if cerr != nil {
				t.Errorf("%s: %v", e.Name, cerr)
				continue
			}

			y, err := Open(p)
			if err != nil {
				counts["<not a yargsong: "+err.Error()+">"]++
				continue
			}
			head := make([]byte, 8)
			n, _ := io.ReadFull(y, head)
			y.Close()

			magic := string(head[:min(6, n)])
			counts[magic]++
			t.Logf("  %-66s -> %q", truncate(e.Name, 66), magic)
		}
		r.Close()
	}

	t.Log("--- payload magic counts ---")
	keys := make([]string, 0, len(counts))
	for k := range counts {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		t.Logf("  %-40q %d", k, counts[k])
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

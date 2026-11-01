package scan

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

// TestProbeRealBigArchives times songRoots against the archives that took vault2 down.
//
// The first multi-song implementation walked the archive as a filesystem and never finished on
// a 2 GB solid .7z. This reads only the stored names, so the claim to check is that opening
// and listing a multi-gigabyte archive is seconds rather than forever - measured against the
// real files over the share, not a synthetic fixture that cannot reproduce the problem.
//
//	YARG_BIG_ARCHIVES='\\192.168.2.7\YARG\songs' go test ./internal/scan/ -run TestProbeRealBigArchives -v
func TestProbeRealBigArchives(t *testing.T) {
	dir := os.Getenv("YARG_BIG_ARCHIVES")
	if dir == "" {
		t.Skip("set YARG_BIG_ARCHIVES to a folder of archives to run")
	}

	var paths []string
	for _, pat := range []string{"*.zip", "*.7z"} {
		m, _ := filepath.Glob(filepath.Join(dir, pat))
		paths = append(paths, m...)
	}
	if len(paths) == 0 {
		t.Skipf("no archives in %s", dir)
	}

	for _, p := range paths {
		fi, err := os.Stat(p)
		if err != nil {
			t.Errorf("%s: %v", filepath.Base(p), err)
			continue
		}

		start := time.Now()
		_, raw, closer, err := openArchive(p)
		openTook := time.Since(start)
		if err != nil {
			t.Logf("  %-28s %5d MB  open FAILED after %v: %v",
				filepath.Base(p), fi.Size()/(1<<20), openTook, err)
			continue
		}

		start = time.Now()
		roots, rerr := songRoots(raw)
		rootsTook := time.Since(start)
		closer.Close()

		t.Logf("  %-28s %5d MB  entries=%-5d open=%-8v roots=%-8v songs=%d err=%v",
			filepath.Base(p), fi.Size()/(1<<20), len(raw),
			openTook.Round(time.Millisecond), rootsTook.Round(time.Millisecond),
			len(roots), rerr)
	}
}

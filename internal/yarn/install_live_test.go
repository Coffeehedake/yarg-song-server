package yarn

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestInstallRealPack installs the smallest real pack into a scratch library.
//
// Unit tests prove the guards; this proves the thing. It downloads 12 MB from YARN, extracts
// an encrypted archive with the derived password, lays the songs out the way a library expects
// and writes a manifest - then installs again to prove the second run is a no-op rather than a
// re-download.
//
//	YARN_PROBE=1 go test ./internal/yarn/ -run TestInstallRealPack -v
func TestInstallRealPack(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run (downloads a real pack)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()

	lib := t.TempDir()
	in := &Installer{
		Client:     NewClient(),
		LibraryDir: lib,
		// Staging on the same disk as the library, which is what the installer's own error
		// message tells an operator to do.
		WorkDir: filepath.Join(lib, ".staging"),
	}

	entries, err := in.Client.Index(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	var target *Entry
	for i := range entries {
		if entries[i].Name == "Children's Keys Pack 3" {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		t.Fatal("probe pack missing from the index")
	}

	res, err := in.Install(ctx, *target)
	if err != nil {
		t.Fatalf("install: %v", err)
	}
	t.Logf("installed %q tag=%s songs=%d bytes=%d dir=%s",
		res.Pack, res.Tag, res.Songs, res.Bytes, res.Dir)

	if res.Skipped {
		t.Error("first install reported Skipped")
	}
	if res.Songs != 6 {
		t.Errorf("installed %d songs, want 6", res.Songs)
	}

	// The files must be where a library expects them, with the extension YARG.Core scans for.
	var found int
	err = filepath.Walk(res.Dir, func(p string, fi os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		if !fi.IsDir() && strings.EqualFold(filepath.Ext(p), ".yargsong") {
			found++
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if found != 6 {
		t.Errorf("found %d .yargsong files on disk, want 6", found)
	}

	m, err := ReadManifest(res.Dir)
	if err != nil {
		t.Fatalf("manifest: %v", err)
	}
	if m == nil {
		t.Fatal("no manifest written")
	}
	if m.Tag != res.Tag {
		t.Errorf("manifest tag %q, want %q", m.Tag, res.Tag)
	}
	if len(m.ArchiveSHA256) != 1 || len(m.ArchiveSHA256[0]) != 64 {
		t.Errorf("manifest archive hashes = %v, want one 64-char hash", m.ArchiveSHA256)
	}

	// No archives left behind: staging is removed and the pack directory holds songs, not 7z.
	if entriesLeft, err := os.ReadDir(res.Dir); err == nil {
		for _, e := range entriesLeft {
			if strings.HasSuffix(e.Name(), ".7z") {
				t.Errorf("archive left in the library: %s", e.Name())
			}
		}
	}

	// Second install: same tag, so nothing should be fetched.
	again, err := in.Install(ctx, *target)
	if err != nil {
		t.Fatalf("second install: %v", err)
	}
	if !again.Skipped {
		t.Error("second install re-downloaded instead of reporting Skipped")
	}
	t.Logf("second install skipped=%v songs=%d", again.Skipped, again.Songs)
}

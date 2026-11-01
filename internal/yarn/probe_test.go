package yarn

import (
	"context"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/bodgit/sevenzip"
)

// TestProbeRealPack is a PROBE, not an assertion: it downloads the smallest real pack and
// prints what is actually inside it, so the tests that follow are written against the format
// as it is rather than as it was imagined. Skipped unless YARN_PROBE is set, because it hits
// the network and writes 12 MB.
//
//	YARN_PROBE=1 go test ./internal/yarn/ -run TestProbeRealPack -v
func TestProbeRealPack(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run (downloads a real pack)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := NewClient()
	entries, err := c.Index(ctx)
	if err != nil {
		t.Fatalf("index: %v", err)
	}
	t.Logf("index entries: %d", len(entries))

	var target *Entry
	for i := range entries {
		if entries[i].Name == "Children's Keys Pack 3" {
			target = &entries[i]
			break
		}
	}
	if target == nil {
		t.Fatal("could not find the probe pack in the index")
	}

	p, err := c.Profile(ctx, target.URL)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}
	t.Logf("pack %q tag=%q songs=%d files=%d", p.Name(), p.Tag(), len(p.Songs()), len(p.Files()))
	for _, s := range p.Songs() {
		t.Logf("  metadata song: %s - %s", s.Artist, s.Title)
	}

	files := p.Files()
	if len(files) == 0 {
		t.Fatal("no files")
	}
	t.Logf("file[0] type=%q url=%s", files[0].FileType, files[0].URL)

	dir := t.TempDir()
	archive := filepath.Join(dir, "pack.7z")

	req, _ := http.NewRequestWithContext(ctx, http.MethodGet, files[0].URL, nil)
	req.Header.Set("User-Agent", userAgent)
	resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
	if err != nil {
		t.Fatalf("download: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("download: HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(archive)
	if err != nil {
		t.Fatal(err)
	}
	n, err := io.Copy(f, resp.Body)
	f.Close()
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	t.Logf("downloaded %d bytes", n)

	// The question the probe exists to answer: does the published derivation open it, and
	// what is the shape of what comes out?
	r, err := sevenzip.OpenReaderWithPassword(archive, archivePassword())
	if err != nil {
		t.Fatalf("OPEN FAILED with the derived password: %v", err)
	}
	defer r.Close()

	t.Logf("OPENED. entries: %d", len(r.File))
	for i, e := range r.File {
		if i >= 25 {
			t.Logf("  ... and %d more", len(r.File)-25)
			break
		}
		t.Logf("  %-70s %d bytes", e.Name, e.UncompressedSize)
	}
}

// TestProbeYargsongFormat answers one question: is a .yargsong an .sng under another name?
// Our reader checks a 6-byte magic, so the file either starts with SNGPKG or it does not.
//
//	YARN_PROBE=1 go test ./internal/yarn/ -run TestProbeYargsongFormat -v
func TestProbeYargsongFormat(t *testing.T) {
	if os.Getenv("YARN_PROBE") == "" {
		t.Skip("set YARN_PROBE=1 to run (downloads a real pack)")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	c := NewClient()
	entries, err := c.Index(ctx)
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
	p, err := c.Profile(ctx, target.URL)
	if err != nil {
		t.Fatalf("profile: %v", err)
	}

	// Cached between runs on purpose: the question gets asked more than once.
	archive := filepath.Join(os.TempDir(), "yarn-probe-childrens-keys-3.7z")
	if _, err := os.Stat(archive); err != nil {
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, p.Files()[0].URL, nil)
		req.Header.Set("User-Agent", userAgent)
		resp, err := (&http.Client{Timeout: 5 * time.Minute}).Do(req)
		if err != nil {
			t.Fatalf("download: %v", err)
		}
		defer resp.Body.Close()
		f, err := os.Create(archive)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.Copy(f, resp.Body); err != nil {
			f.Close()
			t.Fatalf("write: %v", err)
		}
		f.Close()
	}

	r, err := sevenzip.OpenReaderWithPassword(archive, archivePassword())
	if err != nil {
		t.Fatalf("open: %v", err)
	}
	defer r.Close()

	rc, err := r.File[0].Open()
	if err != nil {
		t.Fatalf("open entry: %v", err)
	}
	defer rc.Close()

	head := make([]byte, 32)
	n, err := io.ReadFull(rc, head)
	if err != nil && err != io.ErrUnexpectedEOF {
		t.Fatalf("read entry: %v", err)
	}
	head = head[:n]

	t.Logf("entry: %s", r.File[0].Name)
	t.Logf("first %d bytes hex: %x", n, head)
	t.Logf("first 6 bytes as text: %q", string(head[:min(6, n)]))
	t.Logf("IS IT SNGPKG? %v", n >= 6 && string(head[:6]) == "SNGPKG")
}

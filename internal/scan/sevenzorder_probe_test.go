package scan

import (
	"io"
	"io/fs"
	"os"
	"testing"
	"time"

	"github.com/bodgit/sevenzip"
)

// TestProbeSevenZAccessOrder measures whether a solid .7z is slow because of ITS SIZE or
// because of OUR ACCESS ORDER.
//
// sevenzip pools one decoder per folder, keyed by the exact offset a read left it at, so
// reading entries in stored order should reuse a single stream while any out-of-order read
// restarts decompression from the block start. That is a claim about somebody else's code,
// so it gets measured before anything is designed on top of it.
//
//	YARG_7Z_PROBE=/mnt/user/YARG/songs/Rock\ Band\ 4.7z go test ./internal/scan/ -run TestProbeSevenZAccessOrder -v
func TestProbeSevenZAccessOrder(t *testing.T) {
	p := os.Getenv("YARG_7Z_PROBE")
	if p == "" {
		t.Skip("set YARG_7Z_PROBE to a .7z to run")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("%s  %d MB", p, fi.Size()/(1<<20))

	// 1. Every entry, in STORED order, each read to completion and closed.
	r, err := sevenzip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	start := time.Now()
	var bytesRead int64
	var files int
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		n, err := io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		bytesRead += n
		files++
	}
	ordered := time.Since(start)
	r.Close()
	t.Logf("STORED ORDER: %d entries, %d MB uncompressed, %v total",
		files, bytesRead/(1<<20), ordered.Round(time.Millisecond))

	// 2. The same entries, deliberately out of order: last, first, last-1, second...
	//    Only a handful, because if the theory holds each one costs a full block.
	r2, err := sevenzip.OpenReader(p)
	if err != nil {
		t.Fatal(err)
	}
	order := []int{}
	for i := 0; i < 4 && i < len(r2.File)/2; i++ {
		order = append(order, len(r2.File)-1-i, i)
	}
	start = time.Now()
	for _, idx := range order {
		f := r2.File[idx]
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		n, err := io.Copy(io.Discard, rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		t.Logf("  out-of-order entry %4d (%d KB) at %v", idx, n/1024,
			time.Since(start).Round(time.Millisecond))
	}
	t.Logf("OUT OF ORDER: %d entries in %v", len(order),
		time.Since(start).Round(time.Millisecond))
	r2.Close()

	// 3. What the scanner costs TODAY, for the first three songs only.
	fsys, roots, closer, err := OpenContainerAll(p)
	if err != nil {
		t.Fatal(err)
	}
	defer closer.Close()
	for i, inner := range roots {
		if i >= 3 {
			break
		}
		sub := fsys
		if inner != "" {
			sub, err = fs.Sub(fsys, inner)
			if err != nil {
				t.Logf("  sub %s: %v", inner, err)
				continue
			}
		}
		start = time.Now()
		_, serr := ScanDir(sub)
		t.Logf("  CURRENT ScanDir %-50s %v err=%v", inner,
			time.Since(start).Round(time.Millisecond), serr)
	}
}

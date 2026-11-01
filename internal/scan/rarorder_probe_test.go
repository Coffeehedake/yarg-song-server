package scan

import (
	"io"
	"os"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwaples/rardecode/v2"
)

// TestProbeRarAccess asks what a Go RAR reader will actually let us do, before anything is
// designed on top of it.
//
// archiveItem carries open func() (io.ReadCloser, error) - a LAZY, REPEATABLE, RANDOM-ACCESS
// opener - and newArchiveFS maps every entry up front, so a sequential-only reader cannot
// satisfy the contract the zip and 7z paths already meet. rardecode/v2 advertises List() plus
// File.Open(), which looks like random access; whether it is AFFORDABLE is a separate question,
// and for solid .7z the answer was 3m07s per out-of-order entry. That is a claim about somebody
// else's code, so it gets measured rather than assumed.
//
//	YARG_RAR_PROBE="$env:USERPROFILE\Downloads\Guitar Hero On Tour - Modern Hits.rar" go test ./internal/scan/ -run TestProbeRarAccess -v
func TestProbeRarAccess(t *testing.T) {
	p := os.Getenv("YARG_RAR_PROBE")
	if p == "" {
		t.Skip("set YARG_RAR_PROBE to a .rar to run")
	}
	fi, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	t.Logf("archive: %s", p)
	t.Logf("size   : %d MB", fi.Size()/(1<<20))

	// 1. Can we enumerate at all, and what does it cost? Headers are interleaved with data in
	// RAR, so this has to seek past packed bodies rather than read them.
	start := time.Now()
	files, err := rardecode.List(p)
	if err != nil {
		t.Fatalf("List failed after %s: %v", time.Since(start).Round(time.Millisecond), err)
	}
	listCost := time.Since(start)
	t.Logf("List   : %d entries in %s", len(files), listCost.Round(time.Millisecond))

	// 2. What SHAPE are the names and flags? The CP437 decode exists because zip mandates it;
	// RAR v5 stores UTF-8, so this says whether decodeEntryName has anything to do here.
	var dirs, solid, encrypted, nonUTF8, unknownSize int
	for _, f := range files {
		if f.IsDir {
			dirs++
		}
		if f.Solid {
			solid++
		}
		if f.Encrypted || f.HeaderEncrypted {
			encrypted++
		}
		if !utf8.ValidString(f.Name) {
			nonUTF8++
		}
		if f.UnKnownSize {
			unknownSize++
		}
	}
	t.Logf("dirs=%d solid=%d encrypted=%d non-utf8-names=%d unknown-size=%d",
		dirs, solid, encrypted, nonUTF8, unknownSize)
	for i, f := range files {
		if i >= 8 {
			t.Logf("  ... and %d more", len(files)-8)
			break
		}
		t.Logf("  %10d  solid=%-5v  %q", f.UnPackedSize, f.Solid, f.Name)
	}

	// Reading entries out of a SOLID multi-gigabyte archive is the operation that cost three
	// minutes apiece on .7z, so the flags can be inspected without paying for it.
	if os.Getenv("YARG_RAR_PROBE_LIST_ONLY") != "" {
		t.Log("list-only: skipping the read measurements")
		return
	}

	// 3. Does File.Open() work, and does it cost the same wherever we reach?
	//
	// THIS is the question. If a late entry costs what the first one costs, RAR can be a
	// servable container like .zip and .7z. If it costs a full decompression pass each time,
	// then it can only be UNPACKED in stored order, and claiming otherwise would reproduce
	// the outage that a 2 GB solid .7z in the library already caused once.
	readOne := func(f *rardecode.File) (int64, time.Duration, error) {
		t0 := time.Now()
		rc, oerr := f.Open()
		if oerr != nil {
			return 0, time.Since(t0), oerr
		}
		defer rc.Close()
		n, cerr := io.Copy(io.Discard, rc)
		return n, time.Since(t0), cerr
	}

	firstFile := -1
	for i, f := range files {
		if !f.IsDir && f.UnPackedSize > 0 {
			firstFile = i
			break
		}
	}
	if firstFile < 0 {
		t.Skip("no readable file entries in this archive")
	}

	n, d, err := readOne(files[firstFile])
	t.Logf("open FIRST file entry (#%d): %d bytes in %s err=%v",
		firstFile, n, d.Round(time.Millisecond), err)

	// A late entry, reached WITHOUT reading the ones before it.
	lastFile := -1
	for i := len(files) - 1; i >= 0; i-- {
		if !files[i].IsDir && files[i].UnPackedSize > 0 {
			lastFile = i
			break
		}
	}
	if lastFile > firstFile {
		n, d, err = readOne(files[lastFile])
		t.Logf("open LAST file entry (#%d) OUT OF ORDER: %d bytes in %s err=%v",
			lastFile, n, d.Round(time.Millisecond), err)
	}

	// 4. A bounded stored-order pass, which is what extract() does.
	const cap = 40
	limit := len(files)
	if limit > cap {
		limit = cap
	}
	start = time.Now()
	var got, failed int
	var bytesRead int64
	for _, f := range files[:limit] {
		if f.IsDir || f.UnPackedSize == 0 {
			continue
		}
		bn, _, rerr := readOne(f)
		if rerr != nil {
			failed++
			if failed <= 3 {
				t.Logf("  stored-order read failed: %q: %v", f.Name, rerr)
			}
			continue
		}
		bytesRead += bn
		got++
	}
	t.Logf("stored order: %d entries read (%d failed), %d MB, in %s",
		got, failed, bytesRead/(1<<20), time.Since(start).Round(time.Millisecond))

	// 5. And the sequential streaming API, for comparison - the one shape rardecode is
	// unambiguously built for.
	rc, err := rardecode.OpenReader(p)
	if err != nil {
		t.Fatalf("OpenReader: %v", err)
	}
	defer rc.Close()
	start = time.Now()
	var seqFiles int
	var seqBytes int64
	for seqFiles < limit {
		h, nerr := rc.Next()
		if nerr == io.EOF {
			break
		}
		if nerr != nil {
			t.Logf("  Next failed after %d: %v", seqFiles, nerr)
			break
		}
		if h.IsDir {
			continue
		}
		bn, cerr := io.Copy(io.Discard, rc)
		if cerr != nil {
			t.Logf("  sequential read of %q failed: %v", h.Name, cerr)
			break
		}
		seqBytes += bn
		seqFiles++
	}
	t.Logf("sequential  : %d entries, %d MB, in %s  (volumes: %v)",
		seqFiles, seqBytes/(1<<20), time.Since(start).Round(time.Millisecond), rc.Volumes())
}

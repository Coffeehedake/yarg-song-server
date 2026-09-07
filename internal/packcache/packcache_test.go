package packcache

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

// song writes a real loose song folder and returns its path. pad controls the
// archive's size, which is what the eviction tests need to steer.
func song(t *testing.T, root, name string, pad int) string {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	write := func(f, body string) {
		if err := os.WriteFile(filepath.Join(dir, f), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("song.ini", "[Song]\nname = "+name+"\nartist = Test\n")
	write("notes.chart", fmt.Sprintf("[Song]\n{\n  Resolution = 192\n  Name = \"%s\"\n}\n[ExpertSingle]\n{\n  768 = N 0 0\n}\n", name))
	write("song.ogg", strings.Repeat("a", pad))
	return dir
}

// sizeOf totals the .sng archives in the cache directory.
func sizeOf(t *testing.T, dir string) int64 {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var n int64
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sng") {
			info, err := e.Info()
			if err != nil {
				t.Fatal(err)
			}
			n += info.Size()
		}
	}
	return n
}

func countSNG(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sng") {
			n++
		}
	}
	return n
}

// TestUnboundedNeverEvicts pins the opt-out. An operator who sets 0 has said
// they want every archive kept, and the bound code must not quietly disagree.
func TestUnboundedNeverEvicts(t *testing.T) {
	src, cdir := t.TempDir(), t.TempDir()
	c, err := New(cdir, WithMaxBytes(0))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		name := fmt.Sprintf("song%02d", i)
		if _, err := c.Path(fmt.Sprintf("%064d", i), song(t, src, name, 4096)); err != nil {
			t.Fatal(err)
		}
	}
	if got := countSNG(t, cdir); got != 6 {
		t.Fatalf("unbounded cache holds %d archives, expected all 6", got)
	}
}

// TestBoundIsEnforced is the headline: the cache stops growing.
func TestBoundIsEnforced(t *testing.T) {
	src, cdir := t.TempDir(), t.TempDir()

	// Pack one song first to learn what an archive of this shape costs, so the
	// bound can be expressed in archives rather than in guessed bytes.
	probe, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	p, err := probe.Path(fmt.Sprintf("%064d", 99), song(t, src, "probe", 4096))
	if err != nil {
		t.Fatal(err)
	}
	one, err := os.Stat(p)
	if err != nil {
		t.Fatal(err)
	}
	max := one.Size() * 3 // room for three

	c, err := New(cdir, WithMaxBytes(max))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 8 {
		if _, err := c.Path(fmt.Sprintf("%064d", i), song(t, src, fmt.Sprintf("song%02d", i), 4096)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond) // keep mtimes distinguishable
	}

	if got := sizeOf(t, cdir); got > max {
		t.Errorf("cache is %d bytes, over its %d byte bound", got, max)
	}
	if got := countSNG(t, cdir); got == 8 {
		t.Errorf("nothing was evicted after 8 inserts into a 3-archive bound")
	}
	if got := countSNG(t, cdir); got == 0 {
		t.Error("everything was evicted; the cache is now useless rather than bounded")
	}
}

// TestTheArchiveJustPackedIsNeverEvicted covers the subtle one. The caller is
// about to serve the file it just asked for; evicting it in the same call would
// hand back a path to something that no longer exists.
func TestTheArchiveJustPackedIsNeverEvicted(t *testing.T) {
	src, cdir := t.TempDir(), t.TempDir()
	// A bound so small that every insert must evict something.
	c, err := New(cdir, WithMaxBytes(1))
	if err != nil {
		t.Fatal(err)
	}
	for i := range 4 {
		p, err := c.Path(fmt.Sprintf("%064d", i), song(t, src, fmt.Sprintf("song%02d", i), 2048))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(p); err != nil {
			t.Fatalf("insert %d returned a path that does not exist: %v", i, err)
		}
	}
}

// TestLeastRecentlyUsedGoesFirst proves it is an LRU and not an arbitrary cull.
func TestLeastRecentlyUsedGoesFirst(t *testing.T) {
	src, cdir := t.TempDir(), t.TempDir()

	probe, _ := New(t.TempDir())
	p, err := probe.Path(fmt.Sprintf("%064d", 99), song(t, src, "probe", 4096))
	if err != nil {
		t.Fatal(err)
	}
	one, _ := os.Stat(p)

	// Room for two archives.
	c, err := New(cdir, WithMaxBytes(one.Size()*2))
	if err != nil {
		t.Fatal(err)
	}

	hashA, hashB := fmt.Sprintf("%064d", 1), fmt.Sprintf("%064d", 2)
	dirA := song(t, src, "songA", 4096)
	if _, err := c.Path(hashA, dirA); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)
	if _, err := c.Path(hashB, song(t, src, "songB", 4096)); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	// Touch A so it is now the MORE recently used of the two. B is the victim.
	if _, err := c.Path(hashA, dirA); err != nil {
		t.Fatal(err)
	}
	time.Sleep(20 * time.Millisecond)

	// A third song forces one eviction.
	if _, err := c.Path(fmt.Sprintf("%064d", 3), song(t, src, "songC", 4096)); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(filepath.Join(cdir, hashA+".sng")); err != nil {
		t.Error("evicted the RECENTLY USED archive; a cache that discards what is being asked for is worse than none")
	}
	if _, err := os.Stat(filepath.Join(cdir, hashB+".sng")); err == nil {
		t.Error("the least recently used archive survived; eviction is not ordered by use")
	}
}

// TestBoundIsEnforcedAtStartNotOnlyOnInsert covers a defect found by deploying
// the bound, not by testing it.
//
// Every test above inserts, so every one of them reached eviction through the
// insert path and all six passed. In the real deployment the cache was already
// full from an earlier unbounded run, every request was a HIT, nothing was ever
// packed, and the cache sat at more than twice its bound indefinitely.
// Enforcing only on insert does not bound a cache; it declines to grow one.
func TestBoundIsEnforcedAtStartNotOnlyOnInsert(t *testing.T) {
	src, cdir := t.TempDir(), t.TempDir()

	// Fill a cache directory with an UNBOUNDED cache first, exactly as the
	// deployment did.
	pre, err := New(cdir)
	if err != nil {
		t.Fatal(err)
	}
	for i := range 6 {
		if _, err := pre.Path(fmt.Sprintf("%064d", i), song(t, src, fmt.Sprintf("song%02d", i), 4096)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(5 * time.Millisecond)
	}
	before := sizeOf(t, cdir)
	if countSNG(t, cdir) != 6 {
		t.Fatalf("setup did not fill the cache: %d archives", countSNG(t, cdir))
	}

	// Now open the SAME directory with a bound well under what is in it, and
	// serve nothing at all.
	max := before / 3
	if _, err := New(cdir, WithMaxBytes(max)); err != nil {
		t.Fatal(err)
	}

	if got := sizeOf(t, cdir); got > max {
		t.Errorf("cache is %d bytes on start against a %d byte bound (was %d); "+
			"a bound that only applies to new inserts does not bound anything", got, max, before)
	}
}

// TestStalePartialsAreSwept covers crash residue. A .partial is not a .sng, so
// it counts against no bound and nothing else would ever remove it.
func TestStalePartialsAreSwept(t *testing.T) {
	cdir := t.TempDir()

	stale := filepath.Join(cdir, "abc.123.partial")
	fresh := filepath.Join(cdir, "def.456.partial")
	for _, p := range []string{stale, fresh} {
		if err := os.WriteFile(p, []byte("half an archive"), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	old := time.Now().Add(-2 * stalePartialAge)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatal(err)
	}

	if _, err := New(cdir); err != nil {
		t.Fatal(err)
	}

	if _, err := os.Stat(stale); err == nil {
		t.Error("a stale .partial survived the sweep; these accumulate forever otherwise")
	}
	if _, err := os.Stat(fresh); err != nil {
		t.Error("a FRESH .partial was swept; that is a pack in progress being deleted under itself")
	}
}

// TestConcurrentRequestsForOneSongPackOnce is the guarantee that predates the
// bound, re-pinned because the lock changed from a per-key map to a fixed set
// of shards.
func TestConcurrentRequestsForOneSongPackOnce(t *testing.T) {
	src, cdir := t.TempDir(), t.TempDir()
	c, err := New(cdir)
	if err != nil {
		t.Fatal(err)
	}
	dir := song(t, src, "shared", 8192)
	hash := fmt.Sprintf("%064d", 7)

	var wg sync.WaitGroup
	paths := make([]string, 8)
	errs := make([]error, 8)
	for i := range 8 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			paths[i], errs[i] = c.Path(hash, dir)
		}()
	}
	wg.Wait()

	for i, err := range errs {
		if err != nil {
			t.Fatalf("goroutine %d: %v", i, err)
		}
		if paths[i] != paths[0] {
			t.Fatalf("goroutine %d got %q, goroutine 0 got %q", i, paths[i], paths[0])
		}
	}
	if got := countSNG(t, cdir); got != 1 {
		t.Fatalf("%d archives for one song; concurrent requests packed more than once", got)
	}
	if left, _ := filepath.Glob(filepath.Join(cdir, "*.partial")); len(left) != 0 {
		t.Fatalf("%d .partial files left behind by concurrent packing", len(left))
	}
}

// TestEvictionNeverStealsAnArchiveBeforeItsPackerCanOpenIt pins the fix for the
// one concurrency defect in this package that reached main.
//
// The shape of it: openOnce packs to a temp file, renames it into place, and
// then opens it. Between the rename and the open the archive is a normal cached
// entry that any other goroutine's enforceBound is free to remove - and with a
// cache bounded to one archive it very often does. The packer then gets ENOENT
// for a file it just wrote, and after three such losses in a row the server
// answers 500 for a song that exists.
//
// This asserts on the COUNTER rather than on Open failing, and that is the
// whole point of the test. Three consecutive losses is rare - CI saw it once in
// 7,680 requests - so a test that waited for the 500 would be a coin flip. A
// single loss is common, so counting losses turns a once-a-pipeline flake into
// a number that is either zero or obviously not.
func TestEvictionNeverStealsAnArchiveBeforeItsPackerCanOpenIt(t *testing.T) {
	const (
		songs   = 24
		workers = 24
		rounds  = 6
		pad     = 32 * 1024
	)
	src, cdir := t.TempDir(), t.TempDir()

	dirs := make([]string, songs)
	hashes := make([]string, songs)
	for i := range songs {
		dirs[i] = song(t, src, fmt.Sprintf("song-%03d", i), pad)
		hashes[i] = fmt.Sprintf("%064d", i)
	}

	// Size the bound from a real archive, then allow exactly one. Every request
	// for a different song therefore evicts, which is what puts traffic through
	// the window this test is about.
	sizer, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	f, err := sizer.Open(hashes[0], dirs[0])
	if err != nil {
		t.Fatal(err)
	}
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	one := st.Size()
	f.Close()

	c, err := New(cdir, WithMaxBytes(one))
	if err != nil {
		t.Fatal(err)
	}

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	for w := range workers {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := range rounds {
				for i := range songs {
					// Stagger the starting song per worker so the workers are
					// not all asking for the same one at the same moment.
					j := (i + w + r) % songs
					f, err := c.Open(hashes[j], dirs[j])
					if err != nil {
						mu.Lock()
						if len(failures) < 5 {
							failures = append(failures, err.Error())
						}
						mu.Unlock()
						continue
					}
					st, err := f.Stat()
					if err == nil && st.Size() == 0 {
						mu.Lock()
						if len(failures) < 5 {
							failures = append(failures, "opened an empty archive")
						}
						mu.Unlock()
					}
					f.Close()
				}
			}
		}()
	}
	wg.Wait()

	if got := c.Vanished(); got != 0 {
		t.Errorf("%d of %d packs were evicted between the rename that published them and the open that claimed them; the window is meant to be closed by holding the eviction lock across it",
			got, workers*rounds*songs)
	}
	if len(failures) > 0 {
		t.Errorf("%d requests failed outright; first few: %v", len(failures), failures)
	}
}

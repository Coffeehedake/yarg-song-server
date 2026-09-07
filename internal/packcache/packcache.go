// Package packcache turns a loose song folder into a .sng on demand and keeps
// the result, up to a bound.
//
// The server serves one wire format, .sng, whatever a song was found as. For a
// folder that means packing it, and packing has to happen somewhere: streaming
// it straight to the response would mean no Content-Length, no Range support
// and no resume, which is the difference between a sync client that can be
// interrupted and one that cannot.
//
// Packing is hash-preserving - the chart is copied byte for byte - so a cached
// archive is as valid as the folder it came from, and the package hash is a
// sound cache key precisely because it is derived from the content.
//
// # Why there is a bound at all
//
// A packed archive is almost exactly the size of the folder it came from.
// Measured on the vault2 deployment, 2026-09-05, against the 22-case corpus:
//
//	library   225,406 bytes
//	cache     229,515 bytes across 22 archives   ratio 1.018
//
// So an unbounded cache over a library of loose folders needs a SECOND COPY of
// the whole library on the data disk. That is survivable on a server with
// terabytes free; it is not survivable on the project's primary target, a
// Raspberry Pi whose library sits on external storage while -data sits on the
// SD card. A 32 GB library would fill a 32 GB card with cache alone.
//
// Eviction costs CPU and latency, never data: an evicted archive is re-packed
// byte-identically from the folder on the next request, and its package hash is
// unchanged because the hash comes from the content.
//
// That sentence was written on 2026-09-05 and was FALSE when written. PackDir
// drew a fresh random mask on every call, so a re-pack produced a different
// archive with the same package hash - which made the strong ETag and every
// Range resume across an eviction dishonest. It was caught the next day by
// syncing two machines from one server and comparing SHA-256s: 16 of 22
// archives differed, and 16 was exactly the number this cache had re-packed.
// The mask is now derived from the package hash (see sng.MaskKeyFor) and the
// sentence is true and measured. Nobody had compared the bytes; the claim was
// reasoned from "the hash comes from the content", which is true of the hash
// and says nothing about the archive.
package packcache

import (
	"errors"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// lockShards is the number of mutexes guarding packing.
//
// Previously this was a map of one mutex per key, which never shrank - a slow
// unbounded leak on a long-lived server with a large library. A fixed array is
// bounded by construction. Two different songs can now collide onto one lock
// and serialise, which with 256 shards is rare and costs one pack of waiting;
// the map cost memory forever.
const lockShards = 256

// stalePartialAge is how old a leftover .partial must be before a sweep removes
// it. A crash or a kill mid-pack orphans one, and nothing else would ever clean
// it up. The window is generous because a legitimate in-progress pack of a very
// large song must never be swept out from under itself.
const stalePartialAge = 1 * time.Hour

// Cache is a directory of packed archives, keyed by package hash.
type Cache struct {
	dir string

	// maxBytes bounds the total size of cached archives. Zero means unbounded,
	// which is a deliberate, explicit choice rather than a default.
	maxBytes int64

	locks [lockShards]sync.Mutex

	// evict serialises eviction so two concurrent inserts cannot both decide to
	// remove the same archives. It is also held across the rename-then-open
	// that publishes a freshly packed archive; see openOnce.
	evict sync.Mutex

	// vanished counts how many times a freshly packed archive was removed
	// before the packing goroutine could open it. It exists to make that
	// window MEASURABLE: it is meant to stay at zero, and a test asserts so.
	vanished atomic.Int64
}

// Vanished reports how many times a freshly packed archive was evicted before
// it could be opened. It should be zero; see openOnce for why, and for what it
// cost to learn that "should be" is not a measurement.
func (c *Cache) Vanished() int64 { return c.vanished.Load() }

// Option configures a Cache.
type Option func(*Cache)

// WithMaxBytes bounds the cache's total size. Zero means unbounded.
func WithMaxBytes(n int64) Option {
	return func(c *Cache) { c.maxBytes = n }
}

// New prepares a cache directory.
func New(dir string, opts ...Option) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("packcache: %w", err)
	}
	c := &Cache{dir: dir}
	for _, o := range opts {
		o(c)
	}
	c.sweepStalePartials()

	// Enforce the bound at START as well as on insert.
	//
	// Found by deploying it rather than by a test, 2026-09-05: a bounded server
	// was pointed at a cache directory left over from an unbounded run, every
	// request was a cache HIT, nothing was ever packed, so enforceBound was
	// never reached and the cache sat at 229,515 bytes against a 102,400 bound
	// indefinitely. Enforcing only on insert does not bound a cache - it merely
	// declines to grow one, which is a different and much weaker promise.
	//
	// The three cases this covers are all ordinary: an operator lowering the
	// bound, a cache that predates the setting, and a library that is fully
	// cached and stable so nothing new is ever packed.
	//
	// Deliberately NOT enforced on every cache hit: that would put a readdir
	// and a stat per file into the hot path of serving a song, to catch a
	// condition that only changes when something is inserted or the process
	// restarts.
	c.enforceBound("")
	return c, nil
}

// Dir is where archives are kept.
func (c *Cache) Dir() string { return c.dir }

// MaxBytes is the configured bound; zero means unbounded.
func (c *Cache) MaxBytes() int64 { return c.maxBytes }

// Open returns an OPEN archive for src, packing it if this is the first
// request. The caller owns the file and must close it.
//
// It hands back an open file rather than a path deliberately, and the reason is
// a measured defect rather than a preference. Eviction runs concurrently with
// serving; a caller that received a *path* had to open it as a second step, and
// in the window between those two an evicting goroutine could remove the
// archive. The caller then got ENOENT for a song that exists, and the server
// answered 404 "this song is no longer where the index says it is; rescan" -
// confidently wrong, and pointing the operator at a library that was fine.
//
// Measured 2026-09-07 on Windows, 64 concurrent clients pulling a 40-song
// library through a cache bounded to two archives: 2,560 requests produced 2,
// 1 and 0 such 404s across three runs - genuine HTTP 404s from the handler, not
// connection failures. Intermittent, rare, and impossible to reproduce by hand,
// which is exactly the profile of a bug that ships.
//
// Holding the handle closes the window on both platforms rather than narrowing
// it: on POSIX an unlinked file stays readable through an open descriptor, and
// on Windows the remove fails while the handle is open and enforceBound simply
// skips that entry. The cost is that a file being served cannot be evicted on
// Windows, so a very busy server holds slightly more cache than its bound. That
// is the right way round: the bound is a disk-space target, and serving a song
// that exists is a correctness promise.
//
// Two requests for the same song pack once; two requests for different songs do
// not block each other unless they collide on a lock shard.
// src may be a loose folder, a .zip or a .7z. The cache does not care which:
// the key is the package hash, which is computed from the song's contents, so a
// song that moves from a folder into a zip keeps the same cache entry and the
// same bytes.
func (c *Cache) openOnce(packageHash, src string) (*os.File, error) {
	if packageHash == "" {
		return nil, fmt.Errorf("packcache: empty package hash")
	}
	dst := filepath.Join(c.dir, packageHash+".sng")

	if f := c.hitOpen(dst); f != nil {
		return f, nil
	}

	lock := c.lockFor(packageHash)
	lock.Lock()
	defer lock.Unlock()

	// Another request may have finished while we waited for the lock.
	if f := c.hitOpen(dst); f != nil {
		return f, nil
	}

	// Write to a temp file and rename. A crash or a full disk mid-pack would
	// otherwise leave a truncated archive at the final name, which every later
	// request would happily serve as if it were whole.
	tmp, err := os.CreateTemp(c.dir, packageHash+".*.partial")
	if err != nil {
		return nil, fmt.Errorf("packcache: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename has succeeded

	if err := scan.PackPath(src, tmp); err != nil {
		tmp.Close()
		return nil, fmt.Errorf("packcache: pack %s: %w", src, err)
	}
	if err := tmp.Close(); err != nil {
		return nil, fmt.Errorf("packcache: %w", err)
	}
	// Publish the archive and claim a handle on it under the eviction lock.
	//
	// enforceBound is the only thing that removes a .sng and it holds c.evict
	// for its whole body, so holding that lock here makes it impossible for
	// another goroutine to remove the archive between the rename that publishes
	// it and the open that claims it. The lock covers two syscalls; eviction
	// itself already holds it across a ReadDir and a series of removes, so this
	// is not the expensive user of it.
	//
	// The lock ordering is shard-then-evict and only ever that way round.
	// enforceBound takes no shard lock, so there is no cycle to deadlock on.
	//
	// This window was closed on 2026-09-07 after CI found it, and the history
	// is worth keeping because the reasoning that left it open was wrong in a
	// way this project keeps repeating.
	//
	// It was described here as real "by inspection" but never reproduced, and a
	// build with the retry below disabled passed five stress runs out of five -
	// so the retry was documented as guarding a gap no measurement had shown
	// firing. Every one of those runs was on WINDOWS, where an open handle
	// blocks os.Remove and enforceBound simply skips the entry; the platform was
	// masking the race, and a Windows-only negative was written up as a fact
	// about the code. On Linux, where an unlink succeeds regardless, CI failed
	// on the first pipeline that ran this test: 1 of 7,680 requests answered
	// 500 for a song that exists. Reproduced directly afterwards on Linux at
	// the packcache level - 8 of 3,456 packs lost their archive in this window -
	// which is what the Vanished counter and its test now hold at zero.
	//
	// The tidier fix - hold a handle on the temp file and rename underneath it,
	// so the window closes by ordering rather than by a lock - does not work,
	// and that too was measured rather than argued: on Windows os.Rename fails
	// outright while the source is open, and it broke every serial baseline
	// request the moment it was tried. It had been reasoned from Go's share
	// flags, and the reasoning was simply wrong.
	c.evict.Lock()
	if err := os.Rename(tmpName, dst); err != nil {
		c.evict.Unlock()
		return nil, fmt.Errorf("packcache: %w", err)
	}
	f, err := os.Open(dst)
	c.evict.Unlock()
	if err != nil {
		// Retained, and now genuinely unreachable by construction rather than
		// by luck: nothing can remove dst while the lock above is held. The
		// counter is here so that "unreachable" stays a measurement.
		c.vanished.Add(1)
		return nil, fmt.Errorf("packcache: %w: %w", errVanished, err)
	}

	// Evict AFTER the insert, and never the archive just written: a caller is
	// about to serve it. enforceBound skips the newest entry for that reason.
	c.enforceBound(dst)
	return f, nil
}

// errVanished marks the one failure that is worth retrying: the archive was
// packed and renamed into place, and an evicting goroutine removed it before it
// could be opened. Every other error means something is actually wrong.
var errVanished = errors.New("packed archive evicted before it could be opened")

// openAttempts bounds the retry. Each attempt is a full re-pack, so a loop that
// never gave up could spin forever against a cache bounded smaller than a single
// archive - a misconfiguration, but one the server should survive rather than
// hang on.
//
// Three was arbitrary when the window it guarded was open, and three losses in
// a row is exactly how that window reached a client: CI answered 500 once in
// 7,680 requests. The window is now closed under the eviction lock, so this
// retry should never run at all. It is kept as a second line against a failure
// mode nobody has thought of yet, and Cache.Vanished counts every time it
// fires so that "should never" does not quietly become "does, rarely".
const openAttempts = 3

// Open returns an OPEN archive for src, packing it if needed. The caller owns
// the file and must close it.
func (c *Cache) Open(packageHash, src string) (*os.File, error) {
	var err error
	for attempt := 0; attempt < openAttempts; attempt++ {
		var f *os.File
		f, err = c.openOnce(packageHash, src)
		if err == nil {
			return f, nil
		}
		if !errors.Is(err, errVanished) {
			return nil, err
		}
	}
	return nil, fmt.Errorf("packcache: gave up after %d attempts: %w", openAttempts, err)
}

// Path returns the path of a packed archive for src, packing it if this is the
// first request.
//
// Prefer Open. A path is a claim about the past: by the time the caller acts on
// it, eviction may have removed the file. Path remains because tests and tools
// that are not serving concurrently find it convenient, and because the
// eviction tests need to assert on paths rather than handles - on Windows an
// open handle prevents the very removal those tests exist to observe.
func (c *Cache) Path(packageHash, src string) (string, error) {
	f, err := c.Open(packageHash, src)
	if err != nil {
		return "", err
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", fmt.Errorf("packcache: %w", err)
	}
	return name, nil
}

// hitOpen returns an open handle to dst when it is a usable cached archive, and
// marks it as recently used. It returns nil when there is no usable archive -
// deliberately not an error, because "not cached yet" is the ordinary case and
// the caller's next step is to pack, not to report a failure.
func (c *Cache) hitOpen(dst string) *os.File {
	f, err := os.Open(dst)
	if err != nil {
		return nil
	}
	st, err := f.Stat()
	if err != nil || st.Size() == 0 {
		f.Close()
		return nil
	}
	now := time.Now()
	_ = os.Chtimes(dst, now, now) // best effort; a read-only data dir must not fail a serve
	return f
}

// hit reports whether dst is a usable cached archive, and marks it as recently
// used if so.
//
// Recency is recorded as the file's own mtime rather than in a map, so the disk
// stays the single source of truth. An in-memory LRU would drift from the
// directory it claims to describe every time the process restarted, and the
// drift would be invisible. The cost is that reading a cached song touches its
// mtime, which a backup tool will see as a change - noted here because it is a
// deliberate trade rather than an oversight.
func (c *Cache) hit(dst string) bool {
	st, err := os.Stat(dst)
	if err != nil || st.Size() == 0 {
		return false
	}
	now := time.Now()
	_ = os.Chtimes(dst, now, now) // best effort; a read-only data dir must not fail a serve
	return true
}

// enforceBound removes least-recently-used archives until the cache is within
// maxBytes. It is a no-op when unbounded.
//
// keep names an archive that must survive this pass - the one just packed for a
// caller that is about to serve it.
func (c *Cache) enforceBound(keep string) {
	if c.maxBytes <= 0 {
		return
	}
	c.evict.Lock()
	defer c.evict.Unlock()

	type entry struct {
		path string
		size int64
		used time.Time
	}
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	var total int64
	var list []entry
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".sng") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		p := filepath.Join(c.dir, e.Name())
		total += info.Size()
		if p == keep {
			continue // counted against the bound, never a candidate
		}
		list = append(list, entry{path: p, size: info.Size(), used: info.ModTime()})
	}
	if total <= c.maxBytes {
		return
	}

	sort.Slice(list, func(i, j int) bool { return list[i].used.Before(list[j].used) })
	for _, e := range list {
		if total <= c.maxBytes {
			return
		}
		// On POSIX an unlinked file stays readable through any open descriptor,
		// so removing an archive mid-download is safe and the reader finishes.
		// On Windows the remove fails while a handle is open; skipping that
		// entry and trying the next is the whole handling required.
		if err := os.Remove(e.path); err != nil {
			continue
		}
		total -= e.size
	}
}

// sweepStalePartials removes .partial files left by a crash or a kill mid-pack.
// Nothing else would ever clean them up, and they count against no bound
// because they are not .sng - so without this they accumulate silently forever.
func (c *Cache) sweepStalePartials() {
	entries, err := os.ReadDir(c.dir)
	if err != nil {
		return
	}
	cutoff := time.Now().Add(-stalePartialAge)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".partial") {
			continue
		}
		info, err := e.Info()
		if err != nil || info.ModTime().After(cutoff) {
			continue
		}
		_ = os.Remove(filepath.Join(c.dir, e.Name()))
	}
}

func (c *Cache) lockFor(key string) *sync.Mutex {
	h := fnv.New32a()
	_, _ = h.Write([]byte(key))
	return &c.locks[h.Sum32()%lockShards]
}

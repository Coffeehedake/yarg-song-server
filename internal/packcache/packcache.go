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
package packcache

import (
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
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
	// remove the same archives.
	evict sync.Mutex
}

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
	return c, nil
}

// Dir is where archives are kept.
func (c *Cache) Dir() string { return c.dir }

// MaxBytes is the configured bound; zero means unbounded.
func (c *Cache) MaxBytes() int64 { return c.maxBytes }

// Path returns the path of a packed archive for srcDir, packing it if this is
// the first request.
//
// Two requests for the same song pack once; two requests for different songs do
// not block each other unless they collide on a lock shard.
func (c *Cache) Path(packageHash, srcDir string) (string, error) {
	if packageHash == "" {
		return "", fmt.Errorf("packcache: empty package hash")
	}
	dst := filepath.Join(c.dir, packageHash+".sng")

	if c.hit(dst) {
		return dst, nil
	}

	lock := c.lockFor(packageHash)
	lock.Lock()
	defer lock.Unlock()

	// Another request may have finished while we waited for the lock.
	if c.hit(dst) {
		return dst, nil
	}

	// Write to a temp file and rename. A crash or a full disk mid-pack would
	// otherwise leave a truncated archive at the final name, which every later
	// request would happily serve as if it were whole.
	tmp, err := os.CreateTemp(c.dir, packageHash+".*.partial")
	if err != nil {
		return "", fmt.Errorf("packcache: %w", err)
	}
	tmpName := tmp.Name()
	defer func() { _ = os.Remove(tmpName) }() // no-op once the rename has succeeded

	if err := scan.PackDir(srcDir, tmp); err != nil {
		tmp.Close()
		return "", fmt.Errorf("packcache: pack %s: %w", srcDir, err)
	}
	if err := tmp.Close(); err != nil {
		return "", fmt.Errorf("packcache: %w", err)
	}
	if err := os.Rename(tmpName, dst); err != nil {
		return "", fmt.Errorf("packcache: %w", err)
	}

	// Evict AFTER the insert, and never the archive just written: a caller is
	// about to serve it. enforceBound skips the newest entry for that reason.
	c.enforceBound(dst)
	return dst, nil
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

// Package packcache turns a loose song folder into a .sng on demand and keeps
// the result.
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
package packcache

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// Cache is a directory of packed archives, keyed by package hash.
type Cache struct {
	dir string

	mu    sync.Mutex
	locks map[string]*sync.Mutex // one lock per key, so two requests for the same song pack once
}

// New prepares a cache directory.
func New(dir string) (*Cache, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("packcache: %w", err)
	}
	return &Cache{dir: dir, locks: make(map[string]*sync.Mutex)}, nil
}

// Dir is where archives are kept.
func (c *Cache) Dir() string { return c.dir }

// Path returns the path of a packed archive for srcDir, packing it if this is
// the first request.
//
// Two requests for the same song pack once; two requests for different songs do
// not block each other.
func (c *Cache) Path(packageHash, srcDir string) (string, error) {
	if packageHash == "" {
		return "", fmt.Errorf("packcache: empty package hash")
	}
	dst := filepath.Join(c.dir, packageHash+".sng")

	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
		return dst, nil
	}

	lock := c.lockFor(packageHash)
	lock.Lock()
	defer lock.Unlock()

	// Another request may have finished while we waited for the lock.
	if st, err := os.Stat(dst); err == nil && st.Size() > 0 {
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
	return dst, nil
}

func (c *Cache) lockFor(key string) *sync.Mutex {
	c.mu.Lock()
	defer c.mu.Unlock()
	l, ok := c.locks[key]
	if !ok {
		l = &sync.Mutex{}
		c.locks[key] = l
	}
	return l
}

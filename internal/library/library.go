// Package library is the server's in-memory index of a scanned song library.
//
// It holds what a browse request needs and nothing else: the catalog entry, the
// precomputed sort keys, and enough to locate the package on disk when someone
// asks for the bytes. Persistence, if it is ever wanted, belongs underneath
// this - see docs/ADR-002-v1-store.md for why v1 keeps the whole thing in
// memory and rebuilds on start.
package library

import (
	"fmt"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/scan"
	"github.com/coffeehedake/yarg-song-server/internal/sortkey"
)

// SourceKind is the shape a song was found in, which decides how the server
// produces .sng bytes for it: stream the file, or pack the folder.
type SourceKind string

const (
	SourceSNG SourceKind = "sng"
	SourceDir SourceKind = "dir"
	SourceZip SourceKind = "zip"
	Source7z  SourceKind = "7z"
)

// NeedsPacking reports whether producing this song's bytes means packing it.
//
// Everything except a .sng needs packing, and the check is written that way
// round on purpose: a new container added later is packed by default, which is
// correct, rather than silently served as raw bytes because somebody forgot to
// extend a list of kinds. The failure mode of the inverted test is handing a
// client a .zip and calling it a .sng.
func (k SourceKind) NeedsPacking() bool { return k != SourceSNG }

// Entry is one indexed song.
type Entry struct {
	Song *catalog.Song

	// Kind and Path say how to produce this song's bytes. Path is a path on the
	// SERVER's filesystem and must never reach a client - catalog.Song already
	// carries SourcePath, which is relative to the library root and is the only
	// location a client is entitled to see.
	Kind SourceKind
	Path string

	// Sort keys, precomputed because they are compared O(n log n) times per
	// request and each one costs a Unicode normalisation.
	Name, Artist, Album sortkey.Key
	Genre, Subgenre     sortkey.Key
	Charter, Source     sortkey.Key
	Playlist            sortkey.Key
	searchable          string
}

// newEntry precomputes everything a query touches.
func newEntry(song *catalog.Song, kind SourceKind, path string) *Entry {
	e := &Entry{
		Song:     song,
		Kind:     kind,
		Path:     path,
		Name:     sortkey.New(song.Name),
		Artist:   sortkey.New(song.Artist),
		Album:    sortkey.New(song.Album),
		Genre:    sortkey.New(song.Genre),
		Subgenre: sortkey.New(song.Subgenre),
		Charter:  sortkey.New(song.Charter),
		Source:   sortkey.New(song.Source),
		Playlist: sortkey.New(song.Playlist),
	}
	// One string to scan per entry rather than eight. The separator keeps a
	// query from matching across a field boundary.
	e.searchable = strings.Join([]string{
		e.Name.Search, e.Artist.Search, e.Album.Search, e.Genre.Search,
		e.Subgenre.Search, e.Charter.Search, e.Source.Search, e.Playlist.Search,
	}, "\x00")
	return e
}

// Index is a whole library, ready to be queried. Safe for concurrent readers;
// Build produces a new one rather than mutating an existing one, so a rescan
// never shows a half-built library to a request in flight.
type Index struct {
	entries []*Entry

	// byChart is hash -> MANY, mirroring upstream's
	// Dictionary<HashWrapper, List<SongEntry>>. Two packages with the same
	// chart and different audio share an identity by design; collapsing them
	// here would silently drop content.
	byChart map[string][]*Entry

	byPackage map[string]*Entry

	// Problems are directories and archives the scan could not read. They are
	// kept rather than logged and forgotten: a library that silently indexes
	// 9,000 of 10,000 songs is impossible to debug from the outside.
	Problems []Problem

	// DuplicatePackages counts entries whose package hash was already present -
	// the same package sitting in two places. The first one found wins for
	// serving, which is safe because identical package hashes mean identical
	// bytes.
	DuplicatePackages int

	BuiltAt time.Time
	Root    string
}

// Problem is one thing the scan could not read.
type Problem struct {
	Path string `json:"path"`
	Err  string `json:"error"`
}

// Build walks root and indexes everything it finds.
//
// A song that fails to scan is recorded as a Problem and the walk continues,
// because one unreadable folder must not cost a library.
func Build(root string) (*Index, error) {
	ix := &Index{
		byChart:   make(map[string][]*Entry),
		byPackage: make(map[string]*Entry),
		BuiltAt:   time.Now().UTC(),
		Root:      root,
	}

	err := scan.WalkLibrary(root, func(r scan.Result) {
		if r.Err != nil {
			ix.Problems = append(ix.Problems, Problem{Path: r.Path, Err: r.Err.Error()})
			return
		}
		if r.Song == nil {
			return
		}
		song := r.Song
		song.SourcePath = r.Path

		kind := SourceDir
		switch strings.ToLower(filepath.Ext(r.Path)) {
		case ".sng":
			kind = SourceSNG
		case ".zip":
			kind = SourceZip
		case ".7z":
			kind = Source7z
		}
		e := newEntry(song, kind, filepath.Join(root, filepath.FromSlash(r.Path)))

		ix.entries = append(ix.entries, e)
		ix.byChart[song.ChartHash] = append(ix.byChart[song.ChartHash], e)
		if _, seen := ix.byPackage[song.PackageHash]; seen {
			ix.DuplicatePackages++
		} else {
			ix.byPackage[song.PackageHash] = e
		}
	})
	if err != nil {
		return nil, fmt.Errorf("build index: %w", err)
	}
	return ix, nil
}

// Len is how many songs are indexed.
func (ix *Index) Len() int { return len(ix.entries) }

// DistinctCharts is how many distinct chart hashes are indexed, which is the
// number a client compares against when it asks what it is missing. It is not
// Len: two packages sharing a chart count once.
func (ix *Index) DistinctCharts() int { return len(ix.byChart) }

// ByChartHash returns every entry sharing a chart hash. The slice is the
// index's own and must not be modified.
func (ix *Index) ByChartHash(hash string) []*Entry {
	return ix.byChart[strings.ToLower(hash)]
}

// ByPackageHash returns the entry with this package hash, or nil.
func (ix *Index) ByPackageHash(hash string) *Entry {
	return ix.byPackage[strings.ToLower(hash)]
}

// Missing answers the client's "what do I not have".
//
// The client sends the chart hashes it already holds; the answer is every hash
// in the library that was not in that list. Chart hash, not package hash, on
// purpose: a client that has the chart can play the song, and re-sending it a
// package that differs only in album art is bandwidth spent on nothing.
func (ix *Index) Missing(clientHas []string) []string {
	have := make(map[string]struct{}, len(clientHas))
	for _, h := range clientHas {
		have[strings.ToLower(strings.TrimSpace(h))] = struct{}{}
	}

	missing := make([]string, 0)
	for hash := range ix.byChart {
		if _, ok := have[hash]; !ok {
			missing = append(missing, hash)
		}
	}
	// Map iteration order is random in Go; an unstable answer to an idempotent
	// question is a bad API and impossible to diff between two calls.
	sort.Strings(missing)
	return missing
}

// Query is a browse or search request.
type Query struct {
	// Text is matched, after the client's own normalisation, as a substring of
	// any of the eight text attributes. See Index.Query.
	Text string

	// SortBy is one of the twelve attributes the client sorts by.
	SortBy Attribute

	// Descending reverses the whole ordering, tie-breakers included.
	Descending bool

	Limit  int
	Offset int
}

// Page is one page of results plus the unpaged total, so a caller can render
// "showing 50 of 3,412" without asking twice.
type Page struct {
	Total   int
	Offset  int
	Entries []*Entry
}

// DefaultLimit is used when a query does not say. MaxLimit caps what a caller
// may ask for, because a library can hold tens of thousands of songs and a
// single unbounded response would be tens of megabytes of JSON.
const (
	DefaultLimit = 50
	MaxLimit     = 500
)

// Query filters, orders and pages the library.
//
// The ORDERING agrees with the client: keys are normalised exactly as
// SortString does and the tie-breakers are upstream's comparer chains. The
// MATCHING does not claim to: upstream's search has its own logic that has not
// been read, so this is a plain substring test over the eight text attributes,
// which is ours and is documented as ours.
func (ix *Index) Query(q Query) Page {
	matched := ix.filter(q.Text)

	cmp := comparerFor(q.SortBy)
	sort.SliceStable(matched, func(i, j int) bool {
		c := cmp(matched[i], matched[j])
		if q.Descending {
			return c > 0
		}
		return c < 0
	})

	total := len(matched)
	offset := q.Offset
	if offset < 0 {
		offset = 0
	}
	if offset > total {
		offset = total
	}
	limit := q.Limit
	switch {
	case limit <= 0:
		limit = DefaultLimit
	case limit > MaxLimit:
		limit = MaxLimit
	}
	end := offset + limit
	if end > total {
		end = total
	}

	return Page{Total: total, Offset: offset, Entries: matched[offset:end]}
}

func (ix *Index) filter(text string) []*Entry {
	if strings.TrimSpace(text) == "" {
		out := make([]*Entry, len(ix.entries))
		copy(out, ix.entries)
		return out
	}
	// The query goes through the client's own folding, so "bjork" finds
	// "Björk" and "the beatles" finds "The Beatles".
	needle := sortkey.New(text).Search
	if needle == "" {
		out := make([]*Entry, len(ix.entries))
		copy(out, ix.entries)
		return out
	}

	var out []*Entry
	for _, e := range ix.entries {
		if strings.Contains(e.searchable, needle) {
			out = append(out, e)
		}
	}
	return out
}

// A rebuildable holder, so an HTTP handler can read a consistent index while a
// rescan runs.
type Store struct {
	mu sync.RWMutex
	ix *Index
}

// NewStore wraps an index.
func NewStore(ix *Index) *Store { return &Store{ix: ix} }

// Get returns the current index. Never nil once NewStore has been called.
func (s *Store) Get() *Index {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.ix
}

// Replace swaps in a freshly built index atomically.
func (s *Store) Replace(ix *Index) {
	s.mu.Lock()
	s.ix = ix
	s.mu.Unlock()
}

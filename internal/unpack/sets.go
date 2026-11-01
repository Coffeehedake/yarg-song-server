package unpack

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// A large folder exported from Google Drive arrives as several archives, each holding a slice
// of ONE tree:
//
//	Rock Band 2 DLC-20261101T163956Z-1-001.zip
//	Rock Band 2 DLC-20261101T163956Z-1-002.zip   ... and fourteen more
//
// Unpacked one at a time they produce "Rock Band 2 DLC", "Rock Band 2 DLC (2)", and so on -
// sixteen directories, no song whole, and nothing saying so.
//
// The dangerous case is subtler and was measured on this library. "Nirvana discography" is two
// parts; part one reports 158 SONGS and looks entirely complete, because songRoots finds every
// folder containing a chart file and the charts happen to live in part one. Unpacking it alone
// would produce 158 song folders, pass the song-count verification, and silently be missing
// whatever audio lives in part two. An archive that fails loudly is a much better problem than
// a library that is quietly wrong.
//
// So a part of a set is never unpacked on its own. The set is the unit.

// partPattern matches the Google Takeout naming. The last two numbers are the export's own
// sequence and the part index; only the second matters here.
var partPattern = regexp.MustCompile(`^(.*)-(\d{8}T\d{6}Z)-(\d+)-(\d+)\.(zip|7z)$`)

// Set is several archives that together hold one tree.
type Set struct {
	// Base is the name the tree had before it was split, and the directory it unpacks into.
	Base string `json:"base"`
	// Parts are library-relative paths in the order they must be extracted.
	Parts []string `json:"parts"`
	// Bytes is what the parts occupy on disk, as a guide to what unpacking needs.
	Bytes int64 `json:"bytes"`
}

// partOf reports the set a filename belongs to, and which part it is.
func partOf(name string) (base string, index int, ok bool) {
	m := partPattern.FindStringSubmatch(name)
	if m == nil {
		return "", 0, false
	}
	n, err := strconv.Atoi(m[4])
	if err != nil {
		return "", 0, false
	}
	return m[1], n, true
}

// FindSets groups the archives under dir into the sets they belong to.
//
// A single part on its own is NOT a set. Plenty of Takeout exports are small enough to come
// back as one file, and treating those as a one-part set would refuse to unpack a perfectly
// ordinary archive. Measured on this library: of nine Takeout-named archives, six were single
// and three were genuinely split.
func FindSets(dir string) ([]Set, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("unpack: read %s: %w", dir, err)
	}

	type part struct {
		name  string
		index int
		size  int64
	}
	groups := map[string][]part{}

	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		base, idx, ok := partOf(e.Name())
		if !ok {
			continue
		}
		size := int64(0)
		if info, ierr := e.Info(); ierr == nil {
			size = info.Size()
		}
		groups[base] = append(groups[base], part{name: e.Name(), index: idx, size: size})
	}

	var out []Set
	for base, parts := range groups {
		if len(parts) < 2 {
			continue
		}
		sort.Slice(parts, func(i, j int) bool { return parts[i].index < parts[j].index })

		s := Set{Base: base}
		for _, p := range parts {
			s.Parts = append(s.Parts, p.name)
			s.Bytes += p.size
		}
		out = append(out, s)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Base < out[j].Base })
	return out, nil
}

// SetFor reports the set an archive belongs to, if it is part of one.
//
// This is the guard. Unpacking a single part of a split set is refused by pointing at this,
// because the alternative is a library that looks right and is not.
func SetFor(dir, name string) (*Set, error) {
	base, _, ok := partOf(filepath.Base(name))
	if !ok {
		return nil, nil
	}
	sets, err := FindSets(dir)
	if err != nil {
		return nil, err
	}
	for i := range sets {
		if sets[i].Base == base {
			return &sets[i], nil
		}
	}
	return nil, nil
}

// InspectSet reports what unpacking a whole set would do, reading headers only.
func (o Options) InspectSet(dir string, set Set) (*Plan, error) {
	plan := &Plan{
		Archive: set.Base + " (" + strconv.Itoa(len(set.Parts)) + " parts)",
	}

	// Song roots are read from the stored names in every part and unioned, because a part may
	// legitimately contain none - the charts can all live in part one while the audio is
	// spread across the rest.
	roots := map[string]bool{}
	seen := map[string]bool{}

	for _, p := range set.Parts {
		full := filepath.Join(dir, p)
		entries, bytes, err := scan.ArchiveSize(full)
		if err != nil {
			return nil, fmt.Errorf("unpack: %s: %w", p, err)
		}
		plan.Entries += entries
		plan.Bytes += bytes

		_, rr, closer, aerr := scan.OpenContainerAll(full)
		if aerr == nil {
			for _, r := range rr {
				roots[r] = true
			}
			closer.Close()
		}

		changes, cerr := scan.ArchiveNameChanges(full)
		if cerr == nil {
			for _, c := range changes {
				if seen[c.Stored] {
					continue
				}
				seen[c.Stored] = true
				plan.NameChanges = append(plan.NameChanges, c)
			}
		}
	}
	plan.Songs = len(roots)

	dest, err := o.destForBase(dir, set.Base)
	if err != nil {
		return nil, err
	}
	plan.Dest = filepath.ToSlash(mustRel(o.LibraryRoot, dest))
	return plan, nil
}

// RunSet unpacks every part of a set into one directory.
func (o Options) RunSet(dir string, set Set) (*Result, error) {
	plan, err := o.InspectSet(dir, set)
	if err != nil {
		return nil, err
	}

	dest := filepath.Join(o.LibraryRoot, filepath.FromSlash(plan.Dest))
	staging := dest + scan.InProgressSuffix
	if err := os.RemoveAll(staging); err != nil {
		return nil, fmt.Errorf("unpack: clear %s: %w", staging, err)
	}

	files := 0
	var written int64
	for _, p := range set.Parts {
		// In part order, and merging rather than replacing: the parts are slices of one tree.
		f, w, perr := scan.ExtractMergeInto(filepath.Join(dir, p), staging)
		files += f
		written += w
		if perr != nil {
			os.RemoveAll(staging)
			return nil, fmt.Errorf("unpack: %s: %w; nothing was kept and the parts are untouched", p, perr)
		}
	}

	// Verified against the union of roots across all parts, which is the only count that means
	// anything for a set. A per-part figure would be meaningless: a part holding audio and no
	// charts reports zero songs and is not wrong.
	found := 0
	if werr := scan.WalkLibrary(staging, func(r scan.Result) {
		if r.Err == nil && r.Song != nil {
			found++
		}
	}); werr != nil {
		os.RemoveAll(staging)
		return nil, fmt.Errorf("unpack: verify %s: %w", plan.Dest, werr)
	}
	if found < plan.Songs {
		os.RemoveAll(staging)
		return nil, fmt.Errorf("unpack: the parts hold %d songs but only %d were readable after "+
			"extracting; nothing was kept and the parts are untouched", plan.Songs, found)
	}

	if err := os.Rename(staging, dest); err != nil {
		os.RemoveAll(staging)
		return nil, fmt.Errorf("unpack: publish %s: %w", plan.Dest, err)
	}

	return &Result{Plan: *plan, FilesWritten: files, BytesWritten: written, SongsFound: found}, nil
}

// destForBase picks the directory a set unpacks into, never merging into one already there.
func (o Options) destForBase(dir, base string) (string, error) {
	parent := dir
	if o.Dir != "" {
		parent = o.Dir
	}
	candidate := filepath.Join(parent, base)
	for i := 2; i < 1000; i++ {
		_, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			if _, serr := os.Stat(candidate + scan.InProgressSuffix); os.IsNotExist(serr) {
				return candidate, nil
			}
		} else if err != nil {
			return "", fmt.Errorf("unpack: %w", err)
		}
		candidate = filepath.Join(parent, fmt.Sprintf("%s (%d)", base, i))
	}
	return "", fmt.Errorf("unpack: cannot find an unused name for %s", base)
}

// describeSet is what the refusal says when somebody names one part of a set.
func describeSet(s *Set) string {
	return fmt.Sprintf("%q is part of a %d-part set (%s). Unpacking one part produces a "+
		"directory that can look complete and be missing files that live in the other parts. "+
		"Unpack the set instead: unpack run-set %q",
		s.Parts[0], len(s.Parts), strings.TrimSpace(s.Base), s.Base)
}

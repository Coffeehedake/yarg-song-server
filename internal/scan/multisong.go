package scan

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
)

// An archive may hold more than one song.
//
// It used to be refused outright - ErrTooManySongs told the operator to unpack it and add the
// songs individually - on the reasoning that picking one would silently publish a song and drop
// the rest. That reasoning was right about the danger and wrong about the remedy: publishing
// ALL of them is not guessing. The refusal stays only for the case nothing here can resolve.
//
// Each song inside an archive is identified by its INNER path, slash-separated and relative to
// the archive root, with "" meaning the archive is itself one song folder. That string is what
// lets the packer find the same song again when a client asks for its bytes.

// ContainerSong is one song found inside an archive.
type ContainerSong struct {
	// Inner is the song folder's path within the archive, "" when the archive root is the
	// song. It is a slash path, because that is what fs.FS uses and what archives store.
	Inner string
	Song  *catalog.Song
}

// songRoots finds every song folder in a container, FROM THE STORED ENTRY NAMES.
//
// It reads names and nothing else. The first version walked the archive as a filesystem,
// calling ReadDir on every directory, and that is pathological on a large solid .7z: each
// listing can force the decompression of a whole block, so a 2 GB archive pinned a core at
// 92% and never finished indexing. Measured on vault2, 2026-10-31, where it took the service
// down until it was rolled back. The old single-song path never hit this because it gave up
// with ErrTooManySongs almost immediately.
//
// A directory is a song when it directly contains a chart file. Both separators are handled,
// because an archive written by an old Windows tool stores backslashes and the fs view cannot
// see into those at all - the same blindness looksLikeASong exists to work around.
//
// A song folder found inside another song folder is dropped: a song may legally contain
// subfolders, and publishing an album-art directory as a track is worse than missing an
// oddity nobody ships.
func songRoots(raw []string) ([]string, error) {
	seen := map[string]bool{}

	for _, n := range raw {
		norm := strings.ReplaceAll(n, "\\", "/")
		norm = strings.TrimPrefix(norm, "./")
		if norm == "" || strings.HasSuffix(norm, "/") {
			continue
		}

		dir, base := path.Split(norm)
		dir = strings.TrimSuffix(dir, "/")
		if _, ok := PickChartFile([]string{base}); !ok {
			continue
		}
		// Depth is bounded as the walk was: a chart buried deeper than this is somebody's
		// backup tree, not a song anyone meant to publish.
		if dir != "" && strings.Count(dir, "/")+1 > maxNesting {
			continue
		}
		seen[dir] = true
	}

	if len(seen) == 0 {
		return nil, ErrNoChart
	}

	roots := make([]string, 0, len(seen))
	for d := range seen {
		roots = append(roots, d)
	}
	sort.Strings(roots)

	// Drop any root nested inside another. Sorted order puts a parent before its children,
	// so one pass is enough.
	out := make([]string, 0, len(roots))
	for _, r := range roots {
		nested := false
		for _, kept := range out {
			if kept == "" || strings.HasPrefix(r, kept+"/") {
				nested = true
				break
			}
		}
		if !nested {
			out = append(out, r)
		}
	}
	return out, nil
}

// OpenContainerAll opens an archive and reports every song folder in it.
//
// Unlike OpenContainer it does not narrow the filesystem to one song: the caller gets the
// archive root and the inner paths, because with several songs there is no single root to
// return. The closer must be called.
func OpenContainerAll(p string) (fs.FS, []string, io.Closer, error) {
	fsys, raw, closer, err := openArchive(p)
	if err != nil {
		return nil, nil, nil, err
	}

	roots, err := songRoots(raw)
	if err != nil {
		// Same distinction OpenContainer draws: an archive that plainly contains a song but
		// yields none is one we failed to READ, which is a different answer from "this is
		// an archive of something else".
		if errors.Is(err, ErrNoChart) && looksLikeASong(raw) {
			err = ErrUnreadableArchive
		}
		closer.Close()
		return nil, nil, nil, err
	}
	return fsys, roots, closer, nil
}

// ScanContainerAll scans every song in an archive.
func ScanContainerAll(p string) ([]ContainerSong, error) {
	fsys, roots, closer, err := OpenContainerAll(p)
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	out := make([]ContainerSong, 0, len(roots))
	var problems []error

	for _, inner := range roots {
		sub := fsys
		if inner != "" {
			sub, err = fs.Sub(fsys, inner)
			if err != nil {
				// This used to be where console-era archives died. fs.Sub refuses a path
				// that is not valid UTF-8 - fs.ValidPath starts with utf8.ValidString - and
				// those archives store names in a legacy encoding, so "Man\xa0 - Oye Mi
				// Amor" failed here with "invalid argument" and took its archive with it.
				//
				// archiveFS decodes such names now, so a path arriving here is one that
				// could not be addressed even after that. Still collected rather than
				// returned, because one unreachable song must not cost the other 82.
				problems = append(problems, fmt.Errorf("%s: %w", inner, err))
				continue
			}
		}
		song, err := ScanDir(sub)
		if errors.Is(err, fs.ErrNotExist) {
			// songRoots derived this root from the STORED names, so the archive says a chart
			// is here and the fs view cannot find the folder at all. That is the same finding
			// as "no chart in a folder we could open", reached by a different error, and it
			// must not be quieter. The backslash archive lands here: "Song\notes.mid" is one
			// flat name, so nothing named "Song" exists to open.
			err = ErrNoChart
		}
		if errors.Is(err, ErrNoChart) {
			// The STORED names said this folder holds a chart and the fs view cannot see
			// it. That is the backslash-separator archive: entries written by an old
			// Windows tool surface as a directory with nothing readable under it. An
			// archive we failed to READ is a different answer from an archive of something
			// else, and reporting it is the whole reason ErrUnreadableArchive exists.
			err = ErrUnreadableArchive
		}
		if err != nil {
			// One bad song inside an archive of eighty must not lose the other
			// seventy-nine, and it must not be silent either. Collected and reported
			// together at the end, so the caller gets every song that IS readable plus a
			// named problem for each that is not.
			//
			// This used to return on the first error. Measured on vault2: a single entry
			// whose name is not valid UTF-8 turned an 83-song archive into zero songs and
			// one error, which looks exactly like an archive holding nothing.
			problems = append(problems, fmt.Errorf("%s: %w", inner, err))
			continue
		}
		out = append(out, ContainerSong{Inner: inner, Song: song})
	}

	if len(problems) > 0 {
		return out, fmt.Errorf("%d of %d songs in this archive could not be read: %w",
			len(problems), len(roots), errors.Join(problems...))
	}
	return out, nil
}

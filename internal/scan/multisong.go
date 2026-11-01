package scan

import (
	"errors"
	"io"
	"io/fs"
	"path"

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

// songRoots finds every song folder in a container.
//
// A folder containing a chart is a song and is NOT descended into: a song folder may legally
// contain subfolders, and treating those as further songs would publish an album's artwork
// directory as a track. Depth is bounded the same way songRoot bounds it, because an unbounded
// descent on a hostile archive is a denial of service rather than a thorough search.
func songRoots(fsys fs.FS) ([]string, error) {
	var found []string

	var walk func(cur fs.FS, prefix string, depth int) error
	walk = func(cur fs.FS, prefix string, depth int) error {
		if depth >= maxNesting {
			return nil
		}

		names, err := rootNames(cur)
		if err != nil {
			return err
		}
		if _, ok := PickChartFile(names); ok {
			found = append(found, prefix)
			return nil
		}

		dirs, err := rootDirs(cur)
		if err != nil {
			return err
		}
		for _, d := range dirs {
			sub, err := fs.Sub(cur, d)
			if err != nil {
				// One unreadable subtree must not lose the rest of the archive.
				continue
			}
			next := d
			if prefix != "" {
				next = path.Join(prefix, d)
			}
			if err := walk(sub, next, depth+1); err != nil {
				return err
			}
		}
		return nil
	}

	if err := walk(fsys, "", 0); err != nil {
		return nil, err
	}
	if len(found) == 0 {
		return nil, ErrNoChart
	}
	return found, nil
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

	roots, err := songRoots(fsys)
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
	for _, inner := range roots {
		sub := fsys
		if inner != "" {
			sub, err = fs.Sub(fsys, inner)
			if err != nil {
				return nil, err
			}
		}
		song, err := ScanDir(sub)
		if err != nil {
			// One bad song inside an archive of twenty must not lose the other nineteen,
			// and it must not be silent either. It is reported as its own problem by the
			// walk, which is why this returns what it has alongside the error.
			return out, err
		}
		out = append(out, ContainerSong{Inner: inner, Song: song})
	}
	return out, nil
}

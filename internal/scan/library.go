package scan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// Result is one entry produced by walking a library: either a song or the
// reason a promising-looking directory or archive could not be read.
//
// A failure is reported rather than returned, because one unreadable song must
// not abandon a scan of ten thousand. A library scan that stops at the first
// bad file is a library scan nobody can use.
type Result struct {
	// Path is the folder or .sng file this came from, relative to the root.
	Path string
	Song *catalog.Song
	Err  error
}

// WalkLibrary walks root and reports every song it finds.
//
// Two shapes are recognised: a directory containing a chart file, and a .sng
// file. Directories are not pruned after a match - a folder that contains both
// a song and a subfolder of songs is unusual but legal, and skipping the
// subtree would silently lose content.
func WalkLibrary(root string, emit func(Result)) error {
	return filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			// An unreadable directory is reported and stepped over, not fatal.
			emit(Result{Path: rel(root, p), Err: err})
			if d != nil && d.IsDir() {
				return filepath.SkipDir
			}
			return nil
		}

		if d.IsDir() {
			song, serr := ScanDir(os.DirFS(p))
			if errors.Is(serr, ErrNoChart) {
				return nil // an ordinary folder on the way to a song
			}
			emit(Result{Path: rel(root, p), Song: song, Err: serr})
			return nil
		}

		// A console package is REPORTED, not ignored. Decrypting one is a
		// permanent non-goal, but an operator who drops a 2 GB _rb3con into the
		// library and sees nothing at all concludes the server is broken. A
		// stated refusal costs one line and answers the question.
		if IsRockBandPackage(d.Name()) {
			emit(Result{Path: rel(root, p), Err: ErrRockBandPackage})
			return nil
		}

		if IsContainer(d.Name()) {
			song, serr := ScanContainer(p)
			if errors.Is(serr, ErrNoChart) {
				// A zip of something else entirely. Libraries contain plenty of
				// archives that are not songs; those are not errors.
				//
				// Note this is ErrNoChart specifically, NOT every failure.
				// ErrUnreadableArchive - an archive that visibly holds a song we
				// could not read - falls through and is reported, because
				// swallowing that one is how a legitimate song disappears with
				// no explanation.
				return nil
			}
			emit(Result{Path: rel(root, p), Song: song, Err: serr})
			return nil
		}

		if !strings.EqualFold(filepath.Ext(d.Name()), ".sng") {
			return nil
		}
		song, serr := scanSNGFile(p)
		emit(Result{Path: rel(root, p), Song: song, Err: serr})
		return nil
	})
}

func scanSNGFile(p string) (*catalog.Song, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, err
	}
	a, err := sng.Open(f, st.Size())
	if err != nil {
		return nil, err
	}
	return ScanArchive(a)
}

func rel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return filepath.ToSlash(r)
}

package scan

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
	"github.com/coffeehedake/yarg-song-server/internal/yargsong"
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
	// Inner is the song's path WITHIN an archive, slash-separated, empty for everything
	// else and for an archive that is itself one song folder. It is what lets the packer
	// find this song again among several in the same file.
	Inner string
	Song  *catalog.Song
	Err   error
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
			// A directory something is still writing is not a library yet. Without this, an
			// archive being unpacked is indexed WHILE it is being written: the walk finds
			// half a song folder, scans whatever files have landed so far, and publishes a
			// chart hash for a song whose audio has not arrived. The suffix is the same
			// device the sync client uses on a part-downloaded file, for the same reason -
			// nothing trusts a name until the thing under it is complete.
			//
			// Skipped by name rather than by asking who is writing, because the answer has to
			// survive the server being restarted in the middle of an unpack.
			// Directories the walk steps over: one still being written, one holding archives
			// an operator has unpacked but kept, one holding arrivals nobody has asked the
			// server to take in yet. None of them is a library, and each would do damage if
			// treated as one - the last most of all, because a file still being copied in
			// hashes to something, and that something would be published as a song.
			//
			// Never the root itself. Pointing --songs at one of these is a deliberate act, and
			// refusing to look at the directory it was explicitly given would be obtuse.
			if p != root && IsReservedDir(d.Name()) {
				return filepath.SkipDir
			}

			song, serr := ScanDir(os.DirFS(p))
			if errors.Is(serr, ErrNoChart) {
				return nil // an ordinary folder on the way to a song
			}
			emit(Result{Path: rel(root, p), Song: song, Err: serr})
			return nil
		}

		// An archive may hold several songs, and each one is emitted separately so a pack of
		// twenty becomes twenty catalogue entries rather than a refusal. ScanFile still
		// exists and still answers for one file, because the upload check needs a single
		// verdict - but the walk is the path that can report many.
		if IsContainer(d.Name()) {
			songs, cerr := ScanContainerAll(p)
			for _, cs := range songs {
				emit(Result{Path: rel(root, p), Inner: cs.Inner, Song: cs.Song})
			}
			switch {
			case cerr == nil:
				return nil
			case errors.Is(cerr, ErrNoChart):
				// An archive of something else entirely. Libraries are full of those.
				return nil
			default:
				// Reported even when some songs came back: an archive that yielded
				// nineteen songs and one error has a real problem in it, and silence
				// about the twentieth is how a song disappears unexplained.
				emit(Result{Path: rel(root, p), Err: cerr})
				return nil
			}
		}

		song, serr := ScanFile(p)
		switch {
		case errors.Is(serr, ErrNotASong):
			// Not a shape this server reads at all. Silent on purpose: a
			// library legitimately contains album art, text files and backups.
			return nil
		case errors.Is(serr, ErrNoChart) && IsContainer(d.Name()):
			// A zip of something else entirely. Libraries contain plenty of
			// archives that are not songs; those are not errors.
			//
			// Note this is ErrNoChart specifically, NOT every failure.
			// ErrUnreadableArchive - an archive that visibly holds a song we
			// could not read - falls through and is reported, because
			// swallowing that one is how a legitimate song disappears with no
			// explanation.
			return nil
		}
		emit(Result{Path: rel(root, p), Song: song, Err: serr})
		return nil
	})
}

// ErrNotASong means a file is not a shape this server reads: not a .sng, not a
// container it opens, not a console package it refuses by name.
//
// It exists so ScanFile can say "nothing to do here" without the caller having
// to re-derive the dispatch from the filename, which is exactly how a second
// caller ends up disagreeing with the first.
var ErrNotASong = errors.New("scan: not a song, an archive this server reads, or a console package")

// ScanFile scans ONE file the way a library walk would.
//
// WalkLibrary calls this, and so does the upload check endpoint. That is the
// entire point of it existing: the verdict a person gets by uploading a file
// has to be the verdict they would get by dropping it in the library, and two
// implementations of the same dispatch would eventually disagree about some
// shape nobody thought to test. The dispatch is on the file NAME - a console
// package is recognised by suffix, a container by extension - so this must stay
// the one place that reads it.
//
// Directories are not handled here; a walk sees those before it sees files, and
// a single uploaded file cannot be one.
func ScanFile(p string) (*catalog.Song, error) {
	name := filepath.Base(p)

	// A console package is REPORTED, not ignored. Decrypting one is a permanent
	// non-goal, but an operator who drops a 2 GB _rb3con into the library and
	// sees nothing at all concludes the server is broken. A stated refusal
	// costs one line and answers the question.
	if IsRockBandPackage(name) {
		return nil, ErrRockBandPackage
	}
	if IsContainer(name) {
		return ScanContainer(p)
	}
	// A .yargsong is a song, not a container: YARN ships setlists this way, and the body
	// under its transform is an ordinary SNG wearing a decoy magic. Scanned through the same
	// ScanArchive as a .sng, because once it is open it IS one.
	if IsYargsong(name) {
		return scanYargsongFile(p)
	}
	if !strings.EqualFold(filepath.Ext(name), ".sng") {
		return nil, ErrNotASong
	}
	return scanSNGFile(p)
}

func scanYargsongFile(p string) (*catalog.Song, error) {
	a, r, err := yargsong.OpenArchive(p)
	if err != nil {
		return nil, err
	}
	defer r.Close()
	return ScanArchive(a)
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

package scan

import (
	"archive/zip"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/bodgit/sevenzip"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
)

// A song reaches this server in one of four shapes: a loose folder, a .sng, a
// .zip or a .7z. Three of those are just a filesystem with a chart in it, and
// this file is what makes them interchangeable.
//
// Nothing here needed an adapter. Both archive/zip's Reader and sevenzip's
// Reader already expose Open(name) (fs.File, error), so both satisfy fs.FS, and
// the scanner - which has taken an fs.FS since Phase 1 - reads them unchanged.
// The .sng path stays separate for a real reason rather than an accidental one:
// a .sng carries its metadata in the archive HEADER rather than in a contained
// song.ini, so it is scanned by ScanArchive, not by ScanDir.

// maxNesting bounds how far into an archive we will look for the song folder.
// One level is the common case (Song.zip containing Song/); more than a couple
// is somebody's backup tree, not a song, and an unbounded descent on a hostile
// archive is a denial of service.
const maxNesting = 6

// ErrRockBandPackage means the file is a Rock Band console package. It is
// recognised deliberately so the operator gets a reason rather than silence:
// a file that is simply ignored looks like a broken server.
var ErrRockBandPackage = errors.New("scan: Rock Band console package; its audio is encrypted and decrypting it is a permanent non-goal of this project, so the file is skipped rather than half-read")

// ErrTooManySongs means an archive holds several song folders. The roadmap
// promises "a .zip of a loose folder", singular, and guessing which one the
// operator meant would silently publish one song and drop the rest.
var ErrTooManySongs = errors.New("scan: archive holds more than one song folder; unpack it and add the songs individually")

// ErrUnreadableArchive means an archive plainly contains a song - there is a
// song.ini or a chart file somewhere inside it - but no song folder could be
// resolved from it.
//
// This exists because the alternative is silence. A library legitimately holds
// archives that are not songs, so "no chart here" has to be ignored quietly or
// every holiday-photos zip becomes a problem entry. But an archive that clearly
// DOES hold a song and still cannot be read is a different thing, and swallowing
// it is the same failure as silently ignoring a Rock Band package: the operator
// sees nothing appear and concludes the server is broken.
//
// The known cause is an archive whose entries use BACKSLASH separators, which
// some Windows tools still write. Measured 2026-09-06: Go's zip filesystem
// surfaces a directory for such an entry but no readable file underneath it, so
// the chart is never found. Re-zipping with any modern tool fixes it.
var ErrUnreadableArchive = errors.New("scan: archive contains a song.ini or a chart but no song folder could be read from it; if it was made by an old Windows tool it may use backslash path separators - re-zipping it will fix that")

// rbPackageSuffixes are the console-package shapes seen in the wild. `_rb3con`
// is a SUFFIX, not an extension - those files usually have no extension at all,
// so matching on filepath.Ext alone would miss the most common one.
var rbPackageSuffixes = []string{".con", "_rb3con", ".pkg", ".xex"}

// IsRockBandPackage reports whether a filename is a Rock Band console package.
func IsRockBandPackage(name string) bool {
	l := strings.ToLower(name)
	for _, s := range rbPackageSuffixes {
		if strings.HasSuffix(l, s) {
			return true
		}
	}
	return false
}

// IsContainer reports whether a filename is an archive this server can read as
// a song folder. .sng is deliberately excluded: it is a song, not a container
// holding one, and it is served directly rather than repacked.
func IsContainer(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".7z":
		return true
	}
	return false
}

// OpenContainer opens a .zip or .7z and returns a filesystem rooted at the song
// inside it, together with the closer the caller must call.
//
// The returned FS is rooted at the song rather than at the archive, so every
// caller downstream - the scanner, the packer, the package hash - sees exactly
// what it would see for a loose folder. That is what makes a zipped song and an
// unzipped one produce the same bytes.
func OpenContainer(p string) (fs.FS, io.Closer, error) {
	var (
		fsys   fs.FS
		closer io.Closer
		raw    []string // entry names EXACTLY as stored, before any fs view
	)
	switch strings.ToLower(filepath.Ext(p)) {
	case ".zip":
		r, err := zip.OpenReader(p)
		if err != nil {
			return nil, nil, fmt.Errorf("scan: open zip: %w", err)
		}
		for _, f := range r.File {
			raw = append(raw, f.Name)
		}
		fsys, closer = r, r
	case ".7z":
		r, err := sevenzip.OpenReader(p)
		if err != nil {
			return nil, nil, fmt.Errorf("scan: open 7z: %w", err)
		}
		for _, f := range r.File {
			raw = append(raw, f.Name)
		}
		fsys, closer = r, r
	default:
		return nil, nil, fmt.Errorf("scan: %q is not a container this server reads", filepath.Ext(p))
	}

	root, err := songRoot(fsys)
	if err != nil {
		// Before reporting "no chart", ask whether the archive plainly contains
		// a song anyway. If it does, this is an archive we failed to READ, not
		// an archive of something else, and the two deserve different answers.
		if errors.Is(err, ErrNoChart) && looksLikeASong(raw) {
			err = ErrUnreadableArchive
		}
		closer.Close()
		return nil, nil, err
	}
	return root, closer, nil
}

// looksLikeASong reports whether any entry name in the archive is a song.ini or
// a chart file.
//
// It takes the RAW stored names rather than an fs.FS on purpose, and the first
// attempt at this got it wrong: walking the filesystem view inherits exactly the
// blindness this check exists to detect. The backslash case is the proof - the
// fs view shows a directory with nothing readable under it, so an fs.WalkDir
// finds no song.ini and concludes, wrongly, that the silence was justified.
//
// Both separators are handled because the whole point is names the fs view will
// not parse.
func looksLikeASong(rawNames []string) bool {
	for _, n := range rawNames {
		base := strings.ToLower(n)
		if i := strings.LastIndexAny(base, `/\`); i >= 0 {
			base = base[i+1:]
		}
		switch base {
		case "song.ini", "notes.mid", "notes.midi", "notes.chart", "notes.txt":
			return true
		}
	}
	return false
}

// songRoot finds the directory holding the chart, descending through the
// wrapper folders archives usually carry.
//
// Three shapes, in order of how often they turn up:
//   - chart at the archive root
//   - one folder at the root, chart inside it        (the common case)
//   - several folders, exactly one of which is a song
//
// Several song folders is refused rather than resolved. See ErrTooManySongs.
func songRoot(fsys fs.FS) (fs.FS, error) {
	cur := fsys
	for depth := 0; depth < maxNesting; depth++ {
		names, err := rootNames(cur)
		if err != nil {
			return nil, err
		}
		if _, ok := PickChartFile(names); ok {
			return cur, nil
		}

		dirs, err := rootDirs(cur)
		if err != nil {
			return nil, err
		}
		if len(dirs) == 0 {
			return nil, ErrNoChart
		}

		// Which of these directories is itself a song?
		var songs []string
		for _, d := range dirs {
			sub, err := fs.Sub(cur, d)
			if err != nil {
				continue
			}
			subNames, err := rootNames(sub)
			if err != nil {
				continue
			}
			if _, ok := PickChartFile(subNames); ok {
				songs = append(songs, d)
			}
		}
		switch {
		case len(songs) > 1:
			return nil, ErrTooManySongs
		case len(songs) == 1:
			return fs.Sub(cur, songs[0])
		case len(dirs) == 1:
			// No chart yet, but only one way down - keep descending.
			next, err := fs.Sub(cur, dirs[0])
			if err != nil {
				return nil, err
			}
			cur = next
		default:
			return nil, ErrNoChart
		}
	}
	return nil, ErrNoChart
}

// rootDirs lists the directories at the top of fsys.
func rootDirs(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("scan: read archive root: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ScanContainer scans a .zip or .7z holding one loose song folder.
//
// It is ScanDir with a different way of getting to the filesystem, which is the
// whole point: a song does not become a different song because somebody zipped
// it, and no scanning logic is duplicated to prove that.
func ScanContainer(p string) (*catalog.Song, error) {
	fsys, closer, err := OpenContainer(p)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return ScanDir(fsys)
}

// PackPath packs any supported source - loose folder, .zip or .7z - to a .sng.
//
// A .sng is not accepted here: it is already the wire format and is served
// straight off disk rather than repacked.
func PackPath(p string, w io.Writer) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return PackFS(os.DirFS(p), w)
	}
	fsys, closer, err := OpenContainer(p)
	if err != nil {
		return err
	}
	defer closer.Close()
	return PackFS(fsys, w)
}

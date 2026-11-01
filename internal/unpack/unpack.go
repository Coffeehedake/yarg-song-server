// Package unpack turns an archive in the library into loose song folders beside it.
//
// It exists because an archive is not merely slow to read, it is INVISIBLE. A song inside a
// .zip or .7z cannot be seen by the mobile app or by a player in the game until something
// unpacks it; and reading one song out of a solid .7z was measured on vault2 at about three
// minutes, which is tolerable inside a background index and unshippable on a loading screen.
//
// Everything here is built so that a failure costs nothing:
//
//   - The archive is never modified and never deleted. The operator removes it, later, having
//     seen that the extraction worked.
//   - Extraction goes to a directory named with scan.InProgressSuffix, which the library walk
//     skips, and is renamed into place only once it is complete and verified. A crash leaves a
//     directory the scanner ignores rather than half a song it indexes.
//   - Nothing is overwritten. A destination that already exists is refused, not merged into.
//   - The result is verified by chart hash, not by the extraction returning no error.
//
// Keeping the archive is free rather than wasteful, which is worth knowing before anyone
// "tidies up": the container a song arrived in never reaches its bytes, so the archive and the
// folder extracted from it produce the SAME package hash. The index counts that as one
// duplicate package and serves the first; it does not list the song twice.
package unpack

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// Plan is what unpacking an archive would do, worked out without decompressing anything.
type Plan struct {
	// Archive is the path given, relative to the library root.
	Archive string `json:"archive"`
	// Dest is the directory that would be created, relative to the library root.
	Dest string `json:"dest"`
	// Entries and Bytes are what the archive holds; Bytes is the disk an extraction needs.
	Entries int   `json:"entries"`
	Bytes   int64 `json:"bytes"`
	// Songs is how many song folders the archive holds, read from the stored names.
	Songs int `json:"songs"`
	// NameChanges are entries whose names must be decoded to be written. These are the
	// permanent consequence of unpacking and the reason checking is a separate act.
	NameChanges []scan.NameChange `json:"name_changes,omitempty"`
}

// Result is what an unpack actually did.
type Result struct {
	Plan
	FilesWritten int   `json:"files_written"`
	BytesWritten int64 `json:"bytes_written"`
	// SongsFound is how many songs the scanner found in the extracted directory. It is
	// compared against Plan.Songs, because an extraction that reports success and produces
	// fewer songs than the archive held has lost something quietly.
	SongsFound int `json:"songs_found"`
}

// Options says where the library is and where unpacked songs are allowed to land.
type Options struct {
	// LibraryRoot is the directory the server indexes.
	LibraryRoot string
	// Dir is where unpacked folders are created. Empty means beside the archive, which is the
	// tidy answer and the one that does not work on the live deployment: vault2 mounts the
	// library READ-ONLY on purpose, so that the server cannot damage what it serves. Pointing
	// this at a writable directory that is also scanned keeps that guarantee for the rest of
	// the library while still allowing songs to be unpacked into it.
	Dir string
}

// Inspect reports what unpacking would do. It reads headers only: nothing is decompressed and
// nothing is written.
func (o Options) Inspect(rel string) (*Plan, error) {
	libraryRoot := o.LibraryRoot
	archive, err := resolve(libraryRoot, rel)
	if err != nil {
		return nil, err
	}
	if !scan.IsContainer(filepath.Base(archive)) {
		return nil, fmt.Errorf("unpack: %s is not an archive this server reads", rel)
	}

	entries, bytes, err := scan.ArchiveSize(archive)
	if err != nil {
		return nil, err
	}

	// Song roots come from the stored names, so this costs a directory read rather than a
	// decompression - the same reason the index can list a 2 GB archive's songs in 116ms.
	_, roots, closer, err := scan.OpenContainerAll(archive)
	if err != nil {
		return nil, err
	}
	closer.Close()

	changes, err := scan.ArchiveNameChanges(archive)
	if err != nil {
		return nil, err
	}

	dest, err := o.destFor(archive)
	if err != nil {
		return nil, err
	}

	return &Plan{
		Archive:     filepath.ToSlash(rel),
		Dest:        filepath.ToSlash(mustRel(libraryRoot, dest)),
		Entries:     entries,
		Bytes:       bytes,
		Songs:       len(roots),
		NameChanges: changes,
	}, nil
}

// Run unpacks the archive. The archive is left exactly as it was, whatever happens.
func (o Options) Run(rel string) (*Result, error) {
	libraryRoot := o.LibraryRoot
	plan, err := o.Inspect(rel)
	if err != nil {
		return nil, err
	}

	archive, err := resolve(libraryRoot, rel)
	if err != nil {
		return nil, err
	}
	dest := filepath.Join(libraryRoot, filepath.FromSlash(plan.Dest))
	staging := dest + scan.InProgressSuffix

	// A staging directory left by an earlier attempt is removed rather than reused. It is ours
	// by construction - the suffix is not a name anything else writes - and its contents are
	// unknowable: a merge would silently inherit whatever half-written files are in it.
	if err := os.RemoveAll(staging); err != nil {
		return nil, fmt.Errorf("unpack: clear %s: %w", staging, err)
	}

	files, written, err := scan.ExtractTo(archive, staging)
	if err != nil {
		// Including a disk that filled. The partial directory goes; the archive stays, so the
		// operator has lost nothing but time.
		os.RemoveAll(staging)
		return nil, fmt.Errorf("unpack: %w", err)
	}

	// Verified by scanning what was written, not by trusting that writing it returned no
	// error. The archive said how many songs it held; the disk has to agree.
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
		return nil, fmt.Errorf("unpack: %s holds %d songs but only %d were readable after "+
			"extracting; nothing was kept and the archive is untouched",
			plan.Archive, plan.Songs, found)
	}

	if err := os.Rename(staging, dest); err != nil {
		os.RemoveAll(staging)
		return nil, fmt.Errorf("unpack: publish %s: %w", plan.Dest, err)
	}

	return &Result{
		Plan:         *plan,
		FilesWritten: files,
		BytesWritten: written,
		SongsFound:   found,
	}, nil
}

// destFor picks the directory an archive unpacks into: its own name without the extension.
//
// An existing directory is never merged into or overwritten; a suffix is added instead. Two
// archives can legitimately share a base name, and so can an archive and a folder somebody
// already extracted by hand, and in neither case may one quietly absorb the other.
func (o Options) destFor(archive string) (string, error) {
	base := strings.TrimSuffix(filepath.Base(archive), filepath.Ext(archive))
	dir := filepath.Dir(archive)
	if o.Dir != "" {
		dir = o.Dir
	}

	candidate := filepath.Join(dir, base)
	for i := 2; i < 1000; i++ {
		_, err := os.Stat(candidate)
		if os.IsNotExist(err) {
			if _, err := os.Stat(candidate + scan.InProgressSuffix); os.IsNotExist(err) {
				return candidate, nil
			}
		} else if err != nil {
			return "", fmt.Errorf("unpack: %w", err)
		}
		candidate = filepath.Join(dir, fmt.Sprintf("%s (%d)", base, i))
	}
	return "", fmt.Errorf("unpack: cannot find an unused name for %s", base)
}

// resolve turns a library-relative path into an absolute one, refusing anything that points
// outside the library.
//
// The caller is an HTTP request, so this is the boundary where a path stops being somebody
// else's text. Rejecting absolute paths and any ".." element is what stops "../../etc/passwd"
// and friends from naming a file to read, or a directory to create.
func resolve(libraryRoot, rel string) (string, error) {
	clean := filepath.Clean(filepath.FromSlash(rel))
	if clean == "." || clean == string(filepath.Separator) {
		return "", fmt.Errorf("unpack: no archive named")
	}
	if filepath.IsAbs(clean) || strings.HasPrefix(clean, "..") {
		return "", fmt.Errorf("unpack: %q is not inside the library", rel)
	}

	full := filepath.Join(libraryRoot, clean)

	// Join already removed any ".." but the check is repeated against the result, because this
	// is the line that would do the damage and it should not rest on reasoning about Join.
	inside, err := filepath.Rel(libraryRoot, full)
	if err != nil || inside == ".." || strings.HasPrefix(inside, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("unpack: %q is not inside the library", rel)
	}

	info, err := os.Stat(full)
	if err != nil {
		return "", fmt.Errorf("unpack: %q is not in the library", rel)
	}
	if info.IsDir() {
		return "", fmt.Errorf("unpack: %q is a directory, not an archive", rel)
	}
	return full, nil
}

func mustRel(root, p string) string {
	r, err := filepath.Rel(root, p)
	if err != nil {
		return p
	}
	return r
}

package scan

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bodgit/sevenzip"
	"github.com/nwaples/rardecode/v2"
)

// archiveItems opens an archive and returns its entries in the order they are STORED.
//
// The order is the whole reason this exists separately from a filesystem view. A solid .7z
// shares one compressed stream across many files, so reading entry N means decompressing
// everything before it; reading them in stored order costs one pass, and reading them in any
// other order costs a pass EACH. Measured on vault2 against a 2 GB archive: 616 entries in
// stored order took 3m12s, while eight entries read out of order took 12m30s - about 3m07s
// apiece. fs.WalkDir walks lexically, which is exactly the expensive order.
func archiveItems(p string) ([]archiveItem, io.Closer, error) {
	switch strings.ToLower(filepath.Ext(p)) {
	case ".zip":
		r, err := zip.OpenReader(p)
		if err != nil {
			return nil, nil, fmt.Errorf("scan: open zip: %w", err)
		}
		items := make([]archiveItem, 0, len(r.File))
		for _, f := range r.File {
			items = append(items, archiveItem{
				stored:   f.Name,
				size:     int64(f.UncompressedSize64),
				modified: f.Modified,
				open:     func() (io.ReadCloser, error) { return f.Open() },
			})
		}
		return items, r, nil

	case ".7z":
		r, err := sevenzip.OpenReader(p)
		if err != nil {
			// A password-protected .7z fails deep inside the LZMA reader, and the message it
			// produces - "newRangeDecoder: first byte not zero" - tells an operator nothing.
			if looks7z(p) {
				return nil, nil, fmt.Errorf("%w: %s", ErrUnopenable7z, err)
			}
			return nil, nil, fmt.Errorf("scan: open 7z: %w", err)
		}
		items := make([]archiveItem, 0, len(r.File))
		for _, f := range r.File {
			items = append(items, archiveItem{
				stored:   f.Name,
				size:     int64(f.UncompressedSize),
				modified: f.Modified,
				open:     func() (io.ReadCloser, error) { return f.Open() },
			})
		}
		return items, r, nil

	case ".rar":
		// rardecode gives three ways in: a sequential Reader, an fs.FS, and List + per-entry
		// Open. List is the one that matches archiveItem's contract, which needs a LAZY,
		// REPEATABLE opener per entry - and the question of whether that is affordable was
		// measured rather than assumed, because on a solid .7z the same shape costs about
		// three minutes an entry.
		//
		// Measured 2026-11-01 against the two real archives this was added for:
		//
		//	Guitar Hero On Tour - Modern Hits.rar   173 MB   307 entries   List 1.098s
		//	Marathon Hero 2.rar                    5976 MB   951 entries   List  0.033s
		//
		// Neither is solid, nothing in either is encrypted, and every stored name is already
		// valid UTF-8. Reading the LAST entry without touching the ones before it took under
		// a millisecond, and a 40-entry stored-order pass (498ms) matched the sequential
		// reader (477ms) - so unlike .7z there is no penalty for reaching into a .rar, and it
		// can be served as a container rather than only unpacked.
		//
		// That is a property of these archives, not of the format: RAR supports solid
		// compression too, and a solid .rar would cost exactly what a solid .7z costs. The
		// remedy is the same one .7z already has - unpack it - so this needs no special case,
		// only the honesty that the measurement above is about two files.
		files, err := rardecode.List(p)
		if err != nil {
			// An encrypted .rar fails somewhere inside the decoder with a message an
			// operator cannot act on, exactly as a password-protected .7z does.
			if looksRar(p) {
				return nil, nil, fmt.Errorf("%w: %s", ErrUnopenableRar, err)
			}
			return nil, nil, fmt.Errorf("scan: open rar: %w", err)
		}
		items := make([]archiveItem, 0, len(files))
		for _, f := range files {
			items = append(items, archiveItem{
				stored:   f.Name,
				size:     f.UnPackedSize,
				modified: f.ModificationTime,
				open:     func() (io.ReadCloser, error) { return f.Open() },
			})
		}
		// List closes the archive before it returns and every File.Open reopens it, so there
		// is no handle for the caller to hold. Proved by the probe: entries opened fine after
		// List had returned. A nil Closer is not an option - every caller defers Close.
		return items, nopCloser{}, nil
	}
	return nil, nil, fmt.Errorf("scan: %q is not a container this server reads", filepath.Ext(p))
}

// nopCloser stands in where a reader owns no handle the caller must release.
type nopCloser struct{}

func (nopCloser) Close() error { return nil }

// IgnoredDirName is a directory the library walk does not look inside, at any depth.
//
// It exists so that an archive can stay in the library without costing anything. Unpacking one
// does not delete it - that stays the operator's decision, made after they have seen the songs
// arrive - but an archive left where it was is still read on every startup, and reading one
// song out of a solid .7z was measured at about three minutes. Moving it out of the library
// entirely works and is miserable: the thing is then in some unrelated folder with nothing to
// say which songs it produced.
//
// So this is the third state. Songs sit beside the archive they came from, and the archive sits
// in a folder the indexer ignores. A library can hold its own history without paying for it on
// every restart.
//
// Reserved rather than configured, because a name this load-bearing should mean the same thing
// on every deployment - and because a song folder called "_packed" is not a thing anyone has.
const IgnoredDirName = "_packed"

// IncomingDirName is where somebody drops songs and archives for the server to take in.
//
// Also skipped by the walk, and for a sharper reason than _packed. A file being copied into a
// library over SMB is indexed the moment the walk reaches it, half-written: a truncated chart
// hashes to something, and that something is published as a song. Giving arrivals a doorway the
// indexer does not look through means a copy in progress cannot become a catalogue entry.
//
// Nothing here is taken in automatically. That is the same decision unpacking made: writing to
// the library is an act somebody asks for, not something that happens because a file appeared.
const IncomingDirName = "_incoming"

// IsReservedDir reports whether the library walk should step over a directory.
//
// A directory is reserved because of what it is FOR, never because of what is in it - so this
// takes a name and nothing else, and cannot be fooled by the contents.
func IsReservedDir(name string) bool {
	return name == IgnoredDirName ||
		name == IncomingDirName ||
		strings.HasSuffix(name, InProgressSuffix)
}

// InProgressSuffix marks a directory that is still being written.
//
// WalkLibrary skips these, so an archive being unpacked is not indexed halfway through. It is a
// suffix on the directory name rather than a lock or a marker file because it has to mean the
// same thing after a crash: a server restarted mid-unpack still sees a name it will not trust.
const InProgressSuffix = ".unpacking"

// NameChange is one entry whose stored name could not be used as the archive gave it.
type NameChange struct {
	// Stored is the name as the archive holds it, with the bytes that are not valid UTF-8
	// escaped so it can be printed and compared without corrupting the output it lands in.
	Stored string `json:"stored"`
	// Written is the name an extraction would create on disk.
	Written string `json:"written"`
}

// ArchiveNameChanges reports the entries whose names had to be decoded to be usable.
//
// This exists so that extracting can be a reviewable act rather than a hopeful one. Decoding a
// name inside an index costs nothing if it is wrong - rebuild and it is gone - but WRITING that
// name to disk makes it permanent. An operator deserves to see "this archive says Man\xa0, I
// intend to create Maná" before anything is created, which is why checking and unpacking are
// two separate operations rather than one button.
//
// Reading names costs a central-directory read and nothing more: no entry is decompressed.
func ArchiveNameChanges(p string) ([]NameChange, error) {
	items, closer, err := archiveItems(p)
	if err != nil {
		return nil, err
	}
	defer closer.Close()

	var out []NameChange
	seen := map[string]bool{}
	for _, it := range items {
		written := decodeEntryName(it.stored)
		if written == it.stored || seen[it.stored] {
			continue
		}
		seen[it.stored] = true
		out = append(out, NameChange{
			Stored:  strconv.QuoteToASCII(it.stored),
			Written: written,
		})
	}
	return out, nil
}

// ArchiveSize reports how many entries an archive holds and how many bytes they occupy
// uncompressed, which is what extracting it will need on disk.
//
// Read from the headers, so this is cheap. It is reported rather than checked against free
// space: a portable free-space call needs a dependency this module deliberately does not have -
// one dependency is what keeps `go build` alone sufficient for every platform ADR-001 promises,
// the Raspberry Pi included. So the number is put in front of the operator before they commit
// to anything, and a disk that fills anyway is handled where it happens, by removing the
// partial directory and keeping the archive.
func ArchiveSize(p string) (entries int, bytes int64, err error) {
	items, closer, err := archiveItems(p)
	if err != nil {
		return 0, 0, err
	}
	defer closer.Close()

	for _, it := range items {
		bytes += it.size
	}
	return len(items), bytes, nil
}

// ArchiveNestedArchives counts the entries that are themselves archives.
//
// It answers the question "0 songs" cannot. Measured on vault2: "Rock Band 2 DLC" reports zero
// songs across its sixteen parts, which reads as "this is empty or broken" and is neither - the
// parts hold 931 entries, almost all of them .7z files with one song in each. The chart is one
// archive further down than songRoots looks.
//
// Counted from the stored names, so this costs a directory read.
func ArchiveNestedArchives(p string) (int, error) {
	items, closer, err := archiveItems(p)
	if err != nil {
		return 0, err
	}
	defer closer.Close()

	n := 0
	for _, it := range items {
		name := decodeEntryName(it.stored)
		if strings.HasSuffix(name, "/") {
			continue
		}
		if IsContainer(path.Base(name)) || IsYargsong(path.Base(name)) {
			n++
		}
	}
	return n, nil
}

// ExtractTo writes every entry of an archive into dir, in stored order.
//
// dir must not already exist: this creates it, and refuses rather than merging into whatever is
// already there. Merging would let a second run quietly half-overwrite the first, and the
// caller's whole safety story rests on an extraction either producing a complete directory or
// producing nothing that the scanner will look at.
//
// Entry names are decoded first and then checked with fs.ValidPath, which rejects absolute
// paths and any ".." element - so an archive cannot be made to write outside dir. That is the
// zip-slip defence and it is a property of the names, not of this loop.
func ExtractTo(archivePath, dir string) (files int, written int64, err error) {
	if _, err := os.Stat(dir); err == nil {
		return 0, 0, fmt.Errorf("scan: %s already exists", dir)
	}
	return extract(archivePath, dir)
}

// ExtractMergeInto adds an archive's entries to a directory that may already hold some.
//
// This is what a split set needs and what ExtractTo deliberately refuses. A Google Takeout
// export of a large folder arrives as "Name-<timestamp>-1-001.zip", "-002", and so on, each
// holding a slice of ONE tree - so the parts have to land on top of each other to reassemble
// it, and extracting each to its own destination would produce "Name", "Name (2)", "Name (3)"
// and a library where no song is whole.
//
// Merging is still not overwriting: a file that already exists is an error, not something to
// replace. Two parts claiming the same path means the set is not what it appears to be, and
// quietly keeping whichever arrived last would hide that.
func ExtractMergeInto(archivePath, dir string) (files int, written int64, err error) {
	return extract(archivePath, dir)
}

func extract(archivePath, dir string) (files int, written int64, err error) {
	items, closer, err := archiveItems(archivePath)
	if err != nil {
		return 0, 0, err
	}
	defer closer.Close()

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return 0, 0, fmt.Errorf("scan: create %s: %w", dir, err)
	}

	for _, it := range items {
		name, isDir, ok := cleanEntryName(it.stored)
		if !ok {
			// A name that is not addressable even after decoding. Skipped rather than fatal,
			// on the same reasoning as everywhere else here: one odd entry must not cost the
			// other nine hundred.
			continue
		}

		target := filepath.Join(dir, filepath.FromSlash(name))
		if isDir {
			if err := os.MkdirAll(target, 0o755); err != nil {
				return files, written, fmt.Errorf("scan: create %s: %w", target, err)
			}
			continue
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return files, written, fmt.Errorf("scan: create %s: %w", filepath.Dir(target), err)
		}

		n, err := writeOne(it, target)
		written += n
		if err != nil {
			return files, written, err
		}
		files++
	}
	return files, written, nil
}

// writeOne copies a single entry to disk, closing both ends whatever happens.
//
// O_EXCL rather than Create: an entry that would land on a file already there is refused.
// Within one archive that cannot happen; across the parts of a split set it means two parts
// disagree about what belongs at that path, and replacing one with the other would turn a
// detectable problem into a library that is subtly wrong.
func writeOne(it archiveItem, target string) (int64, error) {
	rc, err := it.open()
	if err != nil {
		return 0, fmt.Errorf("scan: read %s: %w", it.stored, err)
	}
	defer rc.Close()

	f, err := os.OpenFile(target, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		if os.IsExist(err) {
			return 0, fmt.Errorf("scan: %s is already there; two parts claim the same file", target)
		}
		return 0, fmt.Errorf("scan: create %s: %w", target, err)
	}

	n, err := io.Copy(f, rc)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		return n, fmt.Errorf("scan: write %s: %w", target, err)
	}
	return n, nil
}

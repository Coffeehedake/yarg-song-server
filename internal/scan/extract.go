package scan

import (
	"archive/zip"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/bodgit/sevenzip"
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
	}
	return nil, nil, fmt.Errorf("scan: %q is not a container this server reads", filepath.Ext(p))
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
	items, closer, err := archiveItems(archivePath)
	if err != nil {
		return 0, 0, err
	}
	defer closer.Close()

	if _, err := os.Stat(dir); err == nil {
		return 0, 0, fmt.Errorf("scan: %s already exists", dir)
	}
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
func writeOne(it archiveItem, target string) (int64, error) {
	rc, err := it.open()
	if err != nil {
		return 0, fmt.Errorf("scan: read %s: %w", it.stored, err)
	}
	defer rc.Close()

	f, err := os.Create(target)
	if err != nil {
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

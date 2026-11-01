package scan

import (
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/text/encoding/charmap"
)

// An archive entry whose name is not valid UTF-8 is unreachable through io/fs, and that is a
// property of io/fs rather than of the archive.
//
// fs.ValidPath begins with utf8.ValidString, so EVERY path-addressed route refuses such a name:
// fs.Sub, fs.ReadDir, fs.ReadFile, and zip.Reader's own Open, which gates on fs.ValidPath before
// it looks anything up. The entry itself is perfectly readable - the per-entry API (zip.File.Open,
// sevenzip.File.Open) never consults a path at all. The door is open; only the corridor is shut.
//
// Measured on vault2's "Rock Band 3.zip": 948 entries, of which 6 - one song folder and its five
// files - carry the byte 0xA0 in "Man\xa0 - Oye Mi Amor", with the zip UTF-8 flag unset. Those 6
// entries cost the whole archive: ScanContainerAll could not address them, and the archive was
// reported as unreadable rather than as 82 good songs and one problem.
//
// archiveFS is the corridor: it presents the archive's entries under names that ARE valid UTF-8,
// built from the per-entry API, so everything downstream keeps using io/fs and knows nothing
// about any of this.

// decodeEntryName returns a name that io/fs will accept.
//
// Only names that are not already valid UTF-8 are touched. That restraint is deliberate: the zip
// header does carry a flag for this (FileHeader.NonUTF8, bit 11 of the general purpose flags),
// but a flag that is wrong - and plenty of archivers set it wrongly - would make us "fix" a name
// that was already correct. A name we cannot use is a fact; a flag is a claim.
//
// CP437 is what the ZIP specification says the bytes are when that flag is unset, and it is what
// the measured case needs: 0xA0 is "á" in CP437, giving "Maná - Oye Mi Amor", a real band. The
// same byte in CP1252 is a non-breaking space, which would give "Man  - Oye Mi Amor" - valid
// UTF-8, and wrong.
//
// Other archives may well carry some other legacy encoding, and this will decode those to the
// wrong characters. That is survivable and the alternative is not: song identity is
// SHA1(chart bytes) and never depends on a filename, so a song under a slightly wrong name is
// indexed, served and played correctly, while a song we refuse to address is simply gone.
func decodeEntryName(stored string) string {
	if utf8.ValidString(stored) {
		return stored
	}

	// CP437 maps all 256 byte values, so this cannot fail - but the result is checked rather
	// than assumed, because a decoder that silently produced something unusable would push the
	// failure one layer further away from its cause.
	if decoded, err := charmap.CodePage437.NewDecoder().String(stored); err == nil && utf8.ValidString(decoded) {
		return decoded
	}

	// Nothing decoded it. A replacement character at least makes the entry addressable, which
	// is the difference between one odd-looking song and a lost archive.
	return strings.ToValidUTF8(stored, "�")
}

// archiveItem is one entry as the reader gave it to us, before any naming decisions.
type archiveItem struct {
	stored   string
	size     int64
	modified time.Time
	open     func() (io.ReadCloser, error)
}

// archiveFS is a read-only fs.FS over a list of archive entries.
type archiveFS struct {
	files map[string]*archiveItem
	kids  map[string]map[string]bool // dir -> child base name -> isDir
}

var (
	_ fs.FS        = (*archiveFS)(nil)
	_ fs.ReadDirFS = (*archiveFS)(nil)
)

// newArchiveFS builds the view. Names are decoded, cleaned, and any entry that still cannot be
// addressed afterwards is dropped rather than allowed to poison a lookup.
func newArchiveFS(items []archiveItem) *archiveFS {
	a := &archiveFS{
		files: make(map[string]*archiveItem, len(items)),
		kids:  make(map[string]map[string]bool),
	}

	// The root always exists, even for an archive holding nothing. An empty archive must read
	// as an empty directory - which is how the scanner concludes "no chart here" - and not as a
	// missing one, which reads as a failure to open the archive at all.
	a.kids["."] = map[string]bool{}

	type pending struct {
		name string
		item archiveItem
	}
	var files []pending

	// Directories first, so that an entry which turns out to have children is never also
	// recorded as a file. Archives disagree about whether a directory gets its own entry and
	// whether that entry ends in a slash; "something lives under it" settles it either way.
	for _, it := range items {
		name, isDir, ok := cleanEntryName(it.stored)
		if !ok {
			continue
		}
		if isDir {
			a.markDir(name)
			continue
		}
		files = append(files, pending{name: name, item: it})
	}

	for _, p := range files {
		item := p.item
		a.files[p.name] = &item
		a.markFile(p.name)
	}

	// An entry can only be shown to be a directory by something else living under it, and that
	// something else may arrive later in the list - archives are under no obligation to order
	// entries helpfully. Deciding as we go therefore makes the result depend on entry order,
	// which is how "Song" became a zero-byte file in one archive and a directory in another.
	// Settle it once, at the end, when every entry has been seen.
	for name := range a.files {
		if a.isDir(name) {
			delete(a.files, name)
		}
	}
	return a
}

// cleanEntryName decodes and normalises one stored name.
//
// Backslash separators are deliberately NOT converted. An archive written by an old Windows tool
// stores "Song\notes.mid" as a single flat name, and the fs view shows a directory with nothing
// under it - a real defect, and a separate one from this. Fixing it here would change what
// songRoots sees at the same time as the encoding change, and the two want separate evidence.
func cleanEntryName(stored string) (name string, isDir bool, ok bool) {
	name = decodeEntryName(stored)
	isDir = strings.HasSuffix(name, "/")
	name = strings.TrimSuffix(name, "/")
	name = strings.TrimPrefix(name, "./")
	if name == "" || name == "." {
		return "", false, false
	}
	name = path.Clean(name)
	if !fs.ValidPath(name) {
		return "", false, false
	}
	return name, isDir, true
}

func (a *archiveFS) markDir(name string) {
	a.record(name, true)
}

func (a *archiveFS) markFile(name string) {
	a.record(name, false)
}

// record files an entry under its parent and makes sure every ancestor exists as a directory.
func (a *archiveFS) record(name string, isDir bool) {
	if isDir {
		if _, ok := a.kids[name]; !ok {
			a.kids[name] = map[string]bool{}
		}
	}
	for {
		dir, base := path.Split(name)
		dir = strings.TrimSuffix(dir, "/")
		if dir == "" {
			dir = "."
		}
		if _, ok := a.kids[dir]; !ok {
			a.kids[dir] = map[string]bool{}
		}
		if was, seen := a.kids[dir][base]; !seen || (!was && isDir) {
			a.kids[dir][base] = isDir
		}
		if dir == "." {
			return
		}
		name, isDir = dir, true
	}
}

func (a *archiveFS) isDir(name string) bool {
	_, ok := a.kids[name]
	return ok
}

func (a *archiveFS) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	if item, ok := a.files[name]; ok {
		rc, err := item.open()
		if err != nil {
			return nil, &fs.PathError{Op: "open", Path: name, Err: err}
		}
		return &archiveFile{
			ReadCloser: rc,
			info:       archiveInfo{name: path.Base(name), size: item.size, modified: item.modified},
		}, nil
	}
	if a.isDir(name) {
		entries, err := a.ReadDir(name)
		if err != nil {
			return nil, err
		}
		return &archiveDir{
			info:    archiveInfo{name: path.Base(name), dir: true},
			entries: entries,
		}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

func (a *archiveFS) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	kids, ok := a.kids[name]
	if !ok {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}

	out := make([]fs.DirEntry, 0, len(kids))
	for base, isDir := range kids {
		full := base
		if name != "." {
			full = name + "/" + base
		}
		info := archiveInfo{name: base, dir: isDir}
		if item, ok := a.files[full]; ok {
			info.size = item.size
			info.modified = item.modified
		}
		out = append(out, archiveDirEntry{info: info})
	}
	// fs.ReadDir promises directory order; callers here sort anyway, but a filesystem that
	// returned a different order on every call would make any failure below it unreproducible.
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out, nil
}

// archiveInfo is the fs.FileInfo for an entry.
type archiveInfo struct {
	name     string
	size     int64
	modified time.Time
	dir      bool
}

func (i archiveInfo) Name() string { return i.name }
func (i archiveInfo) Size() int64  { return i.size }
func (i archiveInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i archiveInfo) ModTime() time.Time { return i.modified }
func (i archiveInfo) IsDir() bool        { return i.dir }
func (i archiveInfo) Sys() any           { return nil }

type archiveDirEntry struct {
	info archiveInfo
}

func (e archiveDirEntry) Name() string               { return e.info.name }
func (e archiveDirEntry) IsDir() bool                { return e.info.dir }
func (e archiveDirEntry) Type() fs.FileMode          { return e.info.Mode().Type() }
func (e archiveDirEntry) Info() (fs.FileInfo, error) { return e.info, nil }

type archiveFile struct {
	io.ReadCloser
	info archiveInfo
}

func (f *archiveFile) Stat() (fs.FileInfo, error) { return f.info, nil }

type archiveDir struct {
	info    archiveInfo
	entries []fs.DirEntry
	offset  int
}

func (d *archiveDir) Stat() (fs.FileInfo, error) { return d.info, nil }
func (d *archiveDir) Close() error               { return nil }

func (d *archiveDir) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.info.name, Err: fs.ErrInvalid}
}

func (d *archiveDir) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		rest := d.entries[d.offset:]
		d.offset = len(d.entries)
		return rest, nil
	}
	if d.offset >= len(d.entries) {
		return nil, io.EOF
	}
	end := d.offset + n
	if end > len(d.entries) {
		end = len(d.entries)
	}
	out := d.entries[d.offset:end]
	d.offset = end
	return out, nil
}

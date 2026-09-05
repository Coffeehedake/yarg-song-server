package sng

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// maxSectionBytes bounds the metadata and file-index sections we will buffer.
// A .sng header is kilobytes in practice; this stops a corrupt or hostile
// length field from asking us to allocate the address space.
const maxSectionBytes = 64 << 20

// Archive is an opened .sng. It implements fs.FS, so the folder scanner and the
// .sng scanner can share exactly one code path: both are just a filesystem with
// a chart, some stems and some art in it.
//
// Contained files are read lazily through the underlying io.ReaderAt and
// unmasked on the way out, so opening a 200 MB .sng costs only its header.
type Archive struct {
	r    io.ReaderAt
	size int64

	Version uint32
	mask    Mask

	// Metadata is the song.ini key namespace, keys spelled exactly as they
	// appear (NOT lowercased - upstream lowercases filenames but not keys).
	Metadata map[string]string
	// MetaOrder is the key order as stored, for a faithful re-emit.
	MetaOrder []string

	listings map[string]Listing
	names    []string
}

// Open parses the header of a .sng. It reads only the header; contained file
// data is fetched on demand.
func Open(r io.ReaderAt, size int64) (*Archive, error) {
	if size < HeaderSize+16 {
		return nil, fmt.Errorf("sng: file too small (%d bytes)", size)
	}

	head := make([]byte, HeaderSize+16) // magic+version+mask, then metadata len+count
	if _, err := r.ReadAt(head, 0); err != nil {
		return nil, fmt.Errorf("sng: read header: %w", err)
	}
	if string(head[:MagicSize]) != string(Magic[:]) {
		return nil, errors.New("sng: bad magic, not an SNGPKG file")
	}

	a := &Archive{r: r, size: size, Metadata: map[string]string{}, listings: map[string]Listing{}}
	a.Version = binary.LittleEndian.Uint32(head[MagicSize:])
	var key [MaskSize]byte
	copy(key[:], head[MagicSize+VersionSize:HeaderSize])
	a.mask = NewMask(key)

	// Both section lengths are stored INCLUSIVE of the 8-byte count that
	// follows them; upstream subtracts sizeof(ulong) immediately after reading.
	metaLen := int64(binary.LittleEndian.Uint64(head[HeaderSize:])) - 8
	metaCount := binary.LittleEndian.Uint64(head[HeaderSize+8:])

	off := int64(HeaderSize + 16)
	metaBuf, err := a.section(off, metaLen, "metadata")
	if err != nil {
		return nil, err
	}
	if err := a.parseMetadata(metaBuf, metaCount); err != nil {
		return nil, err
	}
	off += metaLen

	idxHead := make([]byte, 16)
	if _, err := r.ReadAt(idxHead, off); err != nil {
		return nil, fmt.Errorf("sng: read file index header: %w", err)
	}
	idxLen := int64(binary.LittleEndian.Uint64(idxHead)) - 8
	idxCount := binary.LittleEndian.Uint64(idxHead[8:])
	off += 16

	idxBuf, err := a.section(off, idxLen, "file index")
	if err != nil {
		return nil, err
	}
	if err := a.parseListings(idxBuf, idxCount); err != nil {
		return nil, err
	}
	return a, nil
}

func (a *Archive) section(off, length int64, what string) ([]byte, error) {
	if length < 0 || length > maxSectionBytes || off+length > a.size {
		return nil, fmt.Errorf("sng: %s section length %d is out of range for a %d-byte file", what, length, a.size)
	}
	buf := make([]byte, length)
	if _, err := a.r.ReadAt(buf, off); err != nil {
		return nil, fmt.Errorf("sng: read %s section: %w", what, err)
	}
	return buf, nil
}

func (a *Archive) parseMetadata(buf []byte, count uint64) error {
	c := cursor{b: buf}
	for i := uint64(0); i < count; i++ {
		k, err := c.lenString()
		if err != nil {
			return fmt.Errorf("sng: metadata pair %d key: %w", i, err)
		}
		v, err := c.lenString()
		if err != nil {
			return fmt.Errorf("sng: metadata pair %d value: %w", i, err)
		}
		if _, seen := a.Metadata[k]; !seen {
			a.MetaOrder = append(a.MetaOrder, k)
		}
		// Last wins, matching song.ini semantics upstream.
		a.Metadata[k] = v
	}
	return nil
}

func (a *Archive) parseListings(buf []byte, count uint64) error {
	c := cursor{b: buf}
	for i := uint64(0); i < count; i++ {
		n, err := c.u8()
		if err != nil {
			return fmt.Errorf("sng: listing %d name length: %w", i, err)
		}
		raw, err := c.bytes(int64(n))
		if err != nil {
			return fmt.Errorf("sng: listing %d name: %w", i, err)
		}
		length, err := c.i64()
		if err != nil {
			return fmt.Errorf("sng: listing %d length: %w", i, err)
		}
		pos, err := c.i64()
		if err != nil {
			return fmt.Errorf("sng: listing %d position: %w", i, err)
		}
		// Position is an ABSOLUTE offset into the .sng, not relative to the
		// data section.
		if length < 0 || pos < 0 || pos+length > a.size {
			return fmt.Errorf("sng: listing %q points outside the file (pos %d, len %d, size %d)", raw, pos, length, a.size)
		}
		// YARG lowercases filenames on load, so lookups are case-insensitive
		// by normalisation. Do the same or a chart written with "Album.PNG"
		// will not be found.
		name := strings.ToLower(strings.ReplaceAll(string(raw), `\`, "/"))
		name = path.Clean(name)
		if name == "." || strings.HasPrefix(name, "../") || path.IsAbs(name) {
			return fmt.Errorf("sng: listing has an unsafe name %q", raw)
		}
		if _, seen := a.listings[name]; !seen {
			a.names = append(a.names, name)
		}
		a.listings[name] = Listing{Name: name, ContentsLen: length, ContentsIndex: pos}
	}
	sort.Strings(a.names)
	return nil
}

// Names returns every contained filename, lowercased and sorted.
func (a *Archive) Names() []string {
	out := make([]string, len(a.names))
	copy(out, a.names)
	return out
}

// Listing returns the entry for a name, matched case-insensitively.
func (a *Archive) Listing(name string) (Listing, bool) {
	l, ok := a.listings[strings.ToLower(name)]
	return l, ok
}

// Open implements fs.FS.
func (a *Archive) Open(name string) (fs.File, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrInvalid}
	}
	lower := strings.ToLower(name)
	if l, ok := a.listings[lower]; ok {
		return &file{a: a, l: l}, nil
	}
	if a.isDir(lower) {
		return &dirHandle{name: lower, entries: a.dirEntries(lower)}, nil
	}
	return nil, &fs.PathError{Op: "open", Path: name, Err: fs.ErrNotExist}
}

// ReadFile reads one contained file whole.
func (a *Archive) ReadFile(name string) ([]byte, error) {
	l, ok := a.listings[strings.ToLower(name)]
	if !ok {
		return nil, &fs.PathError{Op: "read", Path: name, Err: fs.ErrNotExist}
	}
	buf := make([]byte, l.ContentsLen)
	if _, err := a.r.ReadAt(buf, l.ContentsIndex); err != nil {
		return nil, fmt.Errorf("sng: read %q: %w", name, err)
	}
	a.mask.Apply(buf)
	return buf, nil
}

type file struct {
	a   *Archive
	l   Listing
	pos int64
}

func (f *file) Stat() (fs.FileInfo, error) { return fileInfo{f.l}, nil }
func (f *file) Close() error               { return nil }

func (f *file) Read(p []byte) (int, error) {
	if f.pos >= f.l.ContentsLen {
		return 0, io.EOF
	}
	if remaining := f.l.ContentsLen - f.pos; int64(len(p)) > remaining {
		p = p[:remaining]
	}
	n, err := f.a.r.ReadAt(p, f.l.ContentsIndex+f.pos)
	if n > 0 {
		// The mask index is the byte offset WITHIN this contained file,
		// restarting at 0 for every file - not the absolute offset in the
		// .sng. Upstream gets the same result by only ever decrypting whole
		// 1 MB buffers, which works because the buffer size is a multiple of
		// the 256-byte mask table. Tracking the real offset is equivalent and
		// survives arbitrary read sizes.
		f.a.mask.ApplyAt(p[:n], f.pos)
		f.pos += int64(n)
	}
	if err == io.EOF && f.pos < f.l.ContentsLen {
		return n, io.ErrUnexpectedEOF
	}
	if err != nil && err != io.EOF {
		return n, err
	}
	return n, nil
}

// Seek lets callers hash a chart or probe a header without reading the rest.
func (f *file) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = f.pos + offset
	case io.SeekEnd:
		abs = f.l.ContentsLen + offset
	default:
		return 0, fmt.Errorf("sng: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("sng: negative seek position")
	}
	f.pos = abs
	return abs, nil
}

type fileInfo struct{ l Listing }

func (i fileInfo) Name() string       { return path.Base(i.l.Name) }
func (i fileInfo) Size() int64        { return i.l.ContentsLen }
func (i fileInfo) Mode() fs.FileMode  { return 0o444 }
func (i fileInfo) ModTime() time.Time { return time.Time{} }
func (i fileInfo) IsDir() bool        { return false }
func (i fileInfo) Sys() any           { return nil }

// cursor is a bounds-checked reader over an in-memory section.
type cursor struct {
	b []byte
	i int64
}

func (c *cursor) bytes(n int64) ([]byte, error) {
	if n < 0 || c.i+n > int64(len(c.b)) {
		return nil, io.ErrUnexpectedEOF
	}
	out := c.b[c.i : c.i+n]
	c.i += n
	return out, nil
}

func (c *cursor) u8() (uint8, error) {
	b, err := c.bytes(1)
	if err != nil {
		return 0, err
	}
	return b[0], nil
}

func (c *cursor) i32() (int32, error) {
	b, err := c.bytes(4)
	if err != nil {
		return 0, err
	}
	return int32(binary.LittleEndian.Uint32(b)), nil
}

func (c *cursor) i64() (int64, error) {
	b, err := c.bytes(8)
	if err != nil {
		return 0, err
	}
	return int64(binary.LittleEndian.Uint64(b)), nil
}

func (c *cursor) lenString() (string, error) {
	n, err := c.i32()
	if err != nil {
		return "", err
	}
	b, err := c.bytes(int64(n))
	if err != nil {
		return "", err
	}
	return string(b), nil
}

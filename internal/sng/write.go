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
)

// Pair is one metadata entry. Keys are written EXACTLY as given: upstream
// lowercases filenames inside a .sng but not metadata keys, and a repack that
// silently renamed keys would change what the client reads.
type Pair struct {
	Key   string
	Value string
}

// Write packs the named files from fsys, plus the metadata pairs, into a .sng.
//
// The metadata section replaces song.ini - a .sng carries the song.ini key
// namespace in its header rather than as a contained file - so callers must
// pass song.ini's keys as pairs and leave song.ini out of names. Write refuses
// the mistake rather than producing an archive with both.
//
// Contained file data is streamed, so packing a 200 MB library costs a buffer,
// not the library.
//
// The caller supplies the header mask. Write used to draw it from crypto/rand
// itself, which made the output of an otherwise pure function unreproducible
// and quietly broke every promise built on "the same song is the same bytes".
// The mask is written to the header in plaintext, so there is nothing for
// randomness to protect; making it a parameter puts the choice where the caller
// can see it. See MaskKeyFor.
func Write(w io.Writer, key [MaskSize]byte, meta []Pair, fsys fs.FS, names []string) error {
	entries, err := prepare(fsys, names)
	if err != nil {
		return err
	}
	if err := validateMeta(meta); err != nil {
		return err
	}

	mask := NewMask(key)

	metaBytes := 0
	for _, p := range meta {
		metaBytes += 4 + len(p.Key) + 4 + len(p.Value)
	}
	idxBytes := 0
	for _, e := range entries {
		idxBytes += 1 + len(e.name) + 8 + 8
	}

	// magic+version+mask, then (len,count) for each of the two sections, the
	// sections themselves, and the data-length field.
	headerSize := int64(HeaderSize + 8 + 8 + metaBytes + 8 + 8 + idxBytes + 8)

	var dataLen int64
	pos := headerSize
	for i := range entries {
		entries[i].offset = pos
		pos += entries[i].size
		dataLen += entries[i].size
	}

	bw := &countingWriter{w: w}

	if _, err := bw.Write(Magic[:]); err != nil {
		return err
	}
	if err := writeLE(bw, Version1); err != nil {
		return err
	}
	if _, err := bw.Write(key[:]); err != nil {
		return err
	}

	// Both section lengths are stored INCLUSIVE of the 8-byte count that
	// follows them, which is why the reader subtracts 8 immediately.
	if err := writeLE(bw, int64(metaBytes+8)); err != nil {
		return err
	}
	if err := writeLE(bw, uint64(len(meta))); err != nil {
		return err
	}
	for _, p := range meta {
		if err := writeLE(bw, int32(len(p.Key))); err != nil {
			return err
		}
		if _, err := io.WriteString(bw, p.Key); err != nil {
			return err
		}
		if err := writeLE(bw, int32(len(p.Value))); err != nil {
			return err
		}
		if _, err := io.WriteString(bw, p.Value); err != nil {
			return err
		}
	}

	if err := writeLE(bw, int64(idxBytes+8)); err != nil {
		return err
	}
	if err := writeLE(bw, uint64(len(entries))); err != nil {
		return err
	}
	for _, e := range entries {
		if _, err := bw.Write([]byte{byte(len(e.name))}); err != nil {
			return err
		}
		if _, err := io.WriteString(bw, e.name); err != nil {
			return err
		}
		if err := writeLE(bw, e.size); err != nil {
			return err
		}
		if err := writeLE(bw, e.offset); err != nil {
			return err
		}
	}
	if err := writeLE(bw, uint64(dataLen)); err != nil {
		return err
	}

	// If this trips, the size arithmetic above disagrees with what was written
	// and every contentsIndex is wrong. Failing here is far better than
	// emitting an archive that reads as garbage.
	if bw.n != headerSize {
		return fmt.Errorf("sng: internal error: header is %d bytes, computed %d", bw.n, headerSize)
	}

	buf := make([]byte, 64*1024)
	for _, e := range entries {
		if err := copyMasked(bw, fsys, e, &mask, buf); err != nil {
			return err
		}
	}
	return nil
}

type entry struct {
	name   string // lowercased, forward slashes
	source string // the name as it appears in fsys
	size   int64
	offset int64
}

func prepare(fsys fs.FS, names []string) ([]entry, error) {
	seen := make(map[string]string, len(names))
	out := make([]entry, 0, len(names))
	for _, n := range names {
		// YARG lowercases filenames on load, so we emit them lowercased -
		// otherwise "Album.PNG" and "album.png" are the same file to the
		// client but two listings in our archive.
		lower := strings.ToLower(strings.ReplaceAll(n, `\`, "/"))
		if strings.EqualFold(path.Base(lower), "song.ini") {
			return nil, errors.New("sng: song.ini must be passed as metadata pairs, not as a file")
		}
		if len(lower) > MaxFilenameLen {
			return nil, fmt.Errorf("sng: filename %q is %d bytes; the format allows %d", n, len(lower), MaxFilenameLen)
		}
		if prev, dup := seen[lower]; dup {
			return nil, fmt.Errorf("sng: %q and %q collide once lowercased", prev, n)
		}
		seen[lower] = n

		st, err := fs.Stat(fsys, n)
		if err != nil {
			return nil, fmt.Errorf("sng: stat %q: %w", n, err)
		}
		if st.IsDir() {
			continue
		}
		out = append(out, entry{name: lower, source: n, size: st.Size()})
	}
	// Deterministic order. With a caller-supplied mask this is the last piece
	// that makes Write a pure function of its inputs: same folder, same key,
	// same bytes.
	sort.Slice(out, func(i, j int) bool { return out[i].name < out[j].name })
	return out, nil
}

func validateMeta(meta []Pair) error {
	for _, p := range meta {
		if p.Key == "" {
			return errors.New("sng: empty metadata key")
		}
		// The format's own restrictions: a key may not contain '=', and neither
		// key nor value may contain ';' or a newline.
		if strings.ContainsAny(p.Key, "=;\r\n") {
			return fmt.Errorf("sng: metadata key %q contains a reserved character", p.Key)
		}
		if strings.ContainsAny(p.Value, ";\r\n") {
			return fmt.Errorf("sng: value of %q contains a reserved character", p.Key)
		}
	}
	return nil
}

func copyMasked(w io.Writer, fsys fs.FS, e entry, mask *Mask, buf []byte) error {
	f, err := fsys.Open(e.source)
	if err != nil {
		return fmt.Errorf("sng: open %q: %w", e.source, err)
	}
	defer f.Close()

	var written int64
	for written < e.size {
		want := int64(len(buf))
		if remaining := e.size - written; remaining < want {
			want = remaining
		}
		n, readErr := io.ReadFull(f, buf[:want])
		if n > 0 {
			chunk := buf[:n]
			// The mask index is the offset WITHIN this file, so a file split
			// across chunks must carry its running offset.
			mask.ApplyAt(chunk, written)
			if _, err := w.Write(chunk); err != nil {
				return err
			}
			written += int64(n)
		}
		if readErr != nil {
			// The size came from Stat; a short read means the file changed
			// under us, and the listing we already wrote is now a lie.
			return fmt.Errorf("sng: %q changed while packing (read %d of %d bytes): %w", e.source, written, e.size, readErr)
		}
	}
	return nil
}

func writeLE(w io.Writer, v any) error { return binary.Write(w, binary.LittleEndian, v) }

type countingWriter struct {
	w io.Writer
	n int64
}

func (c *countingWriter) Write(p []byte) (int, error) {
	n, err := c.w.Write(p)
	c.n += int64(n)
	return n, err
}

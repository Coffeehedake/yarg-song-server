// Package yargsong reads the .yargsong container YARG setlists ship in.
//
// A .yargsong is an ordinary song file behind one layer of obfuscation, and the obfuscation is
// published: this is a port of YARG.Core/IO/YARGSongFileStream.cs, whose own comments call it
// the "Crawford multi-value cipher" and describe the decode as "a super dumbed down version of
// the algorithm". Nothing here was reverse engineered from a binary; the C# sits in the fork
// this project already vendors, and the arithmetic below is deliberately written to be read
// line by line against it.
//
// WHY: YARG.Core scans `.sng` and `.yargsong` alike, so files extracted into a library are
// playable by the game with no help from us. The server is the one that cannot read them, and
// a song the server cannot read is a song it cannot catalogue, hash, or hand to a client. This
// package is what lets 481 installed songs become 481 songs clients can actually sync.
//
// The format: a 24-byte header - the 8-byte signature "YARGSONG", one seed byte, and a 15-byte
// set - followed by the payload, each byte of which is transformed by position.
package yargsong

import (
	"errors"
	"fmt"
	"io"
	"os"
)

const (
	// HeaderSize is signature + seed + set.
	HeaderSize = 24
	setLength  = 15
	sigLength  = 8
)

// Magic is the 8-byte signature at offset 0.
var Magic = [sigLength]byte{'Y', 'A', 'R', 'G', 'S', 'O', 'N', 'G'}

// ErrNotYargsong means the file does not carry the signature. It is a plain sentinel because
// "this is not one of ours" is an ordinary answer during a scan, not a failure.
var ErrNotYargsong = errors.New("yargsong: missing YARGSONG signature")

// keys are the three values the decode uses. The C# derives a fourth and never reads it; it is
// kept here, unused, so the port can be compared against the original without a reader
// wondering whether a line was dropped.
type keys struct {
	a, b, c int32
	_unused int32
}

// deriveKeys is TryLoad()'s "Crawford special number" block, transliterated.
//
// Every operation is int32 and wraps, matching C#'s `unchecked`. Go's conversions truncate and
// its signed arithmetic wraps, so the translation is exact rather than merely equivalent.
func deriveKeys(seed byte, set []byte) (keys, error) {
	if len(set) != setLength {
		return keys{}, fmt.Errorf("yargsong: set is %d bytes, want %d", len(set), setLength)
	}

	z := int32(seed)
	z += 1679               // A large-ish prime
	w := (z ^ 4) - z*2      // Exponent W value
	n := 25*w - 5           // Aleph value
	x := (w + (z << 1)) ^ 4 // Use some bit shifting to our advantage

	// Approximate infinite summation (we're using bytes so it works)
	l := (n + 73) * (n + 23)
	l -= n*n + 96*n
	x = -l + n + x - w*(5*5)

	x = (x + 5) % 255 // Convert to byte again

	// Go's % keeps the sign of the dividend, exactly as C#'s does, so a negative x produces a
	// negative index below. The C# would throw IndexOutOfRangeException there; we refuse with
	// a reason instead, because a scanner must not panic on a file someone put in a folder.
	if x < 0 {
		return keys{}, fmt.Errorf("yargsong: derived step %d is negative; file header is not one we understand", x)
	}

	var v [4]int32
	for i, j := int32(0), int32(0); i < 24; i, j = i+1, j+x {
		v[0] += int32(byte(int32(set[j%setLength]) + i*3298 + 88903))
		v[1] -= int32(set[(j+7001)%setLength])
		v[2] += int32(set[j%setLength])
		v[3] += j << 2
	}

	return keys{a: v[0], b: v[1], c: v[2], _unused: v[3]}, nil
}

// decode transforms buf in place. off is the position of buf[0] within the PAYLOAD, counting
// from 0 after the header - not within the file.
//
// The expression is the C# one: subtract the position term and b, xor a, then truncate. The
// xor happens before truncation, which is why the arithmetic is done in int32 and only the
// final store narrows to a byte.
func decode(buf []byte, off int64, k keys) {
	for i := range buf {
		pos := int32(off) + int32(i)
		buf[i] = byte((int32(buf[i]) - pos*k.c - k.b) ^ k.a)
	}
}

// Reader is a decoded view of a .yargsong: read and seek it and you get the file that is
// inside, as though the obfuscation were not there.
type Reader struct {
	f    *os.File
	k    keys
	pos  int64 // payload-relative
	size int64
}

// Open opens a .yargsong. The returned Reader must be closed.
func Open(path string) (*Reader, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}

	head := make([]byte, HeaderSize)
	if _, err := io.ReadFull(f, head); err != nil {
		f.Close()
		if errors.Is(err, io.ErrUnexpectedEOF) || errors.Is(err, io.EOF) {
			return nil, ErrNotYargsong
		}
		return nil, err
	}
	if string(head[:sigLength]) != string(Magic[:]) {
		f.Close()
		return nil, ErrNotYargsong
	}

	k, err := deriveKeys(head[sigLength], head[sigLength+1:])
	if err != nil {
		f.Close()
		return nil, err
	}

	fi, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, err
	}

	return &Reader{f: f, k: k, size: fi.Size() - HeaderSize}, nil
}

// Size is the length of the decoded payload.
func (r *Reader) Size() int64 { return r.size }

func (r *Reader) Read(p []byte) (int, error) {
	n, err := r.f.Read(p)
	if n > 0 {
		decode(p[:n], r.pos, r.k)
		r.pos += int64(n)
	}
	return n, err
}

func (r *Reader) ReadAt(p []byte, off int64) (int, error) {
	n, err := r.f.ReadAt(p, off+HeaderSize)
	if n > 0 {
		decode(p[:n], off, r.k)
	}
	return n, err
}

func (r *Reader) Seek(offset int64, whence int) (int64, error) {
	var abs int64
	switch whence {
	case io.SeekStart:
		abs = offset
	case io.SeekCurrent:
		abs = r.pos + offset
	case io.SeekEnd:
		abs = r.size + offset
	default:
		return 0, fmt.Errorf("yargsong: invalid whence %d", whence)
	}
	if abs < 0 {
		return 0, errors.New("yargsong: negative position")
	}
	if _, err := r.f.Seek(abs+HeaderSize, io.SeekStart); err != nil {
		return 0, err
	}
	r.pos = abs
	return abs, nil
}

func (r *Reader) Close() error { return r.f.Close() }

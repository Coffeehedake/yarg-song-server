package sng

import (
	"bytes"
	"crypto/rand"
	"encoding/binary"
	"io"
	"io/fs"
	"testing"
	"testing/fstest"
)

// buildSNG is a TEST FIXTURE ONLY, not the real writer.
//
// It exists so the reader can be exercised without shipping a .sng in the repo.
// Be clear about what it does and does not prove: a reader that round-trips
// with an encoder written from the same understanding of the format proves only
// that the two agree with each other. It catches bounds bugs, offset bugs and
// masking bugs, which is worth having. It does NOT prove we can read a .sng
// produced by SngCli. That requires a real file, and is a gate on the writer.
func buildSNG(t *testing.T, meta [][2]string, files [][2]string) ([]byte, [MaskSize]byte) {
	t.Helper()

	var key [MaskSize]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	mask := NewMask(key)

	metaBytes := 0
	for _, kv := range meta {
		metaBytes += 4 + len(kv[0]) + 4 + len(kv[1])
	}
	idxBytes := 0
	for _, f := range files {
		idxBytes += 1 + len(f[0]) + 8 + 8
	}

	headerSize := HeaderSize + 8 + 8 + metaBytes + 8 + 8 + idxBytes + 8

	var b bytes.Buffer
	b.Write(Magic[:])
	_ = binary.Write(&b, binary.LittleEndian, Version1)
	b.Write(key[:])

	// Section lengths are stored INCLUSIVE of the count field that follows.
	_ = binary.Write(&b, binary.LittleEndian, int64(metaBytes+8))
	_ = binary.Write(&b, binary.LittleEndian, uint64(len(meta)))
	for _, kv := range meta {
		_ = binary.Write(&b, binary.LittleEndian, int32(len(kv[0])))
		b.WriteString(kv[0])
		_ = binary.Write(&b, binary.LittleEndian, int32(len(kv[1])))
		b.WriteString(kv[1])
	}

	_ = binary.Write(&b, binary.LittleEndian, int64(idxBytes+8))
	_ = binary.Write(&b, binary.LittleEndian, uint64(len(files)))
	pos := int64(headerSize)
	dataLen := 0
	for _, f := range files {
		b.WriteByte(byte(len(f[0])))
		b.WriteString(f[0])
		_ = binary.Write(&b, binary.LittleEndian, int64(len(f[1])))
		_ = binary.Write(&b, binary.LittleEndian, pos) // absolute
		pos += int64(len(f[1]))
		dataLen += len(f[1])
	}
	_ = binary.Write(&b, binary.LittleEndian, uint64(dataLen))

	if b.Len() != headerSize {
		t.Fatalf("fixture header size mismatch: wrote %d, computed %d", b.Len(), headerSize)
	}
	for _, f := range files {
		masked := []byte(f[1])
		buf := bytes.Clone(masked)
		mask.Apply(buf) // masking is symmetric
		b.Write(buf)
	}
	return b.Bytes(), key
}

func TestOpenAndRead(t *testing.T) {
	meta := [][2]string{{"name", "Sample Song"}, {"artist", "Someone"}, {"year", "1994"}}
	// A file deliberately longer than the 256-byte mask table, so a per-file
	// offset bug shows up rather than hiding in the first block.
	long := bytes.Repeat([]byte("0123456789abcdef"), 200) // 3200 bytes
	files := [][2]string{
		{"notes.chart", "[Song]\n{\n}\n"},
		{"guitar.ogg", string(long)},
		{"album.png", "\x89PNG\r\n\x1a\n"},
	}
	raw, _ := buildSNG(t, meta, files)

	a, err := Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	if a.Version != Version1 {
		t.Fatalf("version = %d", a.Version)
	}
	if a.Metadata["name"] != "Sample Song" || a.Metadata["artist"] != "Someone" {
		t.Fatalf("metadata = %v", a.Metadata)
	}
	if got := len(a.Names()); got != 3 {
		t.Fatalf("names = %v", a.Names())
	}
	for _, f := range files {
		got, err := a.ReadFile(f[0])
		if err != nil {
			t.Fatalf("ReadFile %s: %v", f[0], err)
		}
		if !bytes.Equal(got, []byte(f[1])) {
			t.Fatalf("%s: content mismatch (%d bytes vs %d)", f[0], len(got), len(f[1]))
		}
	}
}

// The whole point of implementing fs.FS is that the folder scanner and the .sng
// scanner share one code path. If this ever stops holding, they diverge.
func TestSatisfiesFSInterface(t *testing.T) {
	raw, _ := buildSNG(t, nil, [][2]string{{"notes.chart", "hello"}, {"song.ini", "[song]\n"}})
	a, err := Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	var fsys fs.FS = a
	if err := fstest.TestFS(fsys, "notes.chart", "song.ini"); err != nil {
		t.Fatalf("TestFS: %v", err)
	}
}

// Chunked reads must agree with a whole-file read. A mask indexed by the wrong
// origin passes a single-shot read and fails here.
func TestChunkedReadMatchesWhole(t *testing.T) {
	payload := make([]byte, 5000)
	if _, err := rand.Read(payload); err != nil {
		t.Fatal(err)
	}
	raw, _ := buildSNG(t, nil, [][2]string{{"a.bin", string(payload)}, {"b.bin", string(payload)}})
	a, err := Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	// b.bin starts at a different absolute offset than a.bin. If the mask were
	// keyed on the absolute .sng offset, one of these two would come back wrong.
	for _, name := range []string{"a.bin", "b.bin"} {
		f, err := a.Open(name)
		if err != nil {
			t.Fatal(err)
		}
		var got bytes.Buffer
		buf := make([]byte, 7) // awkward size, not a divisor of 256
		for {
			n, err := f.Read(buf)
			got.Write(buf[:n])
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatalf("%s read: %v", name, err)
			}
		}
		_ = f.Close()
		if !bytes.Equal(got.Bytes(), payload) {
			t.Fatalf("%s: chunked read differs from the original", name)
		}
	}
}

func TestNameLookupIsCaseInsensitive(t *testing.T) {
	raw, _ := buildSNG(t, nil, [][2]string{{"Album.PNG", "x"}})
	a, err := Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := a.Listing("album.png"); !ok {
		t.Fatal("lowercased lookup failed")
	}
	if _, ok := a.Listing("ALBUM.PNG"); !ok {
		t.Fatal("uppercased lookup failed")
	}
}

func TestRejectsGarbageRatherThanPanicking(t *testing.T) {
	good, _ := buildSNG(t, [][2]string{{"name", "x"}}, [][2]string{{"notes.chart", "y"}})

	cases := map[string][]byte{
		"empty":       {},
		"short":       good[:10],
		"bad magic":   append([]byte("NOTSNG"), good[6:]...),
		"truncated":   good[:len(good)/2],
		"absurd meta": func() []byte { b := bytes.Clone(good); binary.LittleEndian.PutUint64(b[HeaderSize:], 1<<62); return b }(),
	}
	for name, raw := range cases {
		t.Run(name, func(t *testing.T) {
			defer func() {
				if r := recover(); r != nil {
					t.Fatalf("panicked on %s input: %v", name, r)
				}
			}()
			if _, err := Open(bytes.NewReader(raw), int64(len(raw))); err == nil {
				t.Fatalf("accepted %s input", name)
			}
		})
	}
}

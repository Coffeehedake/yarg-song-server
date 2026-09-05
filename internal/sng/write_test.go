package sng

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
	"testing/fstest"
)

func packFS(t *testing.T, fsys fstest.MapFS, meta []Pair) *Archive {
	t.Helper()
	names := make([]string, 0, len(fsys))
	for n := range fsys {
		names = append(names, n)
	}
	var buf bytes.Buffer
	if err := Write(&buf, meta, fsys, names); err != nil {
		t.Fatalf("Write: %v", err)
	}
	a, err := Open(bytes.NewReader(buf.Bytes()), int64(buf.Len()))
	if err != nil {
		t.Fatalf("Open our own output: %v", err)
	}
	return a
}

func TestWriteRoundTrip(t *testing.T) {
	big := make([]byte, 5000) // spans the 256-byte mask table many times over
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	fsys := fstest.MapFS{
		"notes.chart": {Data: []byte("[Song]\n{\n}\n")},
		"guitar.ogg":  {Data: big},
		"album.png":   {Data: []byte("\x89PNG\r\n\x1a\n")},
	}
	meta := []Pair{{"name", "Written"}, {"artist", "Us"}, {"year", ", 1994?"}}

	a := packFS(t, fsys, meta)

	for _, p := range meta {
		if got := a.Metadata[p.Key]; got != p.Value {
			t.Errorf("metadata[%q] = %q, want %q", p.Key, got, p.Value)
		}
	}
	for name, file := range fsys {
		got, err := a.ReadFile(name)
		if err != nil {
			t.Fatalf("read %s: %v", name, err)
		}
		if !bytes.Equal(got, file.Data) {
			t.Fatalf("%s: %d bytes back, %d in", name, len(got), len(file.Data))
		}
	}
}

// Every write must produce a different mask, or the obfuscation is a constant
// and two archives of the same content are byte-identical.
func TestWriteUsesAFreshMask(t *testing.T) {
	fsys := fstest.MapFS{"notes.chart": {Data: []byte("x")}}
	var a, b bytes.Buffer
	if err := Write(&a, nil, fsys, []string{"notes.chart"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&b, nil, fsys, []string{"notes.chart"}); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("two writes produced identical bytes; the mask is not random")
	}
	if a.Len() != b.Len() {
		t.Fatalf("same content produced different sizes: %d vs %d", a.Len(), b.Len())
	}
}

// A .sng carries song.ini's keys in its header, not as a file. Writing both
// would produce an archive where the two could disagree, so it is refused.
func TestWriteRefusesSongIniAsAFile(t *testing.T) {
	fsys := fstest.MapFS{
		"notes.chart": {Data: []byte("x")},
		"song.ini":    {Data: []byte("[Song]\n")},
	}
	err := Write(&bytes.Buffer{}, nil, fsys, []string{"notes.chart", "song.ini"})
	if err == nil {
		t.Fatal("accepted song.ini as a contained file")
	}
	if !strings.Contains(err.Error(), "metadata pairs") {
		t.Fatalf("unhelpful error: %v", err)
	}
}

func TestWriteLowercasesNamesAndCatchesCollisions(t *testing.T) {
	a := packFS(t, fstest.MapFS{"Album.PNG": {Data: []byte("x")}}, nil)
	if _, ok := a.Listing("album.png"); !ok {
		t.Fatalf("name was not lowercased on write: %v", a.Names())
	}

	fsys := fstest.MapFS{"Album.PNG": {Data: []byte("x")}, "album.png": {Data: []byte("y")}}
	err := Write(&bytes.Buffer{}, nil, fsys, []string{"Album.PNG", "album.png"})
	if err == nil || !strings.Contains(err.Error(), "collide") {
		t.Fatalf("collision after lowercasing was not caught: %v", err)
	}
}

func TestWriteRejectsReservedCharactersInMetadata(t *testing.T) {
	fsys := fstest.MapFS{"notes.chart": {Data: []byte("x")}}
	for _, bad := range []Pair{
		{"na=me", "v"},
		{"name", "a;b"},
		{"name", "a\nb"},
		{"", "v"},
	} {
		if err := Write(&bytes.Buffer{}, []Pair{bad}, fsys, []string{"notes.chart"}); err == nil {
			t.Errorf("accepted metadata pair %+v", bad)
		}
	}
}

// The listing declares each file's length and absolute offset before any data
// is written, so an oversized name would corrupt every offset after it.
func TestWriteRejectsOverlongFilename(t *testing.T) {
	long := strings.Repeat("a", MaxFilenameLen+1) + ".ogg"
	fsys := fstest.MapFS{long: {Data: []byte("x")}}
	if err := Write(&bytes.Buffer{}, nil, fsys, []string{long}); err == nil {
		t.Fatal("accepted a filename longer than the format allows")
	}
}

// Packing must not change the chart bytes, because the song's identity is a
// hash of exactly those bytes. This is the invariant the whole server rests on.
func TestPackingPreservesChartBytesExactly(t *testing.T) {
	chart := []byte("[Song]\n{\n  Name = \"Identity\"\n}\n[ExpertSingle]\n{\n  768 = N 0 0\n}\n")
	a := packFS(t, fstest.MapFS{
		"notes.chart": {Data: chart},
		"song.ogg":    {Data: bytes.Repeat([]byte("a"), 1000)},
	}, []Pair{{"name", "Identity"}})

	got, err := a.ReadFile("notes.chart")
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, chart) {
		t.Fatal("packing altered the chart bytes; every song identity would move")
	}
}

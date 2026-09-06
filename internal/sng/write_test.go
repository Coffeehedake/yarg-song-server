package sng

import (
	"bytes"
	"crypto/rand"
	"strings"
	"testing"
	"testing/fstest"
)

// testKey stands in for the package hash a real caller would seed with. Tests
// that are not about masking should not have to care which key they used, only
// that they used the same one every run.
var testKey = MaskKeyFor("write_test")

func packFS(t *testing.T, fsys fstest.MapFS, meta []Pair) *Archive {
	t.Helper()
	names := make([]string, 0, len(fsys))
	for n := range fsys {
		names = append(names, n)
	}
	var buf bytes.Buffer
	if err := Write(&buf, testKey, meta, fsys, names); err != nil {
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

// This test used to be TestWriteUsesAFreshMask, and it asserted the opposite:
// that two writes of the same content must NEVER be byte-identical, on the
// belief that a repeated mask weakened the obfuscation. It does not. The mask
// sits in the header in plaintext, so it protects nothing, and treating
// byte-identical output as a bug is what let a random mask survive review and
// reach a deployment - where it broke the ETag, the Range resume, and the claim
// that two clients syncing one server get the same files. A test can lock in a
// defect just as firmly as it can catch one.
func TestWriteIsAPureFunctionOfItsInputs(t *testing.T) {
	fsys := fstest.MapFS{"notes.chart": {Data: []byte("x")}}
	key := MaskKeyFor("some package hash")

	var a, b bytes.Buffer
	if err := Write(&a, key, nil, fsys, []string{"notes.chart"}); err != nil {
		t.Fatal(err)
	}
	if err := Write(&b, key, nil, fsys, []string{"notes.chart"}); err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(a.Bytes(), b.Bytes()) {
		t.Fatal("same content and same key produced different bytes; Write is not deterministic")
	}

	var c bytes.Buffer
	if err := Write(&c, MaskKeyFor("a different package hash"), nil, fsys, []string{"notes.chart"}); err != nil {
		t.Fatal(err)
	}
	if bytes.Equal(a.Bytes(), c.Bytes()) {
		t.Fatal("a different key produced identical bytes; the key is being ignored")
	}
	if a.Len() != c.Len() {
		t.Fatalf("the key changed the archive size: %d vs %d", a.Len(), c.Len())
	}
}

// The seed must reach the header unchanged in effect: the mask stored in the
// file has to be the one MaskKeyFor derived, or "derived from the package hash"
// is a claim about code that does something else.
func TestTheDerivedKeyIsTheMaskInTheHeader(t *testing.T) {
	fsys := fstest.MapFS{"notes.chart": {Data: []byte("x")}}
	key := MaskKeyFor("seed")
	var buf bytes.Buffer
	if err := Write(&buf, key, nil, fsys, []string{"notes.chart"}); err != nil {
		t.Fatal(err)
	}
	got := buf.Bytes()[HeaderSize-MaskSize : HeaderSize]
	if !bytes.Equal(got, key[:]) {
		t.Fatalf("header mask %x, derived key %x", got, key)
	}
}

// Deriving must not collapse distinct seeds onto one mask, or two packages
// would pack to archives that differ only where their content differs - which
// is fine for correctness but means the derivation is not doing its job.
func TestMaskKeyForSeparatesSeeds(t *testing.T) {
	seen := map[[MaskSize]byte]string{}
	for _, seed := range []string{"", "a", "b", "aa", "package-hash-1", "package-hash-2"} {
		k := MaskKeyFor(seed)
		if prev, dup := seen[k]; dup {
			t.Fatalf("seeds %q and %q derive the same mask", prev, seed)
		}
		seen[k] = seed
	}
	if MaskKeyFor("x") != MaskKeyFor("x") {
		t.Fatal("MaskKeyFor is not a function")
	}
}

// A .sng carries song.ini's keys in its header, not as a file. Writing both
// would produce an archive where the two could disagree, so it is refused.
func TestWriteRefusesSongIniAsAFile(t *testing.T) {
	fsys := fstest.MapFS{
		"notes.chart": {Data: []byte("x")},
		"song.ini":    {Data: []byte("[Song]\n")},
	}
	err := Write(&bytes.Buffer{}, testKey, nil, fsys, []string{"notes.chart", "song.ini"})
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
	err := Write(&bytes.Buffer{}, testKey, nil, fsys, []string{"Album.PNG", "album.png"})
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
		if err := Write(&bytes.Buffer{}, testKey, []Pair{bad}, fsys, []string{"notes.chart"}); err == nil {
			t.Errorf("accepted metadata pair %+v", bad)
		}
	}
}

// The listing declares each file's length and absolute offset before any data
// is written, so an oversized name would corrupt every offset after it.
func TestWriteRejectsOverlongFilename(t *testing.T) {
	long := strings.Repeat("a", MaxFilenameLen+1) + ".ogg"
	fsys := fstest.MapFS{long: {Data: []byte("x")}}
	if err := Write(&bytes.Buffer{}, testKey, nil, fsys, []string{long}); err == nil {
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

package scan

import (
	"bytes"
	"encoding/binary"
	"testing"
	"testing/fstest"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

const sampleINI = `[Song]
name = Sample Song
artist = Someone
album = An Album
genre = Rock
year = 1994
charter = A Charter
song_length = 210000
preview = 30000 45000
diff_guitar = 4
diff_drums = 2
diff_vocals = -1
five_lane_drums = 1
hopo_frequency = 170
`

func sampleFolder() fstest.MapFS {
	return fstest.MapFS{
		"song.ini":    {Data: []byte(sampleINI)},
		"notes.chart": {Data: []byte("[Song]\n{\n}\n")},
		"guitar.ogg":  {Data: bytes.Repeat([]byte("g"), 400)},
		"drums_1.ogg": {Data: bytes.Repeat([]byte("d"), 300)},
		"song.ogg":    {Data: bytes.Repeat([]byte("s"), 500)},
		"album.png":   {Data: []byte("\x89PNG\r\n\x1a\nfake")},
		"readme.txt":  {Data: []byte("not part of the song")},
	}
}

func TestScanDir(t *testing.T) {
	s, err := ScanDir(sampleFolder())
	if err != nil {
		t.Fatal(err)
	}
	if s.Name != "Sample Song" || s.Artist != "Someone" || s.Charter != "A Charter" {
		t.Fatalf("metadata: %+v", s)
	}
	if s.ChartFormat != catalog.FormatChart || s.ChartFile != "notes.chart" {
		t.Fatalf("chart = %s %s", s.ChartFormat, s.ChartFile)
	}
	if s.YearAsNumber != 1994 || s.Year != "1994" {
		t.Fatalf("year = %q %d", s.Year, s.YearAsNumber)
	}
	if s.SongLengthMS != 210000 || s.PreviewStartMS != 30000 || s.PreviewEndMS != 45000 {
		t.Fatalf("timing = %d %d %d", s.SongLengthMS, s.PreviewStartMS, s.PreviewEndMS)
	}
	if len(s.ChartHash) != 40 || len(s.PackageHash) != 64 {
		t.Fatalf("hashes: chart=%q package=%q", s.ChartHash, s.PackageHash)
	}
	if s.Assets.AlbumArt == nil || s.Assets.AlbumArt.Name != "album.png" {
		t.Fatalf("album art = %+v", s.Assets.AlbumArt)
	}
	if len(s.Assets.Stems) != 3 {
		t.Fatalf("stems = %+v", s.Assets.Stems)
	}
	// An unrecognised file must be carried, not silently dropped.
	if len(s.Assets.Other) != 1 || s.Assets.Other[0].Name != "readme.txt" {
		t.Fatalf("other = %+v", s.Assets.Other)
	}
	// Parse-tuning keys we do not model must survive for a faithful repack.
	if s.RawMetadata["hopo_frequency"] != "170" || s.RawMetadata["five_lane_drums"] != "1" {
		t.Fatalf("raw metadata lost parse-tuning keys: %v", s.RawMetadata)
	}
}

// -1 in the chart means "unknown", and so does a key that is absent. Neither
// may come back as 0, which would claim the charter rated it trivially easy.
func TestUnknownIntensityIsNotZero(t *testing.T) {
	s, err := ScanDir(sampleFolder())
	if err != nil {
		t.Fatal(err)
	}
	if s.Parts.FiveFretGuitar.Intensity != 4 || s.Parts.FourLaneDrums.Intensity != 2 {
		t.Fatalf("stated intensities wrong: %+v", s.Parts)
	}
	if s.Parts.LeadVocals.Intensity != catalog.UnknownIntensity {
		t.Fatalf("explicit -1 became %d", s.Parts.LeadVocals.Intensity)
	}
	if s.Parts.ProKeys.Intensity != catalog.UnknownIntensity {
		t.Fatalf("absent key became %d, not unknown", s.Parts.ProKeys.Intensity)
	}
	if s.PartsDerived {
		t.Fatal("PartsDerived must stay false until MIDI preparsing exists")
	}
}

// notes.mid beats notes.chart. Hashing the wrong one yields an identity the
// client will never agree with, and nothing else in the scan would look wrong.
func TestChartPriorityDecidesIdentity(t *testing.T) {
	both := sampleFolder()
	both["notes.mid"] = &fstest.MapFile{Data: []byte("MThd-not-really")}
	s, err := ScanDir(both)
	if err != nil {
		t.Fatal(err)
	}
	if s.ChartFile != "notes.mid" || s.ChartFormat != catalog.FormatMid {
		t.Fatalf("picked %s (%s), want notes.mid", s.ChartFile, s.ChartFormat)
	}
	only := sampleFolder()
	other, err := ScanDir(only)
	if err != nil {
		t.Fatal(err)
	}
	if s.ChartHash == other.ChartHash {
		t.Fatal("hash did not change when the chosen chart changed")
	}
}

func TestNoChartIsNotASong(t *testing.T) {
	_, err := ScanDir(fstest.MapFS{"song.ini": {Data: []byte(sampleINI)}})
	if err != ErrNoChart {
		t.Fatalf("err = %v, want ErrNoChart", err)
	}
}

func TestCoverKeyOverridesAlbumArt(t *testing.T) {
	f := sampleFolder()
	f["song.ini"] = &fstest.MapFile{Data: []byte(sampleINI + "cover = custom.jpg\n")}
	f["custom.jpg"] = &fstest.MapFile{Data: []byte("jpegish")}
	s, err := ScanDir(f)
	if err != nil {
		t.Fatal(err)
	}
	if s.Assets.AlbumArt == nil || s.Assets.AlbumArt.Name != "custom.jpg" {
		t.Fatalf("album art = %+v, want the cover override", s.Assets.AlbumArt)
	}
}

func TestCleanAndExplicitStemsAreClassified(t *testing.T) {
	f := sampleFolder()
	f["vocals_clean.ogg"] = &fstest.MapFile{Data: []byte("c")}
	f["vocals_explicit.ogg"] = &fstest.MapFile{Data: []byte("e")}
	s, err := ScanDir(f)
	if err != nil {
		t.Fatal(err)
	}
	got := map[string]string{}
	for _, st := range s.Assets.Stems {
		got[st.Stem] = st.Variant
	}
	if got["vocals_clean"] != "clean" || got["vocals_explicit"] != "explicit" {
		t.Fatalf("variants = %v", got)
	}
}

// The point of the fs.FS design in ADR-001: a folder and the same content
// packed into a .sng must scan to the same song. If this ever fails, the two
// code paths have diverged and the client and server will disagree.
func TestFolderAndArchiveAgree(t *testing.T) {
	folder := sampleFolder()
	fromDir, err := ScanDir(folder)
	if err != nil {
		t.Fatal(err)
	}

	// Pack the same content: song.ini becomes the metadata section, everything
	// else becomes a file listing - exactly as a real .sng does.
	var meta [][2]string
	var files [][2]string
	for _, k := range []string{"name", "artist", "album", "genre", "year", "charter",
		"song_length", "preview", "diff_guitar", "diff_drums", "diff_vocals",
		"five_lane_drums", "hopo_frequency"} {
		meta = append(meta, [2]string{k, fromDir.RawMetadata[k]})
	}
	for _, n := range []string{"notes.chart", "guitar.ogg", "drums_1.ogg", "song.ogg", "album.png", "readme.txt"} {
		files = append(files, [2]string{n, string(folder[n].Data)})
	}
	raw := buildTestSNG(t, meta, files)

	a, err := sng.Open(bytes.NewReader(raw), int64(len(raw)))
	if err != nil {
		t.Fatalf("open packed archive: %v", err)
	}
	fromSNG, err := ScanArchive(a)
	if err != nil {
		t.Fatal(err)
	}

	if fromDir.ChartHash != fromSNG.ChartHash {
		t.Fatalf("chart hash differs: folder %s vs sng %s", fromDir.ChartHash, fromSNG.ChartHash)
	}
	if fromDir.Name != fromSNG.Name || fromDir.Artist != fromSNG.Artist ||
		fromDir.SongLengthMS != fromSNG.SongLengthMS || fromDir.YearAsNumber != fromSNG.YearAsNumber {
		t.Fatalf("metadata differs:\n folder %+v\n sng    %+v", fromDir, fromSNG)
	}
	if fromDir.Parts != fromSNG.Parts {
		t.Fatalf("parts differ: %+v vs %+v", fromDir.Parts, fromSNG.Parts)
	}
	if len(fromDir.Assets.Stems) != len(fromSNG.Assets.Stems) {
		t.Fatalf("stems differ: %d vs %d", len(fromDir.Assets.Stems), len(fromSNG.Assets.Stems))
	}
	// The package hash covers song.ini in the folder but not in the .sng, where
	// the metadata is in the header rather than a file. So it is EXPECTED to
	// differ, and asserting equality here would be wrong.
	if fromDir.PackageHash == fromSNG.PackageHash {
		t.Fatal("package hashes matched unexpectedly; the folder carries song.ini as a file and the .sng does not")
	}
}

// buildTestSNG mirrors the fixture in the sng package. Same caveat: it proves
// the reader and this encoder agree, not that either matches SngCli.
func buildTestSNG(t *testing.T, meta [][2]string, files [][2]string) []byte {
	t.Helper()
	var key [16]byte
	for i := range key {
		key[i] = byte(i*31 + 7)
	}
	mask := sng.NewMask(key)

	metaBytes := 0
	for _, kv := range meta {
		metaBytes += 4 + len(kv[0]) + 4 + len(kv[1])
	}
	idxBytes := 0
	for _, f := range files {
		idxBytes += 1 + len(f[0]) + 8 + 8
	}
	headerSize := 6 + 4 + 16 + 8 + 8 + metaBytes + 8 + 8 + idxBytes + 8

	var b bytes.Buffer
	b.Write([]byte("SNGPKG"))
	_ = binary.Write(&b, binary.LittleEndian, uint32(1))
	b.Write(key[:])
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
		_ = binary.Write(&b, binary.LittleEndian, pos)
		pos += int64(len(f[1]))
		dataLen += len(f[1])
	}
	_ = binary.Write(&b, binary.LittleEndian, uint64(dataLen))
	for _, f := range files {
		buf := []byte(f[1])
		out := bytes.Clone(buf)
		mask.Apply(out)
		b.Write(out)
	}
	return b.Bytes()
}

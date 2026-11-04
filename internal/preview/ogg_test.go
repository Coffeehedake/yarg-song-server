package preview

import (
	"bytes"
	"encoding/binary"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// buildPage assembles one Ogg page the way an encoder would, CRC included, so the tests exercise
// the real reader rather than a shortcut.
func buildPage(t *testing.T, typeBits byte, granule int64, serial, seq uint32, body []byte) []byte {
	t.Helper()

	nseg := (len(body) + 254) / 255
	if nseg == 0 {
		nseg = 1
	}
	if nseg > 255 {
		t.Fatalf("test body too large for one page: %d", len(body))
	}

	raw := make([]byte, headerBytes+nseg)
	copy(raw, capturePattern)
	raw[4] = 0
	raw[offTypeBits] = typeBits
	binary.LittleEndian.PutUint64(raw[offGranule:], uint64(granule))
	binary.LittleEndian.PutUint32(raw[offSerial:], serial)
	binary.LittleEndian.PutUint32(raw[offSeq:], seq)
	raw[offSegCount] = byte(nseg)

	left := len(body)
	for i := 0; i < nseg; i++ {
		n := left
		if n > 255 {
			n = 255
		}
		raw[headerBytes+i] = byte(n)
		left -= n
	}

	raw = append(raw, body...)
	binary.LittleEndian.PutUint32(raw[offCRC:], crcOf(raw))
	return raw
}

func opusHead() []byte {
	b := make([]byte, 19)
	copy(b, "OpusHead")
	b[8] = 1                                     // version
	b[9] = 2                                     // channels
	binary.LittleEndian.PutUint16(b[10:], 312)   // pre-skip
	binary.LittleEndian.PutUint32(b[12:], 48000) // input rate (NOT the granule rate)
	return b
}

// A stream with two header pages and audio pages one 48 kHz second apart.
func syntheticOpus(t *testing.T, seconds int) []byte {
	t.Helper()
	var buf bytes.Buffer
	buf.Write(buildPage(t, 0x02, 0, 7, 0, opusHead()))
	tags := append([]byte("OpusTags"), 0, 0, 0, 0, 0, 0, 0, 0)
	buf.Write(buildPage(t, 0x00, 0, 7, 1, tags))
	for i := 1; i <= seconds; i++ {
		body := bytes.Repeat([]byte{byte(i)}, 64)
		buf.Write(buildPage(t, 0x00, int64(i)*48000, 7, uint32(i+1), body))
	}
	return buf.Bytes()
}

func pagesOf(t *testing.T, b []byte) []page {
	t.Helper()
	var out []page
	r := bytes.NewReader(b)
	for {
		p, err := readPage(r)
		if err == io.EOF {
			return out
		}
		if err != nil {
			t.Fatalf("readPage: %v", err)
		}
		out = append(out, p)
	}
}

// The CRC is the one thing here that CANNOT be checked against this package's own writer, and
// that is the whole reason this test exists. Ogg's CRC-32 uses the same polynomial as
// crc32.IEEE but UNREFLECTED, so a wrong implementation produces plausible numbers and a stream
// every real decoder rejects - and a round trip through buildPage and crcOf would agree with
// itself either way.
//
// So it is checked against CRCs written by OTHER people's encoders: the .ogg stems inside the
// real song packages. Gated on YARG_SNG_DIR, and INCONCLUSIVE rather than passing when unset,
// because a silent skip here would report success for the only independent check in the file.
func TestOggCRCAgreesWithRealEncoders(t *testing.T) {
	dir := os.Getenv("YARG_SNG_DIR")
	if dir == "" {
		t.Skip("YARG_SNG_DIR unset: the CRC is NOT verified against an independent encoder")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	checkedFiles, checkedPages := 0, 0
	for _, e := range entries {
		if checkedFiles >= 5 || e.IsDir() || !strings.HasSuffix(e.Name(), ".sng") {
			continue
		}

		f, err := os.Open(filepath.Join(dir, e.Name()))
		if err != nil {
			continue
		}
		st, err := f.Stat()
		if err != nil {
			f.Close()
			continue
		}
		a, err := sng.Open(f, st.Size())
		if err != nil {
			f.Close()
			continue
		}

		var oggName string
		_ = fs.WalkDir(a, ".", func(p string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || oggName != "" {
				return nil
			}
			if strings.HasSuffix(strings.ToLower(p), ".ogg") ||
				strings.HasSuffix(strings.ToLower(p), ".opus") {
				oggName = p
			}
			return nil
		})
		if oggName == "" {
			f.Close()
			continue
		}

		h, err := a.Open(oggName)
		if err != nil {
			f.Close()
			continue
		}
		data, err := io.ReadAll(io.LimitReader(h, 2<<20))
		h.Close()
		f.Close()
		if err != nil || len(data) < headerBytes {
			continue
		}

		r := bytes.NewReader(data)
		filePages := 0
		for {
			p, err := readPage(r)
			if err != nil {
				break
			}
			stored := binary.LittleEndian.Uint32(p.raw[offCRC:])
			if got := crcOf(p.raw); got != stored {
				t.Fatalf("%s page %d of %s: encoder wrote CRC %#08x, we compute %#08x",
					e.Name(), filePages, oggName, stored, got)
			}
			filePages++
			checkedPages++
			if filePages >= 40 {
				break
			}
		}
		if filePages > 0 {
			checkedFiles++
			t.Logf("%s: %d pages of %s agree", e.Name(), filePages, oggName)
		}
	}

	if checkedPages == 0 {
		t.Fatalf("no Ogg pages found under %s - the CRC was NOT verified", dir)
	}
	t.Logf("CRC agrees with real encoders on %d pages across %d packages",
		checkedPages, checkedFiles)
}

func TestSampleRateOfVorbisAndOpus(t *testing.T) {
	v := make([]byte, 16)
	v[0] = 1
	copy(v[1:], "vorbis")
	binary.LittleEndian.PutUint32(v[12:], 44100)
	if got, err := sampleRateOf(v); err != nil || got != 44100 {
		t.Fatalf("vorbis: got %d, %v", got, err)
	}

	// Opus granule positions are 48 kHz regardless of the rate in OpusHead, which says 48000
	// here anyway; the assertion that matters is that the field is NOT what is read.
	o := opusHead()
	binary.LittleEndian.PutUint32(o[12:], 16000) // input rate deliberately not 48 kHz
	if got, err := sampleRateOf(o); err != nil || got != 48000 {
		t.Fatalf("opus: got %d, %v - the input-rate field must not be used", got, err)
	}

	if _, err := sampleRateOf([]byte("not a codec header")); err == nil {
		t.Fatal("expected ErrNoHeaders for an unknown codec")
	}
}

func TestCutCopiesHeadersAndSelectsByGranule(t *testing.T) {
	in := syntheticOpus(t, 10)

	var out bytes.Buffer
	err := CutOgg(bytes.NewReader(in), 3000, 4000, &out)
	if err != nil && !IsShortStream(err) {
		t.Fatalf("CutOgg: %v", err)
	}

	got := pagesOf(t, out.Bytes())
	if len(got) < 3 {
		t.Fatalf("expected headers plus audio, got %d pages", len(got))
	}

	// The two header pages must be present and first, or the clip is not standalone.
	if !bytes.Equal(got[0].payload()[:8], []byte("OpusHead")) {
		t.Fatalf("first page is not OpusHead: %q", got[0].payload()[:8])
	}
	if !bytes.Equal(got[1].payload()[:8], []byte("OpusTags")) {
		t.Fatalf("second page is not OpusTags: %q", got[1].payload()[:8])
	}

	// Sequence numbers must be contiguous from zero.
	for i, p := range got {
		if seq := binary.LittleEndian.Uint32(p.raw[offSeq:]); seq != uint32(i) {
			t.Fatalf("page %d has sequence %d; a renumbered stream must count from 0", i, seq)
		}
	}

	// Granule positions must be rebased, so the clip does not read as a seek into a longer
	// stream. The first audio page sits at 3 s in the source.
	first := got[2].granule()
	if first >= 3*48000 {
		t.Fatalf("granule %d was not rebased (source value was %d)", first, 3*48000)
	}

	// Every page must re-seal correctly, including the ones whose headers were rewritten.
	for i, p := range got {
		stored := binary.LittleEndian.Uint32(p.raw[offCRC:])
		if c := crcOf(p.raw); c != stored {
			t.Fatalf("page %d CRC %#08x does not match recomputed %#08x", i, stored, c)
		}
	}

	// The last page must carry EOS, or a decoder treats the clip as truncated.
	if last := got[len(got)-1]; last.typeBits()&flagEOS == 0 {
		t.Fatal("last page does not carry EOS")
	}
}

// The failure this guards: a cut that ignores startMS and copies from the beginning. It would
// pass every structural assertion above.
func TestCutStartsWhereItWasAsked(t *testing.T) {
	in := syntheticOpus(t, 20)

	for _, startMS := range []int64{0, 5000, 12000} {
		var out bytes.Buffer
		err := CutOgg(bytes.NewReader(in), startMS, 2000, &out)
		if err != nil && !IsShortStream(err) {
			t.Fatalf("start %d: %v", startMS, err)
		}

		got := pagesOf(t, out.Bytes())
		if len(got) < 3 {
			t.Fatalf("start %d: only %d pages", startMS, len(got))
		}

		// Payload bytes in the synthetic stream are the source second, so the first audio page
		// names where the cut really began - independently of any granule arithmetic.
		second := int64(got[2].payload()[0])
		want := startMS / 1000
		if want == 0 {
			want = 1 // the first audio page is at 1 s
		}
		if second != want {
			t.Fatalf("start %d ms: clip begins at source second %d, expected %d",
				startMS, second, want)
		}
	}
}

func TestCutRejectsNonOggAndBadDuration(t *testing.T) {
	var out bytes.Buffer
	if err := CutOgg(strings.NewReader("ID3 this is an mp3"), 0, 1000, &out); err == nil {
		t.Fatal("expected a non-Ogg input to be refused")
	}
	if err := CutOgg(bytes.NewReader(syntheticOpus(t, 3)), 0, 0, &out); err == nil {
		t.Fatal("expected a zero duration to be refused")
	}
}

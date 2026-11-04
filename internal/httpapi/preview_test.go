package httpapi

import (
	"bytes"
	"encoding/binary"
	"io"
	"net/http"
	"strings"
	"testing"
)

// oggCRCTable is Ogg's CRC-32: the crc32.IEEE polynomial, UNREFLECTED. Spelled out here rather
// than imported from internal/preview on purpose - a test that borrows the implementation it is
// checking agrees with its own bugs.
var oggCRCTable = func() [256]uint32 {
	var t [256]uint32
	for i := range t {
		r := uint32(i) << 24
		for j := 0; j < 8; j++ {
			if r&0x80000000 != 0 {
				r = (r << 1) ^ 0x04c11db7
			} else {
				r <<= 1
			}
		}
		t[i] = r
	}
	return t
}()

func oggCRCOf(p []byte) uint32 {
	var c uint32
	for i, b := range p {
		if i >= 22 && i < 26 {
			b = 0
		}
		c = (c << 8) ^ oggCRCTable[byte(c>>24)^b]
	}
	return c
}

func oggPage(typeBits byte, granule int64, seq uint32, body []byte) []byte {
	nseg := (len(body) + 254) / 255
	if nseg == 0 {
		nseg = 1
	}
	raw := make([]byte, 27+nseg)
	copy(raw, "OggS")
	raw[5] = typeBits
	binary.LittleEndian.PutUint64(raw[6:], uint64(granule))
	binary.LittleEndian.PutUint32(raw[14:], 0xfeed)
	binary.LittleEndian.PutUint32(raw[18:], seq)
	raw[26] = byte(nseg)
	left := len(body)
	for i := 0; i < nseg; i++ {
		n := left
		if n > 255 {
			n = 255
		}
		raw[27+i] = byte(n)
		left -= n
	}
	raw = append(raw, body...)
	binary.LittleEndian.PutUint32(raw[22:], oggCRCOf(raw))
	return raw
}

// syntheticVorbis is a structurally valid Ogg Vorbis stream of `seconds` seconds at 44.1 kHz.
//
// Its codebooks are nonsense, so it does not DECODE - and that is acceptable here because this
// file tests routing, stem selection, statuses and caching, none of which decodes anything. The
// cut itself is measured in internal/preview against real encoders' CRCs, and the whole pipeline
// was verified by decoding a real clip with VLC's libvorbis (see ADR-009).
//
// A real .ogg is deliberately NOT committed as a fixture: "never distribute copyrighted audio"
// is a hard constraint in this repo, not a preference.
func syntheticVorbis(seconds int) []byte {
	ident := make([]byte, 30)
	ident[0] = 1
	copy(ident[1:], "vorbis")
	ident[11] = 2                                    // channels
	binary.LittleEndian.PutUint32(ident[12:], 44100) // sample rate

	comment := append([]byte{3}, "vorbis"...)
	setup := append([]byte{5}, "vorbis"...)

	var buf bytes.Buffer
	buf.Write(oggPage(0x02, 0, 0, ident))
	buf.Write(oggPage(0x00, 0, 1, append(comment, setup...)))
	for i := 1; i <= seconds; i++ {
		buf.Write(oggPage(0x00, int64(i)*44100, uint32(i+1), bytes.Repeat([]byte{byte(i)}, 256)))
	}
	return buf.Bytes()
}

func previewSpec(name string, audio map[string]string) []spec {
	return []spec{{
		dir:   name,
		seed:  name,
		ini:   map[string]string{"name": name, "artist": "A", "preview_start_time": "5000"},
		extra: audio,
	}}
}

func TestPreviewCutsAClipFromTheStem(t *testing.T) {
	srv, ix := newTestServer(t, previewSpec("ogg-song",
		map[string]string{"song.ogg": string(syntheticVorbis(60))}))
	defer srv.Close()

	hashes := ix.Missing(nil)
	if len(hashes) != 1 {
		t.Fatalf("expected one song, got %d", len(hashes))
	}
	hash := hashes[0]

	r, err := http.Get(srv.URL + "/song/" + hash + "/preview.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)

	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", r.StatusCode, body)
	}
	if ct := r.Header.Get("Content-Type"); ct != "audio/ogg" {
		t.Fatalf("content type %q", ct)
	}
	if !bytes.HasPrefix(body, []byte("OggS")) {
		t.Fatal("the clip does not start with an Ogg page")
	}

	// The whole point: a clip, not the stem. The source is 60 seconds and the preview window
	// is 30, so a response the size of the source means nothing was cut.
	source := len(syntheticVorbis(60))
	if len(body) >= source {
		t.Fatalf("clip is %d bytes and the stem is %d - nothing was cut", len(body), source)
	}

	etag := r.Header.Get("ETag")
	if etag == "" {
		t.Fatal("no ETag, so clients cannot cache a deterministic clip")
	}

	// Deterministic output is what makes the strong ETag sound, so a second request must
	// produce identical bytes rather than merely the same length.
	r2, err := http.Get(srv.URL + "/song/" + hash + "/preview.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer r2.Body.Close()
	body2, _ := io.ReadAll(r2.Body)
	if !bytes.Equal(body, body2) {
		t.Fatal("two requests returned different bytes; the ETag is then a lie")
	}

	req, _ := http.NewRequest("GET", srv.URL+"/song/"+hash+"/preview.ogg", nil)
	req.Header.Set("If-None-Match", etag)
	r3, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer r3.Body.Close()
	if r3.StatusCode != http.StatusNotModified {
		t.Fatalf("If-None-Match gave %d, expected 304", r3.StatusCode)
	}
}

func TestPreviewRefusesFormatsItCannotCut(t *testing.T) {
	// noAudio, or the harness's default song.ogg would be tried first and this would be
	// measuring an uncuttable Ogg rather than an unsupported format.
	srv, ix := newTestServer(t, []spec{{
		dir:     "mp3-song",
		seed:    "mp3-song",
		ini:     map[string]string{"name": "m", "artist": "A", "preview_start_time": "5000"},
		extra:   map[string]string{"song.mp3": "ID3\x03\x00\x00\x00\x00\x00\x00not really an mp3"},
		noAudio: true,
	}})
	defer srv.Close()

	hash := ix.Missing(nil)[0]
	r, err := http.Get(srv.URL + "/song/" + hash + "/preview.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)

	// 415 and not 500: the server understood the request and cannot satisfy it yet. MP3 is 449
	// of the live library's 22,091 stem files, so this path is real and must say what it means.
	if r.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d (%s), expected 415", r.StatusCode, body)
	}
	if !strings.Contains(string(body), ".mp3") {
		t.Fatalf("the refusal does not name the format: %s", body)
	}
}

func TestPreviewUnknownHashIs404(t *testing.T) {
	srv, _ := newTestServer(t, previewSpec("x",
		map[string]string{"song.ogg": string(syntheticVorbis(10))}))
	defer srv.Close()

	r, err := http.Get(srv.URL + "/song/" + strings.Repeat("a", 40) + "/preview.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	if r.StatusCode != http.StatusNotFound {
		t.Fatalf("status %d, expected 404", r.StatusCode)
	}
}

// A song with no full mix must still get a preview, from whatever stem it does have. Around 224
// of the live library's 4,085 songs are in this position; without the fallback they would get
// whichever file the archive listed first, or nothing.
func TestPreviewFallsBackWhenThereIsNoFullMix(t *testing.T) {
	srv, ix := newTestServer(t, previewSpec("no-mix", map[string]string{
		"guitar.ogg": string(syntheticVorbis(45)),
		"drums.ogg":  string(syntheticVorbis(45)),
	}))
	defer srv.Close()

	hash := ix.Missing(nil)[0]
	r, err := http.Get(srv.URL + "/song/" + hash + "/preview.ogg")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)

	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s) - a song without a full mix got no preview", r.StatusCode, body)
	}
	if !bytes.HasPrefix(body, []byte("OggS")) {
		t.Fatal("fallback clip is not an Ogg stream")
	}
}

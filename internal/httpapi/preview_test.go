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

// syntheticVorbisAt is syntheticVorbis with a chosen payload size per second, so a test can make
// one stem busy and another nearly empty and then tell their clips apart by size.
//
// It does NOT make a test able to judge audibility: ADR-009 records that clip size separates two
// clips cut from one window and cannot say whether one clip on its own can be heard. Sparse audio
// compresses like silence.
func syntheticVorbisAt(seconds, bytesPerSecond int) []byte {
	ident := make([]byte, 30)
	ident[0] = 1
	copy(ident[1:], "vorbis")
	ident[11] = 2
	binary.LittleEndian.PutUint32(ident[12:], 44100)

	comment := append([]byte{3}, "vorbis"...)
	setup := append([]byte{5}, "vorbis"...)

	var buf bytes.Buffer
	buf.Write(oggPage(0x02, 0, 0, ident))
	buf.Write(oggPage(0x00, 0, 1, append(comment, setup...)))
	for i := 1; i <= seconds; i++ {
		buf.Write(oggPage(0x00, int64(i)*44100, uint32(i+1),
			bytes.Repeat([]byte{byte(i)}, bytesPerSecond)))
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

// One MPEG1 Layer III frame at 128 kbit/s, 44.1 kHz: 417 bytes, 26.12 ms. The payload is
// filler; the endpoint never decodes it, it only copies whole frames.
func mp3Frames(n int) string {
	var b []byte
	for i := 0; i < n; i++ {
		f := make([]byte, 417)
		f[0], f[1], f[2], f[3] = 0xFF, 0xFB, 0x90, 0x00
		for j := 4; j < len(f); j++ {
			f[j] = byte(i + 1)
		}
		b = append(b, f...)
	}
	return string(b)
}

// 150 of the live library's 4,085 songs carry ONLY .mp3 stems - Metallica, Foo Fighters,
// Queens of the Stone Age - and every one of them used to answer 415 and play nothing.
func TestPreviewCutsMP3Stems(t *testing.T) {
	srv, ix := newTestServer(t, []spec{{
		dir:     "mp3-only",
		seed:    "mp3-only",
		ini:     map[string]string{"name": "m", "artist": "A", "preview_start_time": "1000"},
		extra:   map[string]string{"song.mp3": mp3Frames(2000)},
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

	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s)", r.StatusCode, body)
	}
	// The type follows the bytes, not the URL the caller spelled.
	if ct := r.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("content type %q, expected audio/mpeg", ct)
	}
	if !bytes.HasPrefix(body, []byte{0xFF, 0xFB}) {
		t.Fatalf("clip does not start with an MP3 frame header: % x", body[:4])
	}
	source := len(mp3Frames(2000))
	if len(body) >= source {
		t.Fatalf("clip is %d bytes and the stem is %d - nothing was cut", len(body), source)
	}
}

// What is left that genuinely cannot be cut. There are no .wav stems in the live library at all,
// which is why no WAV cutter exists - but the refusal path still has to say what it means.
func TestPreviewRefusesFormatsItCannotCut(t *testing.T) {
	srv, ix := newTestServer(t, []spec{{
		dir:     "wav-song",
		seed:    "wav-song",
		ini:     map[string]string{"name": "w", "artist": "A", "preview_start_time": "5000"},
		extra:   map[string]string{"song.wav": "RIFF....WAVEfmt not really a wav"},
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

	// 415 and not 500: the server understood the request and cannot satisfy it.
	if r.StatusCode != http.StatusUnsupportedMediaType {
		t.Fatalf("status %d (%s), expected 415", r.StatusCode, body)
	}
	if !strings.Contains(string(body), ".wav") {
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

// The per-stem endpoint, which exists so the CLIENT can mix rather than the server choosing.
//
// Upstream's LoadPreviewAudio adds every stem to one mixer (YARG.Core's CreateAudioMixer), so a
// server that picks one stem is answering a question the client already answers - and picking
// badly, as ADR-009 records at length. This serves each stem's clip separately.
func TestPreviewPerStem(t *testing.T) {
	srv, ix := newTestServer(t, previewSpec("multi-stem", map[string]string{
		"song.ogg":    string(syntheticVorbisAt(60, 64)),   // the near-empty backing stem
		"guitar.ogg":  string(syntheticVorbisAt(60, 8192)), // where the music is
		"drums_1.ogg": string(syntheticVorbisAt(60, 4096)),
	}))
	defer srv.Close()

	hash := ix.Missing(nil)[0]

	fetch := func(stem string) (*http.Response, []byte) {
		t.Helper()
		r, err := http.Get(srv.URL + "/song/" + hash + "/preview/" + stem)
		if err != nil {
			t.Fatal(err)
		}
		body, _ := io.ReadAll(r.Body)
		r.Body.Close()
		return r, body
	}

	rGuitar, guitar := fetch("guitar")
	if rGuitar.StatusCode != http.StatusOK {
		t.Fatalf("guitar: status %d (%s)", rGuitar.StatusCode, guitar)
	}
	if ct := rGuitar.Header.Get("Content-Type"); ct != "audio/ogg" {
		t.Fatalf("guitar: content type %q", ct)
	}
	if !bytes.HasPrefix(guitar, []byte("OggS")) {
		t.Fatal("guitar: not an Ogg stream")
	}

	rSong, song := fetch("song")
	if rSong.StatusCode != http.StatusOK {
		t.Fatalf("song: status %d (%s)", rSong.StatusCode, song)
	}

	// Each stem must give its OWN bytes. If the handler ignored the stem and served whatever
	// previewCandidates picked, these would be equal and the client would mix one stem N times.
	if bytes.Equal(guitar, song) {
		t.Fatal("guitar and song returned identical bytes; the stem parameter is being ignored")
	}

	// And the ETags must differ, or a cache keyed on them serves one stem's bytes for another -
	// which is worse than no caching, because it is silent and it is per-viewer.
	if a, b := rGuitar.Header.Get("ETag"), rSong.Header.Get("ETag"); a == "" || a == b {
		t.Fatalf("ETags are %q and %q; each stem needs its own or a cache will cross them", a, b)
	}

	rDrums, drums := fetch("drums_1")
	if rDrums.StatusCode != http.StatusOK {
		t.Fatalf("drums_1: status %d (%s)", rDrums.StatusCode, drums)
	}

	// A stem the song does not have is a 404, not the server quietly substituting another.
	rMissing, missing := fetch("keys")
	if rMissing.StatusCode != http.StatusNotFound {
		t.Fatalf("keys: status %d (%s), expected 404", rMissing.StatusCode, missing)
	}

	// An unknown chart is still a 404 here, same as the single-clip endpoint.
	rBadHash, _ := fetch2(t, srv.URL+"/song/"+strings.Repeat("b", 40)+"/preview/song")
	if rBadHash != http.StatusNotFound {
		t.Fatalf("unknown hash: status %d, expected 404", rBadHash)
	}
}

func fetch2(t *testing.T, url string) (int, []byte) {
	t.Helper()
	r, err := http.Get(url)
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	b, _ := io.ReadAll(r.Body)
	return r.StatusCode, b
}

// MP3 stems go through the same endpoint, and the type must follow the bytes here too.
func TestPreviewPerStemMP3(t *testing.T) {
	srv, ix := newTestServer(t, []spec{{
		dir:     "mp3-stems",
		seed:    "mp3-stems",
		ini:     map[string]string{"name": "m", "artist": "A", "preview_start_time": "1000"},
		extra:   map[string]string{"song.mp3": mp3Frames(2000), "guitar.mp3": mp3Frames(2000)},
		noAudio: true,
	}})
	defer srv.Close()

	hash := ix.Missing(nil)[0]
	r, err := http.Get(srv.URL + "/song/" + hash + "/preview/guitar")
	if err != nil {
		t.Fatal(err)
	}
	defer r.Body.Close()
	body, _ := io.ReadAll(r.Body)

	if r.StatusCode != http.StatusOK {
		t.Fatalf("status %d (%s)", r.StatusCode, body)
	}
	if ct := r.Header.Get("Content-Type"); ct != "audio/mpeg" {
		t.Fatalf("content type %q, expected audio/mpeg", ct)
	}
	if !bytes.HasPrefix(body, []byte{0xFF, 0xFB}) {
		t.Fatalf("not an MP3 frame: % x", body[:4])
	}
}

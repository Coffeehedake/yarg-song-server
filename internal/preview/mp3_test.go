package preview

import (
	"bytes"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// mp3Frame builds one MPEG1 Layer III frame at 128 kbit/s, 44.1 kHz: 417 bytes, 26.12 ms.
// The payload is filled with `mark` so a test can tell WHERE a cut began without trusting any
// of this package's own arithmetic.
func mp3Frame(mark byte) []byte {
	f := make([]byte, 417)
	f[0] = 0xFF
	f[1] = 0xFB // MPEG1, Layer III, no CRC
	f[2] = 0x90 // bitrate index 9 (128k), sample rate index 0 (44100), no padding
	f[3] = 0x00
	for i := 4; i < len(f); i++ {
		f[i] = mark
	}
	return f
}

func syntheticMP3(frames int) []byte {
	var buf bytes.Buffer
	for i := 1; i <= frames; i++ {
		buf.Write(mp3Frame(byte(i)))
	}
	return buf.Bytes()
}

// The frame-length formula is the one thing here that cannot be checked against this package's
// own writer: mp3Frame uses the same constants, so a round trip would agree with itself whatever
// they were. Getting it wrong is not subtle in its effects - every frame after the first lands at
// the wrong offset and the clip is noise - but it IS subtle to spot, because resync papers over
// it and still produces a plausible-looking file.
//
// So it is measured against files OTHER people's encoders produced: the .mp3 stems inside the
// real song packages. If the formula is right, frames tile each file exactly and almost no bytes
// need skipping. If it is wrong, the resync path eats real audio and the coverage collapses.
//
// Gated on YARG_SNG_DIR, and it SKIPS LOUDLY rather than passing, because this is the only
// independent check in the file.
func TestMP3FramesTileRealFiles(t *testing.T) {
	// Its own variable, because the ogg and mp3 checks need DIFFERENT packages - the local
	// mirror has no mp3 songs at all, they live only on the server.
	dir := os.Getenv("YARG_MP3_DIR")
	if dir == "" {
		dir = os.Getenv("YARG_SNG_DIR")
	}
	if dir == "" {
		t.Skip("YARG_MP3_DIR unset: the frame formula is NOT verified against real encoders")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("read %s: %v", dir, err)
	}

	checked := 0
	for _, e := range entries {
		if checked >= 3 || e.IsDir() || !strings.HasSuffix(e.Name(), ".sng") {
			continue
		}

		data, name := firstStem(t, filepath.Join(dir, e.Name()), ".mp3")
		if data == nil {
			continue
		}

		// Walk with the pure header reader, counting what is frame and what had to be skipped.
		var framed, skipped, frames int
		for i := 0; i < len(data)-4; {
			length, _, ok := frameSize(data[i : i+4])
			if ok && i+length <= len(data) {
				framed += length
				frames++
				i += length
				continue
			}
			skipped++
			i++
		}

		covered := float64(framed) / float64(len(data)) * 100
		t.Logf("%s (%s): %d bytes, %d frames, %.1f%% covered, %d bytes skipped",
			e.Name(), name, len(data), frames, covered, skipped)

		if frames == 0 {
			t.Fatalf("%s: no frames found at all - the formula or the sync test is wrong", name)
		}
		// SKIPPED BYTES, not coverage, is the discriminating number - and this was measured,
		// not assumed. Changing 144 to 143 in the length formula still covered 99.3% and passed
		// a 95% threshold, because resync recovers within a few bytes of each frame and the
		// error is only ~3 bytes in ~417. The file's own comment predicted exactly that.
		//
		// A correct formula skips NOTHING inside the audio: every frame ends where the next
		// header begins. One byte per frame of slack is already far more than correct parsing
		// needs, and a 1-in-144 error produces thousands.
		if skipped > frames {
			t.Fatalf("%s: %d bytes skipped across %d frames (%.1f%% covered); a correct "+
				"formula lands on every header and skips none", name, skipped, frames, covered)
		}
		checked++
	}

	if checked == 0 {
		t.Fatalf("no .mp3 stem found under %s - the formula was NOT verified", dir)
	}
}

func firstStem(t *testing.T, path, ext string) ([]byte, string) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		return nil, ""
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return nil, ""
	}
	a, err := sng.Open(f, st.Size())
	if err != nil {
		return nil, ""
	}

	var found string
	_ = fs.WalkDir(a, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if strings.HasSuffix(strings.ToLower(p), ext) {
			found = p
		}
		return nil
	})
	if found == "" {
		return nil, ""
	}

	h, err := a.Open(found)
	if err != nil {
		return nil, ""
	}
	defer h.Close()
	data, err := io.ReadAll(io.LimitReader(h, 4<<20))
	if err != nil {
		return nil, ""
	}
	return data, found
}

func TestCutMP3StartsWhereItWasAsked(t *testing.T) {
	// 26.12 ms per frame, so 400 frames is about 10.4 seconds.
	in := syntheticMP3(400)

	for _, startMS := range []int64{0, 2000, 5000} {
		var out bytes.Buffer
		err := CutMP3(bytes.NewReader(in), startMS, 1000, &out)
		if err != nil && !IsShortStream(err) {
			t.Fatalf("start %d: %v", startMS, err)
		}
		if out.Len() == 0 {
			t.Fatalf("start %d: nothing written", startMS)
		}

		// The payload byte names the source frame, so this is independent of the arithmetic
		// under test: frame N begins at (N-1) * 26.12 ms.
		got := int(out.Bytes()[4])
		want := int(float64(startMS)/26.12) + 1
		if got < want-1 || got > want+1 {
			t.Fatalf("start %d ms: clip begins at source frame %d, expected about %d",
				startMS, got, want)
		}
	}
}

func TestCutMP3LengthIsAboutRight(t *testing.T) {
	in := syntheticMP3(400)
	var out bytes.Buffer
	if err := CutMP3(bytes.NewReader(in), 1000, 2000, &out); err != nil && !IsShortStream(err) {
		t.Fatal(err)
	}

	frames := out.Len() / 417
	// 2000 ms at 26.12 ms per frame is about 77 frames; one either side for the boundary.
	if frames < 74 || frames > 80 {
		t.Fatalf("expected about 77 frames for 2000 ms, got %d", frames)
	}
}

// An ID3v2 tag must be stepped over, not copied in. Its size field is SYNCSAFE - seven
// significant bits per byte - so a plain big-endian read overshoots and lands inside the audio.
func TestCutMP3SkipsAnID3Tag(t *testing.T) {
	tag := make([]byte, 10+200)
	copy(tag, "ID3")
	tag[3], tag[4] = 4, 0
	// 200 as syncsafe: 0x00 0x00 0x01 0x48
	tag[6], tag[7], tag[8], tag[9] = 0x00, 0x00, 0x01, 0x48

	in := append(tag, syntheticMP3(200)...)
	var out bytes.Buffer
	if err := CutMP3(bytes.NewReader(in), 0, 500, &out); err != nil && !IsShortStream(err) {
		t.Fatal(err)
	}
	if out.Len() == 0 {
		t.Fatal("nothing written")
	}
	if !bytes.HasPrefix(out.Bytes(), []byte{0xFF, 0xFB}) {
		t.Fatalf("clip does not start with a frame header: % x", out.Bytes()[:4])
	}
	// Frame 1, not something further in - the tag must not have eaten audio.
	if got := out.Bytes()[4]; got != 1 {
		t.Fatalf("clip starts at source frame %d; the ID3 skip consumed audio", got)
	}
}

func TestCutMP3RejectsRubbish(t *testing.T) {
	var out bytes.Buffer
	if err := CutMP3(strings.NewReader(strings.Repeat("not audio ", 200)), 0, 1000, &out); err == nil {
		t.Fatal("expected a non-MP3 input to be refused")
	}
	if err := CutMP3(bytes.NewReader(syntheticMP3(10)), 0, 0, &out); err == nil {
		t.Fatal("expected a zero duration to be refused")
	}
}

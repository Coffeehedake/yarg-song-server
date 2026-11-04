package preview

import (
	"bufio"
	"errors"
	"fmt"
	"io"
)

// ErrNotMP3 is returned when no MP3 frame can be found at all.
var ErrNotMP3 = errors.New("preview: no MP3 frame found")

// CutMP3 writes roughly durMS milliseconds of src starting at startMS.
//
// Same idea as CutOgg and the same reason for it (docs/ADR-009-preview-clips.md): an MP3 is a row
// of self-contained frames, so a clip is a run of whole frames copied out. Nothing is decoded and
// nothing is re-encoded.
//
// This exists because 150 of the live library's 4,085 songs carry ONLY .mp3 stems - Metallica,
// Foo Fighters, Queens of the Stone Age among them - and every one of those answered 415 and
// played nothing. 449 of 22,091 stem files are .mp3, but most of those sit beside an .ogg that
// the Ogg cutter was already using, which is why the song count is the number that matters.
//
// There are no .wav stems in this library at all, so a WAV cutter would be code nobody runs.
// Said out loud because "and WAV is trivial" is true and irrelevant.
func CutMP3(src io.Reader, startMS, durMS int64, dst io.Writer) error {
	if durMS <= 0 {
		return errors.New("preview: duration must be positive")
	}
	if startMS < 0 {
		startMS = 0
	}

	r := bufio.NewReaderSize(src, 1<<16)
	if err := skipID3v2(r); err != nil {
		return err
	}

	var (
		elapsedMS float64
		written   int
		found     bool
	)

	for {
		frame, ms, err := readFrame(r)
		if err == io.EOF {
			if !found {
				return ErrNotMP3
			}
			if written == 0 {
				return fmt.Errorf("preview: no audio at or after %d ms", startMS)
			}
			return errShortStream
		}
		if err != nil {
			return err
		}
		found = true

		if elapsedMS >= float64(startMS) {
			if _, err := dst.Write(frame); err != nil {
				return err
			}
			written++
			if elapsedMS >= float64(startMS+durMS) {
				return nil
			}
		}

		elapsedMS += ms
	}
}

// skipID3v2 steps over a leading ID3v2 tag, which is not audio and would be copied into the clip
// as a frame-shaped lump of metadata otherwise.
//
// The size is SYNCSAFE: four bytes of seven significant bits each, so a plain big-endian read is
// wrong by up to 256 bytes per byte and lands mid-frame. The resync in readFrame would recover,
// but it would do so by discarding real audio.
func skipID3v2(r *bufio.Reader) error {
	head, err := r.Peek(10)
	if err != nil {
		if err == io.EOF {
			return nil // too short to hold a tag; let readFrame report it
		}
		return err
	}
	if string(head[:3]) != "ID3" {
		return nil
	}

	size := int(head[6]&0x7f)<<21 | int(head[7]&0x7f)<<14 |
		int(head[8]&0x7f)<<7 | int(head[9]&0x7f)
	if _, err := r.Discard(10 + size); err != nil && err != io.EOF {
		return err
	}
	return nil
}

// MPEG Layer III bitrates in kbit/s, by version and bitrate index.
var (
	bitratesV1 = [16]int{0, 32, 40, 48, 56, 64, 80, 96, 112, 128, 160, 192, 224, 256, 320, 0}
	bitratesV2 = [16]int{0, 8, 16, 24, 32, 40, 48, 56, 64, 80, 96, 112, 128, 144, 160, 0}

	sampleRates = map[byte][3]int{
		3: {44100, 48000, 32000}, // MPEG 1
		2: {22050, 24000, 16000}, // MPEG 2
		0: {11025, 12000, 8000},  // MPEG 2.5
	}
)

// readFrame returns one whole frame and how many milliseconds of audio it holds.
//
// It RESYNCS rather than giving up: real files carry junk between frames, and a cutter that
// stopped at the first unparseable byte would truncate a clip for a reason the listener cannot
// see. Anything skipped is reported by nothing, which is correct - it is not audio.
func readFrame(r *bufio.Reader) ([]byte, float64, error) {
	for {
		head, err := r.Peek(4)
		if err != nil {
			return nil, 0, io.EOF
		}

		length, ms, ok := frameSize(head)
		if !ok {
			// Not a frame header. Step one byte and look again.
			if _, err := r.Discard(1); err != nil {
				return nil, 0, io.EOF
			}
			continue
		}

		frame := make([]byte, length)
		if _, err := io.ReadFull(r, frame); err != nil {
			return nil, 0, io.EOF
		}
		return frame, ms, nil
	}
}

// frameSize reads a 4-byte header and returns the whole frame's length in bytes and duration.
//
// Layer III only, which is every MP3 in this library and every MP3 anyone means by "an mp3".
// Layer I and II have different formulas; they are rejected rather than mis-measured, because a
// wrong length desynchronises everything after it and the clip becomes noise.
func frameSize(h []byte) (length int, ms float64, ok bool) {
	if h[0] != 0xFF || h[1]&0xE0 != 0xE0 {
		return 0, 0, false
	}

	version := (h[1] >> 3) & 0x03 // 3 = MPEG1, 2 = MPEG2, 0 = MPEG2.5, 1 = reserved
	layer := (h[1] >> 1) & 0x03   // 1 = Layer III
	if layer != 1 || version == 1 {
		return 0, 0, false
	}

	rates, known := sampleRates[version]
	if !known {
		return 0, 0, false
	}

	bitrateIndex := (h[2] >> 4) & 0x0F
	rateIndex := (h[2] >> 2) & 0x03
	if bitrateIndex == 0 || bitrateIndex == 15 || rateIndex == 3 {
		// Free-format and "bad" are both unusable: the first means the length is not derivable
		// from the header at all.
		return 0, 0, false
	}

	var kbps int
	if version == 3 {
		kbps = bitratesV1[bitrateIndex]
	} else {
		kbps = bitratesV2[bitrateIndex]
	}
	sampleRate := rates[rateIndex]
	if kbps == 0 || sampleRate == 0 {
		return 0, 0, false
	}

	padding := int((h[2] >> 1) & 0x01)

	// MPEG1 Layer III carries 1152 samples per frame, MPEG2 and 2.5 carry 576 - which is also
	// why the byte formula differs. Using the MPEG1 constants on an MPEG2 file yields frames of
	// twice the right length and a clip of noise.
	if version == 3 {
		length = 144*kbps*1000/sampleRate + padding
		ms = 1152.0 * 1000.0 / float64(sampleRate)
	} else {
		length = 72*kbps*1000/sampleRate + padding
		ms = 576.0 * 1000.0 / float64(sampleRate)
	}

	if length < 4 {
		return 0, 0, false
	}
	return length, ms, true
}

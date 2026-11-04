// Package preview cuts a short clip out of a song's audio without decoding it.
//
// See docs/ADR-009-preview-clips.md for why there is no decoder here. The short version: the
// runtime image is distroless-static, so ffmpeg would mean replacing the base image for 60-100 MB,
// and 98% of this library's 22,091 stem files are Ogg-family, which cannot be range-served from
// the middle because the codec and sample rate live in header pages at the start of the file.
//
// An Ogg stream is a row of self-describing pages. Playing audio means decoding pages; CUTTING
// audio means copying whole pages, which needs only the page headers. That is the whole idea.
package preview

import (
	"encoding/binary"
	"errors"
	"fmt"
	"io"
)

// ErrNotOgg is returned when the input does not begin with an Ogg page.
var ErrNotOgg = errors.New("preview: not an Ogg stream")

// ErrNoHeaders is returned when the stream carries no identification header, which means the
// sample rate is unknown and no granule position can be turned into a time.
var ErrNoHeaders = errors.New("preview: no identification header")

const (
	capturePattern = "OggS"
	headerBytes    = 27 // through the page_segments count
	offTypeBits    = 5
	offGranule     = 6
	offSerial      = 14
	offSeq         = 18
	offCRC         = 22
	offSegCount    = 26

	flagEOS = 0x04
)

// oggCRC is Ogg's own CRC-32: polynomial 0x04c11db7, MSB-first, zero init, no final xor and NO
// reflection. It is not any of the CRC-32s in hash/crc32 - crc32.IEEE is the same polynomial
// reflected, which produces different numbers and a stream every decoder rejects.
var oggCRC = func() [256]uint32 {
	var t [256]uint32
	for i := 0; i < 256; i++ {
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

func crcOf(page []byte) uint32 {
	var c uint32
	for i, b := range page {
		// The CRC field itself is treated as zero while computing it.
		if i >= offCRC && i < offCRC+4 {
			b = 0
		}
		c = (c << 8) ^ oggCRC[byte(c>>24)^b]
	}
	return c
}

// page is one Ogg page, held whole so it can be written back byte for byte.
type page struct {
	raw []byte
}

func (p page) typeBits() byte  { return p.raw[offTypeBits] }
func (p page) granule() int64  { return int64(binary.LittleEndian.Uint64(p.raw[offGranule:])) }
func (p page) serial() uint32  { return binary.LittleEndian.Uint32(p.raw[offSerial:]) }
func (p page) payload() []byte { return p.raw[headerBytes+int(p.raw[offSegCount]):] }

// setEOS, setSeq and setGranule all invalidate the CRC, so reseal must follow.
func (p page) setEOS()            { p.raw[offTypeBits] |= flagEOS }
func (p page) setSeq(n uint32)    { binary.LittleEndian.PutUint32(p.raw[offSeq:], n) }
func (p page) setGranule(g int64) { binary.LittleEndian.PutUint64(p.raw[offGranule:], uint64(g)) }
func (p page) reseal()            { binary.LittleEndian.PutUint32(p.raw[offCRC:], crcOf(p.raw)) }

// readPage reads exactly one page, or io.EOF at a clean stream end.
func readPage(r io.Reader) (page, error) {
	h := make([]byte, headerBytes)
	if _, err := io.ReadFull(r, h); err != nil {
		if err == io.ErrUnexpectedEOF {
			return page{}, io.EOF
		}
		return page{}, err
	}
	if string(h[:4]) != capturePattern {
		return page{}, ErrNotOgg
	}

	nseg := int(h[offSegCount])
	segs := make([]byte, nseg)
	if _, err := io.ReadFull(r, segs); err != nil {
		return page{}, fmt.Errorf("preview: short segment table: %w", err)
	}

	size := 0
	for _, s := range segs {
		size += int(s)
	}

	body := make([]byte, size)
	if _, err := io.ReadFull(r, body); err != nil {
		return page{}, fmt.Errorf("preview: short page payload: %w", err)
	}

	raw := make([]byte, 0, headerBytes+nseg+size)
	raw = append(raw, h...)
	raw = append(raw, segs...)
	raw = append(raw, body...)
	return page{raw: raw}, nil
}

// SampleRate reads the rate that granule positions are counted in.
//
// For Opus this is ALWAYS 48000 whatever the input rate was, because the Ogg mapping defines
// granule positions in 48 kHz units - reading OpusHead's own "input sample rate" field here
// would be a plausible way to get every Opus clip's timing wrong.
func sampleRateOf(firstPayload []byte) (int, error) {
	switch {
	case len(firstPayload) >= 16 && firstPayload[0] == 1 &&
		string(firstPayload[1:7]) == "vorbis":
		return int(binary.LittleEndian.Uint32(firstPayload[12:16])), nil
	case len(firstPayload) >= 8 && string(firstPayload[:8]) == "OpusHead":
		return 48000, nil
	}
	return 0, ErrNoHeaders
}

// CutOgg writes a standalone Ogg stream holding roughly durMS milliseconds of src, starting at
// startMS.
//
// It decodes nothing. The header pages are copied so the output stands alone, the body pages are
// selected by their granule positions, and only the page headers are rewritten - sequence
// numbers renumbered, granule positions rebased so the clip starts near zero, EOS set on the
// last page, and the CRC resealed on every page whose header was touched.
//
// Rebasing matters: without it a player reads the clip as a seek into a longer stream and reports
// a wrong duration.
func CutOgg(src io.Reader, startMS, durMS int64, dst io.Writer) error {
	if durMS <= 0 {
		return errors.New("preview: duration must be positive")
	}
	if startMS < 0 {
		startMS = 0
	}

	var (
		headers  []page
		rate     int
		seq      uint32
		written  int
		base     int64 = -1
		firstSet bool
	)

	// Pass one: the header pages. Their granule position is zero, which is how they are told
	// from audio without parsing any codec.
	for {
		p, err := readPage(src)
		if err == io.EOF {
			return ErrNoHeaders
		}
		if err != nil {
			return err
		}

		if !firstSet {
			r, err := sampleRateOf(p.payload())
			if err != nil {
				return err
			}
			if r <= 0 {
				return fmt.Errorf("preview: implausible sample rate %d", r)
			}
			rate, firstSet = r, true
		}

		if p.granule() == 0 {
			headers = append(headers, p)
			continue
		}

		// First audio page. Emit the headers, then fall through to handle this page.
		for _, h := range headers {
			h.setSeq(seq)
			seq++
			h.reseal()
			if _, err := dst.Write(h.raw); err != nil {
				return err
			}
		}

		startSamples := startMS * int64(rate) / 1000
		endSamples := startSamples + durMS*int64(rate)/1000

		// Pass two: skip to the start, then copy until long enough.
		for {
			if p.granule() >= startSamples {
				if base < 0 {
					base = p.granule() - (p.granule() - startSamples)
					if base < 0 {
						base = 0
					}
				}
				p.setSeq(seq)
				seq++
				p.setGranule(p.granule() - base)
				if p.granule() >= endSamples-base || p.granule() < 0 {
					p.setEOS()
					p.reseal()
					_, err := dst.Write(p.raw)
					return err
				}
				p.reseal()
				if _, err := dst.Write(p.raw); err != nil {
					return err
				}
				written++
			}

			p, err = readPage(src)
			if err == io.EOF {
				// The stream ended before the clip was long enough. What was written is
				// still a valid shorter clip, but the last page must carry EOS or a decoder
				// reads it as truncated - so this is reported rather than passed off as fine.
				if written == 0 {
					return fmt.Errorf("preview: no audio at or after %d ms", startMS)
				}
				return errShortStream
			}
			if err != nil {
				return err
			}
		}
	}
}

// errShortStream means the clip is valid but shorter than asked for, with no EOS flag written.
// Callers decide whether that is acceptable; it is not silently swallowed here.
var errShortStream = errors.New("preview: stream ended before the clip was complete")

// ErrShortStream is the exported form, for callers that want to accept a short clip.
func IsShortStream(err error) bool { return errors.Is(err, errShortStream) }

package chart

import (
	"encoding/binary"
	"errors"
	"fmt"
	"strings"
)

// A hand-rolled SMF scanner rather than a MIDI library, on purpose.
//
// Charts in the wild violate the MIDI specification in two documented ways: some
// do not reset running status after a SysEx or meta event, and many put a 0xFF
// byte inside SysEx events. A spec-correct parser hard-errors on files YARG
// plays perfectly well, so a strict library would reject real songs. This
// scanner is permissive about exactly those two things and about nothing else,
// and it skips a track it cannot follow rather than failing the file.
//
// It also does far less than a MIDI library: delta times are decoded only to
// find the next event boundary, never accumulated, and everything except
// Note-On and text meta events is discarded.

// noteSet is a bitmap of the 128 MIDI note numbers.
type noteSet [2]uint64

func (s *noteSet) add(n byte)     { s[n>>6] |= 1 << (n & 63) }
func (s noteSet) has(n byte) bool { return s[n>>6]&(1<<(n&63)) != 0 }
func (s noteSet) empty() bool     { return s[0] == 0 && s[1] == 0 }

func (s noteSet) anyIn(r noteRange) bool {
	for n := int(r.lo); n <= int(r.hi); n++ {
		if s.has(byte(n)) {
			return true
		}
	}
	return false
}

func (s noteSet) anyOf(ns []byte) bool {
	for _, n := range ns {
		if s.has(n) {
			return true
		}
	}
	return false
}

// midiTrackData is what one track contributes.
type midiTrackData struct {
	name  string
	notes noteSet
	// texts holds meta text events, needed for the ENHANCED_OPENS gate and for
	// telling a lyrics-only vocals track from an empty one.
	texts []string
	// truncated is set when the track could not be walked to its end. Its notes
	// are still usable - a partial answer beats discarding the track - but the
	// caller should know the reading is incomplete.
	truncated bool
}

// ErrNotSMF means the file does not begin with an MThd chunk.
var ErrNotSMF = errors.New("chart: not a standard MIDI file")

// scanMIDI walks an SMF and returns one entry per track.
func scanMIDI(data []byte) ([]midiTrackData, error) {
	if len(data) < 14 || string(data[0:4]) != "MThd" {
		return nil, ErrNotSMF
	}
	headerLen := binary.BigEndian.Uint32(data[4:8])
	pos := 8 + int(headerLen)
	if pos > len(data) || headerLen < 6 {
		return nil, fmt.Errorf("chart: bad MThd length %d", headerLen)
	}

	var out []midiTrackData
	for pos+8 <= len(data) {
		chunkID := string(data[pos : pos+4])
		chunkLen := int(binary.BigEndian.Uint32(data[pos+4 : pos+8]))
		pos += 8
		if chunkLen < 0 || pos+chunkLen > len(data) {
			// A truncated final chunk is common in the wild; take what is there
			// rather than discarding a whole file for a missing tail.
			chunkLen = len(data) - pos
		}
		body := data[pos : pos+chunkLen]
		pos += chunkLen

		if chunkID != "MTrk" {
			continue // unknown chunk types are skipped by the spec
		}
		out = append(out, scanTrack(body))
	}
	return out, nil
}

func scanTrack(b []byte) midiTrackData {
	var td midiTrackData
	i := 0
	var running byte

	for i < len(b) {
		// delta time - decoded to advance, never accumulated
		_, n, ok := readVLQ(b[i:])
		if !ok {
			td.truncated = true
			return td
		}
		i += n
		if i >= len(b) {
			td.truncated = true
			return td
		}

		status := b[i]
		if status&0x80 != 0 {
			i++
			// Running status is NOT cleared by SysEx or meta here. The
			// specification says it should be; real charts continue the prior
			// running status across them, and honouring the spec breaks those
			// files. This is the deliberate leniency.
			if status < 0xF0 {
				running = status
			}
		} else {
			if running == 0 {
				td.truncated = true
				return td
			}
			status = running
		}

		switch {
		case status == 0xFF: // meta
			if i >= len(b) {
				td.truncated = true
				return td
			}
			metaType := b[i]
			i++
			length, n, ok := readVLQ(b[i:])
			if !ok || i+n+length > len(b) {
				td.truncated = true
				return td
			}
			i += n
			payload := b[i : i+length]
			i += length
			// 0x01..0x0F are the text-ish metas: text, copyright, track name,
			// instrument, lyric, marker, cue point.
			if metaType >= 0x01 && metaType <= 0x0F {
				s := strings.TrimRight(string(payload), "\x00")
				if metaType == 0x03 {
					// The track NAME is not track content. Letting it into
					// texts made every vocals track look lyrics-only, because
					// "PART VOCALS" itself read as a sung word.
					if td.name == "" {
						td.name = strings.TrimSpace(s)
					}
				} else {
					td.texts = append(td.texts, s)
				}
			}
			if metaType == 0x2F { // end of track
				return td
			}

		case status == 0xF0 || status == 0xF7: // SysEx
			length, n, ok := readVLQ(b[i:])
			if !ok || i+n+length > len(b) {
				td.truncated = true
				return td
			}
			// A 0xFF byte inside the payload is barely spec-compliant and is
			// common in charts; skipping by declared length rather than
			// scanning for a terminator handles it without special-casing.
			i += n + length

		default:
			switch status & 0xF0 {
			case 0x90: // note on
				if i+1 >= len(b) {
					td.truncated = true
					return td
				}
				note, velocity := b[i], b[i+1]
				i += 2
				// Velocity zero is a note-off in disguise and is not evidence
				// that a note exists.
				if velocity > 0 {
					td.notes.add(note & 0x7F)
				}
			case 0x80, 0xA0, 0xB0, 0xE0: // 2-byte, all discarded
				i += 2
			case 0xC0, 0xD0: // 1-byte, discarded
				i++
			default:
				td.truncated = true
				return td
			}
		}
	}
	return td
}

// readVLQ decodes a MIDI variable-length quantity.
func readVLQ(b []byte) (value int, size int, ok bool) {
	for size < 4 && size < len(b) {
		c := b[size]
		value = value<<7 | int(c&0x7F)
		size++
		if c&0x80 == 0 {
			return value, size, true
		}
	}
	return 0, 0, false
}

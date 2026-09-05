package chart

import (
	"bufio"
	"bytes"
	"fmt"
	"strconv"
	"strings"
)

// .chart preparsing is a line scan. Each difficulty of each instrument is its
// own section, named [<Difficulty><Instrument>], so difficulty detection is
// reading a header rather than classifying note numbers.
//
// The one subtlety: notes AND modifiers both use the `N` type code, so a
// section is not "charted" merely because it contains N lines. Only the
// playable types count.

// chartSections maps an instrument suffix to the part it means.
//
// SingleBass is deliberately absent: the documentation describes it as a legacy
// track whose exact distinction is unclear and which was never used in a
// shipped game. So are the Feedback-editor-only suffixes, which were never
// implemented anywhere significant.
var chartSections = map[string]Part{
	"Single":       FiveFretGuitar,
	"DoubleGuitar": FiveFretCoopGuitar,
	"DoubleBass":   FiveFretBass,
	"DoubleRhythm": FiveFretRhythm,
	"Keyboard":     Keys,

	"GHLGuitar": SixFretGuitar,
	"GHLBass":   SixFretBass,
	"GHLRhythm": SixFretRhythm,
	"GHLCoop":   SixFretCoopGuitar,
	// GHLKeys appears in TheNathannator's list but not in FireFox's; there is
	// no 6-fret keys slot in the model, so it is recognised and skipped rather
	// than silently mapped onto something else.
}

var chartDifficulties = map[string]Difficulty{
	"Easy": Easy, "Medium": Medium, "Hard": Hard, "Expert": Expert,
}

// Playable `N` types. The excluded values are modifiers sharing the same type
// code: 5 is the strum/HOPO flip and 6 is tap on both fret families, and on
// drums 34-38 are accents, 40-44 ghosts and 66-68 cymbal toggles.
var (
	fiveFretNotes = map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 7: true}
	// Black-3 is type 8, not 5, because 5 was already the force flag.
	sixFretNotes = map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 7: true, 8: true}
	drumNotes    = map[int]bool{0: true, 1: true, 2: true, 3: true, 4: true, 5: true, 32: true}
	// Cymbal markers - note the polarity is INVERTED from .mid: in .chart
	// 4-lane notes are toms by default and cymbals are marked, whereas in .mid
	// they are cymbals by default and toms are marked.
	drumCymbalMarkers = map[int]bool{66: true, 67: true, 68: true}
	drumFiveLaneNote  = 5
)

// PreparseChart establishes which parts and difficulties a notes.chart contains.
func PreparseChart(data []byte) (*Result, error) {
	r := newResult()

	type drumHit struct {
		diffs    Mask
		cymbals  bool
		fiveLane bool
	}
	var drums drumHit

	var section string
	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 8*1024*1024)

	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || line == "{" || line == "}" {
			continue
		}
		if strings.HasPrefix(line, "[") && strings.HasSuffix(line, "]") {
			section = line[1 : len(line)-1]
			continue
		}
		if section == "" {
			continue
		}

		diff, part, ok := splitSection(section)
		if !ok {
			continue // [Song], [SyncTrack], [Events] and unknown sections
		}
		noteType, ok := parseNoteLine(line)
		if !ok {
			continue // S phrases, E events, tempo - never notes
		}

		switch {
		case section == "":
			// unreachable; kept for clarity of intent
		case part == FourLaneDrums:
			if drumNotes[noteType] {
				drums.diffs.Set(diff)
				if noteType == drumFiveLaneNote {
					drums.fiveLane = true
				}
			}
			if drumCymbalMarkers[noteType] {
				drums.cymbals = true
			}
		case isSixFret(part):
			if sixFretNotes[noteType] {
				r.add(part, diff)
			}
		default:
			if fiveFretNotes[noteType] {
				r.add(part, diff)
			}
		}
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("chart: read .chart: %w", err)
	}

	if drums.diffs.Any() {
		switch {
		case drums.cymbals:
			r.Difficulties[ProDrums] = drums.diffs
			r.Difficulties[FourLaneDrums] = drums.diffs
			if drums.fiveLane {
				r.note("drum sections have both cymbal markers and a 5-lane green note; read as Pro, per the documented preference")
			}
		case drums.fiveLane:
			r.Difficulties[FiveLaneDrums] = drums.diffs
		default:
			r.Difficulties[FourLaneDrums] = drums.diffs
		}
	}

	// Lyrics in .chart live in the global [Events] section with no pitch, so a
	// .chart can carry lyrics while having no vocals part. We deliberately do
	// not report vocals from them.
	r.note(".chart cannot express vocals, harmonies, pro keys, pro guitar or elite drums; absence of those here is a format limit, not a statement about the song")
	return r, nil
}

func splitSection(section string) (Difficulty, Part, bool) {
	for prefix, d := range chartDifficulties {
		if !strings.HasPrefix(section, prefix) {
			continue
		}
		suffix := section[len(prefix):]
		if suffix == "Drums" {
			return d, FourLaneDrums, true // resolved to a real type later
		}
		if p, ok := chartSections[suffix]; ok {
			return d, p, true
		}
		return 0, "", false
	}
	return 0, "", false
}

// parseNoteLine pulls the type out of `<Position> = N <Type> <Length>`, and
// only from N lines. S is a special phrase and E is a text event; neither is
// ever a note.
func parseNoteLine(line string) (int, bool) {
	eq := strings.IndexByte(line, '=')
	if eq < 0 {
		return 0, false
	}
	fields := strings.Fields(line[eq+1:])
	if len(fields) < 2 || fields[0] != "N" {
		return 0, false
	}
	n, err := strconv.Atoi(fields[1])
	if err != nil {
		return 0, false
	}
	return n, true
}

func isSixFret(p Part) bool {
	switch p {
	case SixFretGuitar, SixFretBass, SixFretRhythm, SixFretCoopGuitar:
		return true
	}
	return false
}

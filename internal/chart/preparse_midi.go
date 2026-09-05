package chart

import (
	"fmt"
	"strings"
)

// PreparseMIDI establishes which parts and difficulties a notes.mid contains.
func PreparseMIDI(data []byte) (*Result, error) {
	tracks, err := scanMIDI(data)
	if err != nil {
		return nil, err
	}
	r := newResult()

	// Duplicate track names happen in real files. Merging by name rather than
	// letting the last one win means a stray second PART GUITAR adds to the
	// picture instead of replacing it.
	merged := map[string]*midiTrackData{}
	var order []string
	for i := range tracks {
		t := &tracks[i]
		if t.name == "" {
			continue
		}
		if prev, ok := merged[t.name]; ok {
			prev.notes[0] |= t.notes[0]
			prev.notes[1] |= t.notes[1]
			prev.texts = append(prev.texts, t.texts...)
			prev.truncated = prev.truncated || t.truncated
			continue
		}
		cp := *t
		merged[t.name] = &cp
		order = append(order, t.name)
	}

	var drumTrack *midiTrackData
	harmSeen := map[int]bool{}

	for _, name := range order {
		t := merged[name]
		spec, known := lookupTrack(name)
		if !known {
			// The specification is explicit that unknown tracks are ignored.
			// Recording it is still worth doing: a chart carrying a part we do
			// not know about is information, not noise.
			if !t.notes.empty() {
				r.note(fmt.Sprintf("unrecognised track %q carries notes and was ignored", name))
			}
			continue
		}
		if t.truncated {
			r.note(fmt.Sprintf("track %q could not be read to its end; its parts may be understated", name))
		}

		switch spec.kind {
		case kindIgnore:
			// Named so that their absence from the result is a decision.
		case kindFiveFret:
			opens := hasEnhancedOpens(t.texts)
			for d := Easy; d <= Expert; d++ {
				if t.notes.anyIn(fiveFret[d]) || (opens && t.notes.has(fiveFretOpens[d])) {
					r.add(spec.part, d)
				}
			}
		case kindSixFret:
			for d := Easy; d <= Expert; d++ {
				if t.notes.anyIn(sixFret[d]) {
					r.add(spec.part, d)
				}
			}
		case kindProGuitar:
			for d := Easy; d <= Expert; d++ {
				if t.notes.anyIn(proGuitar[d]) {
					r.add(spec.part, d)
				}
			}
		case kindEliteDrums:
			for d := Easy; d <= Expert; d++ {
				if t.notes.anyIn(eliteDrums[d]) {
					r.add(spec.part, d)
				}
			}
		case kindProKeys:
			// Difficulty comes from the track name; the note test must be
			// strictly inside the playable range, because every Pro Keys track
			// is required to carry a range-shift marker at song start and would
			// otherwise always look present.
			if t.notes.anyIn(proKeysRange) {
				r.add(spec.part, spec.diff)
			}
		case kindVocals:
			if scanVocals(t) {
				r.add(spec.part, Expert) // vocals have no difficulty blocks
				if n, isHarm := harmTracks[name]; isHarm {
					harmSeen[n] = true
				}
			}
		case kindDrums:
			drumTrack = t
		}
	}

	if drumTrack != nil {
		classifyDrums(r, drumTrack)
	}
	finishHarmonies(r, harmSeen)
	deriveFromEliteDrums(r, drumTrack)
	return r, nil
}

func lookupTrack(name string) (trackSpec, bool) {
	if s, ok := midiTracks[name]; ok {
		return s, true
	}
	// Case is NOT documented as significant. Match exactly first, then fall
	// back to case-insensitive so an oddly-cased real chart still works.
	upper := strings.ToUpper(name)
	if s, ok := midiTracks[upper]; ok {
		return s, true
	}
	return trackSpec{}, false
}

// hasEnhancedOpens gates five-fret open notes. Without this event, note 59 is a
// left-hand animation note in GH2/RB charts, and counting it would invent an
// Easy difficulty on a large fraction of Rock Band-derived songs.
func hasEnhancedOpens(texts []string) bool {
	for _, s := range texts {
		t := strings.ToUpper(strings.Trim(strings.TrimSpace(s), "[]"))
		if t == "ENHANCED_OPENS" {
			return true
		}
	}
	return false
}

// scanVocals decides whether a vocals track carries a part.
//
// Presence is a disjunction, because charts may be lyrics-only with no pitch
// notes at all. Phrase markers (105/106) and percussion (96/97) are NOT
// evidence: a phrase marker with no pitch and no lyric is not a vocals part.
func scanVocals(t *midiTrackData) bool {
	if t.notes.anyIn(vocalsPitch) {
		return true
	}
	for _, s := range t.texts {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		// Bracketed entries are directives, not sung words.
		if strings.HasPrefix(s, "[") {
			continue
		}
		return true
	}
	return false
}

// classifyDrums decides which of the three drum types a PART DRUMS track is.
//
// There is no track name for this - 4-lane, Pro and 5-lane all share one track,
// and the documentation says the type "must be determined through heuristics".
func classifyDrums(r *Result, t *midiTrackData) {
	var found Mask
	for d := Easy; d <= Expert; d++ {
		if t.notes.anyIn(drums[d]) {
			found.Set(d)
		}
	}
	if !found.Any() {
		// A PART DRUMS track with no playable notes is animation data. The
		// Elite Drums spec is explicit that its mere presence must not be
		// treated as a drum part.
		if !t.notes.empty() {
			r.note("PART DRUMS carries no playable notes and was treated as animation data")
		}
		return
	}

	pro := t.notes.anyOf(drumTomMarkers)
	fiveLane := t.notes.anyOf(drumFiveLaneGreen)

	// Where both are detected the documentation says to prefer Pro.
	switch {
	case pro:
		r.Difficulties[ProDrums] = found
		r.Difficulties[FourLaneDrums] = found
		if fiveLane {
			r.note("drum track has both tom markers and a 5-lane green note; read as Pro, per the documented preference")
		}
	case fiveLane:
		r.Difficulties[FiveLaneDrums] = found
	default:
		r.Difficulties[FourLaneDrums] = found
	}
}

// finishHarmonies reports a harmony count of 0, 2 or 3.
//
// A lone HARM1 is the lead line, not a harmony arrangement - so one track does
// not make harmonies, and the HarmonyVocals part is dropped if that is all
// there was.
func finishHarmonies(r *Result, seen map[int]bool) {
	count := 0
	for n := 1; n <= 3; n++ {
		if seen[n] {
			count = n
		}
	}
	if count < 2 {
		delete(r.Difficulties, HarmonyVocals)
		r.HarmonyCount = 0
		if count == 1 {
			r.note("HARM1 present with no HARM2; treated as the lead line rather than a harmony part")
		}
		return
	}
	r.HarmonyCount = count
	if !seen[2] && seen[3] {
		r.note("HARM3 present without HARM2, which the documentation does not describe")
	}
}

// deriveFromEliteDrums records the parts the client will present even though
// they are not charted.
//
// The Elite Drums specification requires the game to downchart 4-lane, Pro and
// 5-lane from PART ELITE_DRUMS when PART DRUMS is absent or holds only
// animation data. Reporting "no drums" for such a song would be wrong in a way
// the player can see in the song browser.
func deriveFromEliteDrums(r *Result, drumTrack *midiTrackData) {
	if !r.Has(EliteDrums) {
		return
	}
	if r.Has(FourLaneDrums) || r.Has(ProDrums) || r.Has(FiveLaneDrums) {
		return
	}
	for _, p := range []Part{FourLaneDrums, ProDrums, FiveLaneDrums} {
		r.Difficulties[p] = r.Difficulties[EliteDrums]
		r.Derived[p] = true
	}
	r.note("4-lane, Pro and 5-lane drums derived from PART ELITE_DRUMS by downchart, as the specification requires")
}

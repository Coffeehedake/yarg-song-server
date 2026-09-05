package scan

import (
	"errors"
	"sort"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/chart"
	"github.com/coffeehedake/yarg-song-server/internal/songini"
)

// partSelectors maps a preparser part onto its slot in the catalog schema.
var partSelectors = map[chart.Part]func(*catalog.Parts) *catalog.PartValues{
	chart.FiveFretGuitar:     func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretGuitar },
	chart.FiveFretBass:       func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretBass },
	chart.FiveFretRhythm:     func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretRhythm },
	chart.FiveFretCoopGuitar: func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretCoopGuitar },
	chart.SixFretGuitar:      func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretGuitar },
	chart.SixFretBass:        func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretBass },
	chart.SixFretRhythm:      func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretRhythm },
	chart.SixFretCoopGuitar:  func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretCoopGuitar },
	chart.FourLaneDrums:      func(p *catalog.Parts) *catalog.PartValues { return &p.FourLaneDrums },
	chart.ProDrums:           func(p *catalog.Parts) *catalog.PartValues { return &p.ProDrums },
	chart.FiveLaneDrums:      func(p *catalog.Parts) *catalog.PartValues { return &p.FiveLaneDrums },
	chart.EliteDrums:         func(p *catalog.Parts) *catalog.PartValues { return &p.EliteDrums },
	chart.ProGuitar17:        func(p *catalog.Parts) *catalog.PartValues { return &p.ProGuitar17Fret },
	chart.ProGuitar22:        func(p *catalog.Parts) *catalog.PartValues { return &p.ProGuitar22Fret },
	chart.ProBass17:          func(p *catalog.Parts) *catalog.PartValues { return &p.ProBass17Fret },
	chart.ProBass22:          func(p *catalog.Parts) *catalog.PartValues { return &p.ProBass22Fret },
	chart.ProKeys:            func(p *catalog.Parts) *catalog.PartValues { return &p.ProKeys },
	chart.Keys:               func(p *catalog.Parts) *catalog.PartValues { return &p.Keys },
	chart.LeadVocals:         func(p *catalog.Parts) *catalog.PartValues { return &p.LeadVocals },
	chart.HarmonyVocals:      func(p *catalog.Parts) *catalog.PartValues { return &p.HarmonyVocals },
}

// applyPreparse folds a chart preparse into the song, leaving the diff_* based
// intensities alone.
//
// The chart is the sole authority on what EXISTS. song.ini's diff_* tags are
// difficulty ratings, not presence flags, so a part is never synthesised from a
// tag and never suppressed because its tag is missing.
func applyPreparse(s *catalog.Song, raw []byte, format catalog.ChartFormat, ini *songini.File) {
	var res *chart.Result
	var err error

	switch format {
	case catalog.FormatMid, catalog.FormatMidi:
		res, err = chart.PreparseMIDI(raw)
	case catalog.FormatChart:
		res, err = chart.PreparseChart(raw)
	default:
		// UltraStar. YARG rejects notes.txt charts whose title it cannot find,
		// by a rule we have not established - see docs/SOURCES.md. Deriving
		// parts from it would be guessing twice over.
		s.PartsNotes = append(s.PartsNotes,
			"UltraStar charts are not preparsed; instrument availability is unknown for this song")
		return
	}
	if err != nil {
		if errors.Is(err, chart.ErrNotSMF) {
			s.PartsNotes = append(s.PartsNotes,
				"chart file is not a standard MIDI file despite its name; instrument availability is unknown")
			return
		}
		s.PartsNotes = append(s.PartsNotes, "chart could not be preparsed: "+err.Error())
		return
	}

	applyDrumOverrides(res, ini)

	for part, mask := range res.Difficulties {
		sel, ok := partSelectors[part]
		if !ok {
			continue
		}
		sel(&s.Parts).Difficulties = uint8(mask)
	}
	for part := range res.Derived {
		s.DerivedParts = append(s.DerivedParts, string(part))
	}
	sort.Strings(s.DerivedParts)
	s.HarmonyCount = res.HarmonyCount
	s.PartsNotes = append(s.PartsNotes, res.Notes...)
	s.PartsDerived = true
}

// applyDrumOverrides lets song.ini settle the drum type.
//
// Drum type is the one thing the chart cannot state: 4-lane, Pro and 5-lane all
// share a single track, so it is a heuristic over marker notes. song.ini's
// pro_drums and five_lane_drums are explicit and win. Both true at once is
// documented as an invalid state, so it is recorded rather than resolved.
func applyDrumOverrides(res *chart.Result, ini *songini.File) {
	proSet, proOK := ini.Bool("pro_drums")
	if !proOK {
		proSet, proOK = ini.Bool("pro_drum") // older spelling
	}
	fiveSet, fiveOK := ini.Bool("five_lane_drums")

	if proOK && proSet && fiveOK && fiveSet {
		res.Notes = append(res.Notes,
			"song.ini sets both pro_drums and five_lane_drums, which is a documented invalid state; the chart's own markers were used instead")
		return
	}

	mask := res.Difficulties[chart.FourLaneDrums] |
		res.Difficulties[chart.ProDrums] |
		res.Difficulties[chart.FiveLaneDrums]
	if !mask.Any() {
		return
	}

	switch {
	case proOK && proSet:
		res.Difficulties[chart.ProDrums] = mask
		res.Difficulties[chart.FourLaneDrums] = mask
		delete(res.Difficulties, chart.FiveLaneDrums)
	case fiveOK && fiveSet:
		res.Difficulties[chart.FiveLaneDrums] = mask
		delete(res.Difficulties, chart.ProDrums)
		delete(res.Difficulties, chart.FourLaneDrums)
	}
}

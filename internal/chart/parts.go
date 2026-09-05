// Package chart preparses a song's chart to answer one question: which
// instruments and difficulties does it actually contain?
//
// It deliberately does NOT parse charts for play. No timing, no sustains, no
// HOPO inference, no star power. The browse UI needs an instrument grid; the
// client already has YARG.Core for everything else.
//
// Everything here follows docs/research/chart-preparsing.md, which is built
// from TheNathannator's Guitar Game Chart Formats, the RBN/C3 documentation,
// FireFox2000000's .chart specification and the Elite Drums spec. Where those
// disagree or are silent, the code says so rather than guessing.
package chart

// Difficulty is one of the four tiers. Dance calls its top tier "Challenge";
// we map it onto Expert rather than inventing a fifth.
type Difficulty uint8

const (
	Easy Difficulty = iota
	Medium
	Hard
	Expert
)

// Mask is a bitmask of difficulties, 1<<Difficulty, matching how YARG stores
// them.
type Mask uint8

func (m Mask) Has(d Difficulty) bool { return m&(1<<d) != 0 }
func (m *Mask) Set(d Difficulty)     { *m |= 1 << d }
func (m Mask) Any() bool             { return m != 0 }

// Part identifies an instrument slot.
type Part string

const (
	FiveFretGuitar     Part = "five_fret_guitar"
	FiveFretBass       Part = "five_fret_bass"
	FiveFretRhythm     Part = "five_fret_rhythm"
	FiveFretCoopGuitar Part = "five_fret_coop_guitar"

	SixFretGuitar     Part = "six_fret_guitar"
	SixFretBass       Part = "six_fret_bass"
	SixFretRhythm     Part = "six_fret_rhythm"
	SixFretCoopGuitar Part = "six_fret_coop_guitar"

	FourLaneDrums Part = "four_lane_drums"
	ProDrums      Part = "pro_drums"
	FiveLaneDrums Part = "five_lane_drums"
	EliteDrums    Part = "elite_drums"

	ProGuitar17 Part = "pro_guitar_17_fret"
	ProGuitar22 Part = "pro_guitar_22_fret"
	ProBass17   Part = "pro_bass_17_fret"
	ProBass22   Part = "pro_bass_22_fret"
	ProKeys     Part = "pro_keys"

	Keys Part = "keys"

	LeadVocals    Part = "lead_vocals"
	HarmonyVocals Part = "harmony_vocals"
)

// Result is what a preparse establishes.
type Result struct {
	// Difficulties maps a part to the difficulties found in the chart.
	//
	// Vocals have no difficulty blocks in any documented format, so a present
	// vocals part is reported with the Expert bit only - a placeholder meaning
	// "this part exists", not a claim that only Expert was charted.
	Difficulties map[Part]Mask

	// HarmonyCount is how many HARM tracks carry content: 0, 2 or 3. A lone
	// HARM1 is the lead line, not a harmony arrangement.
	HarmonyCount int

	// Derived lists parts that are NOT charted directly but which the client
	// will present anyway. The Elite Drums spec requires the game to downchart
	// 4-lane, Pro and 5-lane from PART ELITE_DRUMS when PART DRUMS is absent or
	// carries only animation data - so reporting "no drums" there would be
	// wrong in a way the player can see.
	Derived map[Part]bool

	// Notes records anything the preparser noticed but could not resolve, for
	// a human reading a catalog entry. Not an error channel.
	Notes []string
}

func newResult() *Result {
	return &Result{
		Difficulties: map[Part]Mask{},
		Derived:      map[Part]bool{},
	}
}

func (r *Result) add(p Part, d Difficulty) {
	m := r.Difficulties[p]
	m.Set(d)
	r.Difficulties[p] = m
}

func (r *Result) note(s string) { r.Notes = append(r.Notes, s) }

// Has reports whether a part was found at any difficulty.
func (r *Result) Has(p Part) bool { return r.Difficulties[p].Any() }

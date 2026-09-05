package chart

// The note maps. Every number here is cited in
// docs/research/chart-preparsing.md; the citations are summarised in comments
// because getting one wrong is invisible until a player sees a difficulty that
// does not exist.
//
// The governing rule: NEVER range-test a difficulty block. Every family has
// non-playable numbers inside its own blocks - force-strum and HOPO markers,
// open-note markers, star power, face-off phrases, animation notes - and a
// range test turns any of them into a phantom difficulty. Allow-list the
// playable numbers instead.

// noteRange is an inclusive span of playable note numbers for one difficulty.
type noteRange struct {
	lo, hi byte
}

func (r noteRange) contains(n byte) bool { return n >= r.lo && n <= r.hi }

// diffRanges is the playable notes for each of the four difficulties.
type diffRanges [4]noteRange

// fiveFret: lanes only. 59/71/83/95 are OPEN notes and are deliberately absent
// - note 59 is normally a left-hand animation note in GH2/RB tracks, and
// counting it unconditionally invents an Easy difficulty on a large fraction of
// Rock Band-derived charts. Opens are gated on ENHANCED_OPENS; see openNotes.
var fiveFret = diffRanges{
	Easy:   {60, 64},
	Medium: {72, 76},
	Hard:   {84, 88},
	Expert: {96, 100},
}

// fiveFretOpens are the open-note numbers, valid ONLY when the track carries an
// ENHANCED_OPENS text event.
var fiveFretOpens = [4]byte{Easy: 59, Medium: 71, Hard: 83, Expert: 95}

// sixFret blocks are nine wide and start a semitone lower than five-fret,
// because six lanes plus an open must fit. Opens need no gate here - the
// documentation is explicit that ENHANCED_OPENS is not required for 6-fret.
var sixFret = diffRanges{
	Easy:   {58, 64},
	Medium: {70, 76},
	Hard:   {82, 88},
	Expert: {94, 100},
}

// drums: kick plus four lanes, plus the 5-lane green at the top of each block.
// Expert deliberately starts at 96, not 95: note 95 is the Expert+ / 2x kick,
// which is opt-in and can appear on a companion track carrying little else, so
// it is not evidence of an Expert drum chart on its own.
var drums = diffRanges{
	Easy:   {60, 65},
	Medium: {72, 77},
	Hard:   {84, 89},
	Expert: {96, 101},
}

// Drum type is not signalled by track name - 4-lane, Pro and 5-lane all share
// PART DRUMS - so it is a heuristic over marker notes.
var (
	drumTomMarkers    = []byte{110, 111, 112} // present => Pro (4-lane with toms)
	drumFiveLaneGreen = []byte{65, 77, 89, 101}
)

// eliteDrums spans TWO octaves per difficulty: the lower octave holds gems, the
// upper holds modifiers (flam, indifferent hat, disco flip). Only the gems
// count - a disco-flip marker exists for downcharting and can sit on a
// difficulty with no gems at all.
var eliteDrums = diffRanges{
	Easy:   {0, 10},
	Medium: {24, 34},
	Hard:   {48, 58},
	Expert: {72, 82},
}

// proGuitar blocks are eleven wide, and only the first six are strings. Easy
// and Medium sit two and one octaves below the usual positions, which puts Pro
// Guitar's HARD block (72-82) exactly where a five-fret track's MEDIUM block
// lives - so this map must never be applied to a track chosen by anything but
// an exact name match.
var proGuitar = diffRanges{
	Easy:   {24, 29},
	Medium: {48, 53},
	Hard:   {72, 77},
	Expert: {96, 101},
}

// proKeys uses one MIDI track per difficulty, and all four use the SAME
// playable range. The bound matters: every Pro Keys track is required to carry
// a range-shift marker (notes 0/2/4/5/7/9) at song start, so a test that is not
// strictly inside 48-72 reports every track as present, including empty ones.
var proKeysRange = noteRange{48, 72}

// vocalsPitch is the sung-pitch range. 96/97 are percussion and 105/106 are
// phrase markers, all outside it. Charts may also be lyrics-only with no pitch
// notes at all, which is why presence is a disjunction - see scanVocals.
var vocalsPitch = noteRange{36, 84}

// midiTrack maps an exact track name to how it should be read.
//
// Matching is EXACT and whole-string. Prefix matching maps "PART GUITAR GHL"
// onto the five-fret map, whose blocks differ by a semitone, and produces
// confident nonsense.
type trackKind uint8

const (
	kindFiveFret trackKind = iota
	kindSixFret
	kindDrums
	kindEliteDrums
	kindProGuitar
	kindProKeys
	kindVocals
	kindIgnore
)

type trackSpec struct {
	kind trackKind
	part Part
	// diff is set for the per-difficulty Pro Keys tracks, which carry their
	// difficulty in the track name rather than in note numbers.
	diff Difficulty
}

var midiTracks = map[string]trackSpec{
	// 5-fret. T1 GEMS is Guitar Hero 1's lead track; the documentation says it
	// is safe to support the same way, though it puts star power and face-off
	// markers INSIDE each difficulty block - which the allow-list handles.
	"PART GUITAR":      {kind: kindFiveFret, part: FiveFretGuitar},
	"T1 GEMS":          {kind: kindFiveFret, part: FiveFretGuitar},
	"PART GUITAR COOP": {kind: kindFiveFret, part: FiveFretCoopGuitar},
	"PART BASS":        {kind: kindFiveFret, part: FiveFretBass},
	"PART RHYTHM":      {kind: kindFiveFret, part: FiveFretRhythm},
	"PART KEYS":        {kind: kindFiveFret, part: Keys},

	// 6-fret (Guitar Hero Live).
	"PART GUITAR GHL":      {kind: kindSixFret, part: SixFretGuitar},
	"PART BASS GHL":        {kind: kindSixFret, part: SixFretBass},
	"PART RHYTHM GHL":      {kind: kindSixFret, part: SixFretRhythm},
	"PART GUITAR COOP GHL": {kind: kindSixFret, part: SixFretCoopGuitar},

	// Drums. All three drum types share one track; PART DRUM is a FoFiX-era
	// spelling and PART REAL_DRUMS_PS carries the same notes as the standard
	// track, differing only in SysEx modifiers.
	"PART DRUMS":         {kind: kindDrums},
	"PART DRUM":          {kind: kindDrums},
	"PART REAL_DRUMS_PS": {kind: kindDrums},
	"PART ELITE_DRUMS":   {kind: kindEliteDrums, part: EliteDrums},

	// Pro Guitar and Bass. Separate tracks per fret count because the
	// controllers differ.
	"PART REAL_GUITAR":    {kind: kindProGuitar, part: ProGuitar17},
	"PART REAL_GUITAR_22": {kind: kindProGuitar, part: ProGuitar22},
	"PART REAL_BASS":      {kind: kindProGuitar, part: ProBass17},
	"PART REAL_BASS_22":   {kind: kindProGuitar, part: ProBass22},

	// Pro Keys: difficulty is in the NAME. The animation companions below use
	// the identical 48-72 note range and are distinguished only by name, which
	// is why "REAL_KEYS" must never be matched as a substring.
	"PART REAL_KEYS_X": {kind: kindProKeys, part: ProKeys, diff: Expert},
	"PART REAL_KEYS_H": {kind: kindProKeys, part: ProKeys, diff: Hard},
	"PART REAL_KEYS_M": {kind: kindProKeys, part: ProKeys, diff: Medium},
	"PART REAL_KEYS_E": {kind: kindProKeys, part: ProKeys, diff: Easy},

	// Vocals. HARM1-3 are the recommended names; the PART HARM* spellings come
	// from The Beatles: Rock Band.
	"PART VOCALS": {kind: kindVocals, part: LeadVocals},
	"HARM1":       {kind: kindVocals, part: HarmonyVocals},
	"HARM2":       {kind: kindVocals, part: HarmonyVocals},
	"HARM3":       {kind: kindVocals, part: HarmonyVocals},
	"PART HARM1":  {kind: kindVocals, part: HarmonyVocals},
	"PART HARM2":  {kind: kindVocals, part: HarmonyVocals},
	"PART HARM3":  {kind: kindVocals, part: HarmonyVocals},

	// Present, deliberately ignored. The animation and venue tracks matter
	// because naming them here documents that their absence from the results is
	// a decision, not an oversight.
	"EVENTS":                 {kind: kindIgnore},
	"BEAT":                   {kind: kindIgnore},
	"VENUE":                  {kind: kindIgnore},
	"ANIM":                   {kind: kindIgnore},
	"PART KEYS_ANIM_LH":      {kind: kindIgnore},
	"PART KEYS_ANIM_RH":      {kind: kindIgnore},
	"PART REAL_KEYS_ANIM_LH": {kind: kindIgnore},
	"PART REAL_KEYS_ANIM_RH": {kind: kindIgnore},
	// An alternate rendering of PART DRUMS, not a second drum part.
	"PART DRUMS_2X": {kind: kindIgnore},
}

// harmTracks are the harmony track names, for counting.
var harmTracks = map[string]int{
	"HARM1": 1, "HARM2": 2, "HARM3": 3,
	"PART HARM1": 1, "PART HARM2": 2, "PART HARM3": 3,
}

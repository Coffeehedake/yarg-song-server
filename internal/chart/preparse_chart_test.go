package chart

import "testing"

func preparseChart(t *testing.T, s string) *Result {
	t.Helper()
	r, err := PreparseChart([]byte(s))
	if err != nil {
		t.Fatalf("PreparseChart: %v", err)
	}
	return r
}

func TestChartSections(t *testing.T) {
	r := preparseChart(t, `
[Song]
{
  Name = "x"
}
[ExpertSingle]
{
  768 = N 0 0
}
[HardDoubleBass]
{
  768 = N 2 0
}
[ExpertGHLGuitar]
{
  768 = N 8 0
}
`)
	if !r.Difficulties[FiveFretGuitar].Has(Expert) {
		t.Error("ExpertSingle not detected")
	}
	if !r.Difficulties[FiveFretBass].Has(Hard) {
		t.Error("HardDoubleBass not detected")
	}
	// Black-3 is note type 8, not 5, because 5 was already the force flag.
	if !r.Difficulties[SixFretGuitar].Has(Expert) {
		t.Error("six-fret black-3 note not detected")
	}
	if r.Difficulties[FiveFretGuitar].Has(Hard) {
		t.Error("a difficulty was invented")
	}
}

// Notes AND modifiers both use the N type code. A section containing only
// modifiers, phrases and events is not a charted difficulty.
func TestChartModifiersAndEventsAreNotNotes(t *testing.T) {
	r := preparseChart(t, `
[ExpertSingle]
{
  768 = N 5 0
  768 = N 6 0
  768 = S 64 768
  768 = E solo
  1248 = E soloend
}
`)
	if r.Has(FiveFretGuitar) {
		t.Fatalf("force flag, tap, star power and text events created a part: %v", r.Difficulties)
	}
}

func TestChartDrumTypeHeuristics(t *testing.T) {
	// In .chart the polarity is INVERTED from .mid: notes are toms by default
	// and cymbals are marked, so a cymbal marker means Pro.
	pro := preparseChart(t, "[ExpertDrums]\n{\n768 = N 1 0\n768 = N 66 0\n}\n")
	if !pro.Has(ProDrums) || !pro.Has(FourLaneDrums) {
		t.Fatalf("cymbal marker did not yield Pro drums: %v", pro.Difficulties)
	}

	five := preparseChart(t, "[ExpertDrums]\n{\n768 = N 1 0\n768 = N 5 0\n}\n")
	if !five.Has(FiveLaneDrums) || five.Has(ProDrums) {
		t.Fatalf("5-lane green did not yield 5-lane drums: %v", five.Difficulties)
	}

	plain := preparseChart(t, "[ExpertDrums]\n{\n768 = N 1 0\n}\n")
	if !plain.Has(FourLaneDrums) || plain.Has(ProDrums) || plain.Has(FiveLaneDrums) {
		t.Fatalf("plain drums misclassified: %v", plain.Difficulties)
	}
}

// .chart lyrics live in the global [Events] section with no pitch, so a chart
// can carry lyrics while having no vocals part. Reporting vocals from them
// would promise the player an instrument that is not there.
func TestChartLyricsDoNotMakeAVocalsPart(t *testing.T) {
	r := preparseChart(t, `
[Events]
{
  768 = E "phrase_start"
  800 = E "lyric hel"
  832 = E "lyric lo"
}
[ExpertSingle]
{
  768 = N 0 0
}
`)
	if r.Has(LeadVocals) || r.Has(HarmonyVocals) {
		t.Fatalf("lyrics in [Events] created a vocals part: %v", r.Difficulties)
	}
}

func TestChartUnknownSectionsIgnored(t *testing.T) {
	r := preparseChart(t, `
[ExpertSingleBass]
{
  768 = N 0 0
}
[ExpertNonsense]
{
  768 = N 0 0
}
[SyncTrack]
{
  0 = B 120000
}
`)
	if len(r.Difficulties) != 0 {
		t.Fatalf("legacy and unknown sections produced parts: %v", r.Difficulties)
	}
}

// The format limits of .chart are a property of the format, not a statement
// about the song, and the result says so rather than leaving a reader to infer
// that a song has no vocals.
func TestChartRecordsItsOwnFormatLimits(t *testing.T) {
	r := preparseChart(t, "[ExpertSingle]\n{\n768 = N 0 0\n}\n")
	if len(r.Notes) == 0 {
		t.Fatal("no note explaining that .chart cannot express vocals or pro instruments")
	}
}

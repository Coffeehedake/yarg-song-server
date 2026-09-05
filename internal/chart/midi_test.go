package chart

import (
	"bytes"
	"encoding/binary"
	"testing"
)

// smf builds a minimal type-1 MIDI file from named tracks. Test fixture only.
type track struct {
	name  string
	notes []byte
	texts []string
	// raw, when set, is appended verbatim before end-of-track - for exercising
	// the specification violations real charts contain.
	raw []byte
}

func smf(tracks ...track) []byte {
	var out bytes.Buffer
	out.WriteString("MThd")
	_ = binary.Write(&out, binary.BigEndian, uint32(6))
	_ = binary.Write(&out, binary.BigEndian, uint16(1))
	_ = binary.Write(&out, binary.BigEndian, uint16(len(tracks)))
	_ = binary.Write(&out, binary.BigEndian, uint16(480))

	for _, t := range tracks {
		var b bytes.Buffer
		writeMeta(&b, 0x03, []byte(t.name))
		for _, s := range t.texts {
			writeMeta(&b, 0x01, []byte(s))
		}
		for _, n := range t.notes {
			b.WriteByte(0x00) // delta
			b.Write([]byte{0x90, n, 0x64})
			b.WriteByte(0x10)
			b.Write([]byte{0x80, n, 0x00})
		}
		b.Write(t.raw)
		b.WriteByte(0x00)
		b.Write([]byte{0xFF, 0x2F, 0x00})

		out.WriteString("MTrk")
		_ = binary.Write(&out, binary.BigEndian, uint32(b.Len()))
		out.Write(b.Bytes())
	}
	return out.Bytes()
}

func writeMeta(b *bytes.Buffer, kind byte, payload []byte) {
	b.WriteByte(0x00)
	b.Write([]byte{0xFF, kind, byte(len(payload))})
	b.Write(payload)
}

func preparse(t *testing.T, data []byte) *Result {
	t.Helper()
	r, err := PreparseMIDI(data)
	if err != nil {
		t.Fatalf("PreparseMIDI: %v", err)
	}
	return r
}

func TestFiveFretDifficulties(t *testing.T) {
	r := preparse(t, smf(track{name: "PART GUITAR", notes: []byte{60, 72, 96}}))
	m := r.Difficulties[FiveFretGuitar]
	for _, d := range []Difficulty{Easy, Medium, Expert} {
		if !m.Has(d) {
			t.Errorf("difficulty %d missing", d)
		}
	}
	if m.Has(Hard) {
		t.Error("Hard reported with no notes in its block")
	}
}

// Force-strum and force-HOPO markers live INSIDE the difficulty block. They
// modify notes; they are not notes. A range test reports a phantom Expert here.
func TestForceMarkersAloneAreNotADifficulty(t *testing.T) {
	r := preparse(t, smf(track{name: "PART GUITAR", notes: []byte{101, 102}}))
	if r.Has(FiveFretGuitar) {
		t.Fatalf("force markers alone created a part: %v", r.Difficulties)
	}
}

// Note 59 is a left-hand animation note in GH2/RB charts unless ENHANCED_OPENS
// says otherwise. Counting it unconditionally invents an Easy difficulty on a
// large fraction of Rock Band-derived songs.
func TestOpenNotesAreGatedOnEnhancedOpens(t *testing.T) {
	ungated := preparse(t, smf(track{name: "PART GUITAR", notes: []byte{59, 96}}))
	if ungated.Difficulties[FiveFretGuitar].Has(Easy) {
		t.Error("note 59 created an Easy difficulty with no ENHANCED_OPENS event")
	}
	if !ungated.Difficulties[FiveFretGuitar].Has(Expert) {
		t.Error("Expert was lost")
	}

	for _, form := range []string{"ENHANCED_OPENS", "[ENHANCED_OPENS]"} {
		gated := preparse(t, smf(track{name: "PART GUITAR", notes: []byte{59}, texts: []string{form}}))
		if !gated.Difficulties[FiveFretGuitar].Has(Easy) {
			t.Errorf("open note not counted with %s present", form)
		}
	}
}

// GHL blocks sit a semitone lower than five-fret. Prefix-matching the track
// name applies the wrong map and produces confident nonsense.
func TestTrackNamesMatchWholeStringNotPrefix(t *testing.T) {
	r := preparse(t, smf(track{name: "PART GUITAR GHL", notes: []byte{94}}))
	if r.Has(FiveFretGuitar) {
		t.Fatal("a GHL track was read as five-fret guitar")
	}
	if !r.Difficulties[SixFretGuitar].Has(Expert) {
		t.Fatalf("six-fret Expert not detected: %v", r.Difficulties)
	}
}

// Every Pro Keys track is required to carry a range-shift marker at song start,
// so a test that is not strictly inside 48-72 reports every track as present -
// including the empty ones.
func TestProKeysRangeShiftIsNotAPart(t *testing.T) {
	r := preparse(t, smf(track{name: "PART REAL_KEYS_H", notes: []byte{0, 2, 4}}))
	if r.Has(ProKeys) {
		t.Fatalf("range-shift markers alone created a Pro Keys part: %v", r.Difficulties)
	}
	r = preparse(t, smf(track{name: "PART REAL_KEYS_H", notes: []byte{0, 60}}))
	if !r.Difficulties[ProKeys].Has(Hard) {
		t.Fatal("a real Pro Keys note was not detected")
	}
	if r.Difficulties[ProKeys].Has(Expert) {
		t.Fatal("Pro Keys difficulty came from note numbers rather than the track name")
	}
}

// The animation tracks use the identical 48-72 range and are distinguished only
// by name, which is why "REAL_KEYS" must never be a substring match.
func TestProKeysAnimationTrackIsIgnored(t *testing.T) {
	for _, name := range []string{"PART KEYS_ANIM_LH", "PART REAL_KEYS_ANIM_RH"} {
		r := preparse(t, smf(track{name: name, notes: []byte{60, 61, 62}}))
		if r.Has(ProKeys) {
			t.Fatalf("%s was read as a playable Pro Keys part", name)
		}
	}
}

func TestDrumTypeHeuristics(t *testing.T) {
	pro := preparse(t, smf(track{name: "PART DRUMS", notes: []byte{96, 97, 110}}))
	if !pro.Has(ProDrums) || !pro.Has(FourLaneDrums) {
		t.Fatalf("tom marker did not yield Pro drums: %v", pro.Difficulties)
	}
	if pro.Has(FiveLaneDrums) {
		t.Error("5-lane reported without its green note")
	}

	five := preparse(t, smf(track{name: "PART DRUMS", notes: []byte{96, 101}}))
	if !five.Has(FiveLaneDrums) || five.Has(ProDrums) {
		t.Fatalf("5-lane green did not yield 5-lane drums: %v", five.Difficulties)
	}

	plain := preparse(t, smf(track{name: "PART DRUMS", notes: []byte{96, 97}}))
	if !plain.Has(FourLaneDrums) || plain.Has(ProDrums) || plain.Has(FiveLaneDrums) {
		t.Fatalf("plain drums misclassified: %v", plain.Difficulties)
	}
}

// Note 95 is the opt-in Expert+ / 2x kick and can appear on a companion track
// carrying little else. It is not evidence of an Expert drum chart.
func TestExpertPlusKickAloneIsNotExpertDrums(t *testing.T) {
	r := preparse(t, smf(track{name: "PART DRUMS", notes: []byte{95}}))
	if r.Difficulties[FourLaneDrums].Has(Expert) {
		t.Fatalf("note 95 alone created Expert drums: %v", r.Difficulties)
	}
}

// The Elite Drums spec is explicit: the mere presence of PART DRUMS must not
// count, only playable notes in it.
func TestAnimationOnlyDrumTrackIsNotAPart(t *testing.T) {
	// Rock Band drum animation occupies 24-51, well below the difficulty blocks.
	r := preparse(t, smf(track{name: "PART DRUMS", notes: []byte{24, 30, 51}}))
	if r.Has(FourLaneDrums) || r.Has(ProDrums) || r.Has(FiveLaneDrums) {
		t.Fatalf("animation-only drum track became a part: %v", r.Difficulties)
	}
	if len(r.Notes) == 0 {
		t.Error("the situation was not recorded for a human")
	}
}

func TestEliteDrumsDownchartsWhenDrumsAreAnimationOnly(t *testing.T) {
	r := preparse(t,
		smf(track{name: "PART ELITE_DRUMS", notes: []byte{75}}, // Expert snare
			track{name: "PART DRUMS", notes: []byte{24, 30}})) // animation only
	if !r.Difficulties[EliteDrums].Has(Expert) {
		t.Fatalf("elite drums not detected: %v", r.Difficulties)
	}
	for _, p := range []Part{FourLaneDrums, ProDrums, FiveLaneDrums} {
		if !r.Has(p) {
			t.Errorf("%s not derived; the client downcharts these and would show them", p)
		}
		if !r.Derived[p] {
			t.Errorf("%s was not marked as derived rather than charted", p)
		}
	}
}

// The modifier octave carries flam, indifferent-hat and disco-flip markers. The
// disco flip in particular exists for downcharting and can sit on a difficulty
// with no gems at all.
func TestEliteDrumsModifierOctaveIsNotAGem(t *testing.T) {
	r := preparse(t, smf(track{name: "PART ELITE_DRUMS", notes: []byte{87, 88, 90}}))
	if r.Has(EliteDrums) {
		t.Fatalf("modifier notes alone created an Elite Drums part: %v", r.Difficulties)
	}
}

func TestVocalsPresence(t *testing.T) {
	pitched := preparse(t, smf(track{name: "PART VOCALS", notes: []byte{60}}))
	if !pitched.Has(LeadVocals) {
		t.Error("pitched vocals not detected")
	}

	// Charts may be lyrics-only, with no pitch notes at all.
	lyricsOnly := preparse(t, smf(track{name: "PART VOCALS", texts: []string{"hel-", "lo"}}))
	if !lyricsOnly.Has(LeadVocals) {
		t.Error("lyrics-only vocals not detected")
	}

	// A phrase marker with no pitch and no lyric is not a vocals part.
	phraseOnly := preparse(t, smf(track{name: "PART VOCALS", notes: []byte{105, 116}}))
	if phraseOnly.Has(LeadVocals) {
		t.Errorf("phrase and star-power markers alone created a vocals part: %v", phraseOnly.Difficulties)
	}
}

// A lone HARM1 is the lead line, not a harmony arrangement.
func TestHarmonyCounting(t *testing.T) {
	one := preparse(t, smf(track{name: "HARM1", notes: []byte{60}}))
	if one.Has(HarmonyVocals) || one.HarmonyCount != 0 {
		t.Fatalf("HARM1 alone reported as harmonies: count=%d %v", one.HarmonyCount, one.Difficulties)
	}

	two := preparse(t, smf(
		track{name: "HARM1", notes: []byte{60}},
		track{name: "HARM2", notes: []byte{64}}))
	if !two.Has(HarmonyVocals) || two.HarmonyCount != 2 {
		t.Fatalf("two-part harmony not detected: count=%d", two.HarmonyCount)
	}

	three := preparse(t, smf(
		track{name: "HARM1", notes: []byte{60}},
		track{name: "HARM2", notes: []byte{64}},
		track{name: "HARM3", notes: []byte{67}}))
	if three.HarmonyCount != 3 {
		t.Fatalf("HarmonyCount = %d, want 3", three.HarmonyCount)
	}
}

// Charts commonly continue running status across SysEx and meta events, which
// the specification forbids and which breaks strict parsers. Ours must not be
// strict, because the game is not.
func TestRunningStatusSurvivesSysExAndMeta(t *testing.T) {
	var raw bytes.Buffer
	raw.WriteByte(0x00)
	raw.Write([]byte{0x90, 96, 0x64}) // establishes running status
	raw.WriteByte(0x00)
	raw.Write([]byte{0xF0, 0x04, 'P', 'S', 0x00, 0xFF}) // SysEx containing 0xFF
	raw.WriteByte(0x00)
	raw.Write([]byte{0xFF, 0x01, 0x03, 'a', 'b', 'c'}) // meta
	raw.WriteByte(0x00)
	raw.Write([]byte{84, 0x64}) // running status resumed: note 84 on

	r := preparse(t, smf(track{name: "PART GUITAR", raw: raw.Bytes()}))
	m := r.Difficulties[FiveFretGuitar]
	if !m.Has(Expert) {
		t.Error("note before the SysEx was lost")
	}
	if !m.Has(Hard) {
		t.Error("running status was not continued across SysEx and meta, as real charts require")
	}
}

func TestRejectsNonMIDI(t *testing.T) {
	if _, err := PreparseMIDI([]byte("not a midi file at all")); err == nil {
		t.Fatal("accepted a non-MIDI file")
	}
}

func TestTruncatedTrackKeepsWhatItRead(t *testing.T) {
	good := smf(track{name: "PART GUITAR", notes: []byte{96}})
	r, err := PreparseMIDI(good[:len(good)-3])
	if err != nil {
		t.Fatalf("a truncated file should still parse: %v", err)
	}
	if !r.Difficulties[FiveFretGuitar].Has(Expert) {
		t.Error("notes read before the truncation were discarded")
	}
}

package chart

import "testing"

const ultraStarSample = `#VERSION:1.0.0
#TITLE:Sample Song
#ARTIST:Sample Artist
#ALBUM:Sample Album
#BPM:120
#MP3:song.mp3
: 0 4 0 Hel~
: 4 4 2 lo
E
`

func TestUltraStarMetadataComesFromTheChart(t *testing.T) {
	r, us, err := PreparseUltraStar([]byte(ultraStarSample))
	if err != nil {
		t.Fatal(err)
	}
	if us.Title != "Sample Song" || us.Artist != "Sample Artist" || us.Album != "Sample Album" {
		t.Fatalf("metadata = %+v", us)
	}
	if !us.HasTitle {
		t.Error("HasTitle false with a title present")
	}
	if !r.Difficulties[LeadVocals].Has(Expert) {
		t.Error("lead vocals not reported")
	}
	if r.Has(FiveFretGuitar) || r.Has(FourLaneDrums) {
		t.Error("an instrumental part was derived from a vocals-only format")
	}
}

// TestUltraStarWithoutTitleStillHasItsNotes replaces an earlier test that
// asserted the opposite.
//
// That test required that NO vocals part be reported for a title-less chart,
// on the belief that YARG refuses such a song outright so its contents were
// moot. The oracle disproved the belief on 2026-09-05: packed into a .sng the
// song plays, because the packer writes the name into the archive metadata
// from song.ini. The test was not wrong about YARG's behaviour on a loose
// folder; it was wrong to conclude that a refused song has no notes.
//
// What a chart CONTAINS does not depend on whether it is titled.
func TestUltraStarWithoutTitleStillHasItsNotes(t *testing.T) {
	r, us, err := PreparseUltraStar([]byte("#ARTIST:Someone\n#BPM:120\n: 0 4 0 x\nE\n"))
	if err != nil {
		t.Fatal(err)
	}
	if us.HasTitle {
		t.Fatal("HasTitle true with no TITLE tag")
	}
	if !r.Has(LeadVocals) {
		t.Fatal("no vocals part reported for a title-less chart that has note lines; " +
			"it plays once packed, and reporting zero parts made it look empty")
	}
	if len(r.Notes) == 0 {
		t.Fatal("the missing-title caveat was not recorded")
	}
}

// TestUltraStarTitleDoesNotChangeDerivedParts is the property the bug above
// violated, stated directly: two charts that differ ONLY by their #TITLE line
// must derive identical parts.
//
// This is the shape of the corpus pair that exposed it — 20-ultrastar and
// 21-ultrastar-no-title are byte-identical apart from that one line, and the
// server reported vocals for one and nothing for the other.
func TestUltraStarTitleDoesNotChangeDerivedParts(t *testing.T) {
	const body = "#ARTIST:Corpus Artist\n#ALBUM:Corpus Album\n#BPM:120\n#GAP:0\n#MP3:song.ogg\n: 0 4 0 Hel~\n: 4 4 2 lo\n- 8\nE\n"

	titled, _, err := PreparseUltraStar([]byte("#VERSION:1.0.0\n#TITLE:A Real Title\n" + body))
	if err != nil {
		t.Fatal(err)
	}
	untitled, _, err := PreparseUltraStar([]byte("#VERSION:1.0.0\n" + body))
	if err != nil {
		t.Fatal(err)
	}

	for _, p := range []Part{LeadVocals, HarmonyVocals} {
		if got, want := untitled.Difficulties[p], titled.Difficulties[p]; got != want {
			t.Errorf("%v: untitled chart derived %v, titled twin derived %v; "+
				"the only difference between these two inputs is a #TITLE line", p, got, want)
		}
	}
}

func TestUltraStarHeaderSyntax(t *testing.T) {
	// Keys are case-insensitive and whitespace around both sides is ignored.
	_, us, err := PreparseUltraStar([]byte("#title :  Mixed Case Key  \n: 0 4 0 x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if us.Title != "Mixed Case Key" {
		t.Fatalf("title = %q", us.Title)
	}

	// The header ends at the first body element; a later #-line is body, not
	// header, and must not be read as a tag.
	_, us, err = PreparseUltraStar([]byte("#TITLE:Real\n: 0 4 0 x\n#TITLE:Later\n"))
	if err != nil {
		t.Fatal(err)
	}
	if us.Title != "Real" {
		t.Fatalf("title = %q; a tag after the body began was read", us.Title)
	}
}

func TestUltraStarHarmony(t *testing.T) {
	r, _, err := PreparseUltraStar([]byte("#TITLE:Duet\n#PARTS:2\n: 0 4 0 x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if !r.Has(HarmonyVocals) || r.HarmonyCount != 2 {
		t.Fatalf("PARTS:2 did not yield harmony: count=%d %v", r.HarmonyCount, r.Difficulties)
	}

	// The official spec names voices with #P1..#P9, but YARG keys harmony on
	// PARTS. We follow YARG and say so rather than reconciling silently.
	r, _, err = PreparseUltraStar([]byte("#TITLE:Duet\n#P1:Alice\n#P2:Bob\n: 0 4 0 x\n"))
	if err != nil {
		t.Fatal(err)
	}
	if r.Has(HarmonyVocals) {
		t.Error("harmony inferred from #P2, which YARG does not do")
	}
	found := false
	for _, n := range r.Notes {
		if len(n) > 0 && n[0] == 'U' {
			found = true
		}
	}
	if !found {
		t.Error("the #P2 discrepancy was not recorded")
	}
}

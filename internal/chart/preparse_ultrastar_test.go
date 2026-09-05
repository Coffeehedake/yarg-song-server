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

// This is the exact failure YARG reported on our corpus: it refuses an
// UltraStar chart with no TITLE, and the title comes from the .txt rather than
// from song.ini.
func TestUltraStarWithoutTitleIsRejectable(t *testing.T) {
	r, us, err := PreparseUltraStar([]byte("#ARTIST:Someone\n#BPM:120\n: 0 4 0 x\nE\n"))
	if err != nil {
		t.Fatal(err)
	}
	if us.HasTitle {
		t.Fatal("HasTitle true with no TITLE tag")
	}
	if r.Has(LeadVocals) {
		t.Fatal("a vocals part was reported for a chart YARG refuses")
	}
	if len(r.Notes) == 0 {
		t.Fatal("the rejection reason was not recorded")
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

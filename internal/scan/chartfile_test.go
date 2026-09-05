package scan

import (
	"strings"
	"testing"
)

func TestPickChartFilePriority(t *testing.T) {
	cases := []struct {
		name  string
		files []string
		want  ChartFormat
		ok    bool
	}{
		{"mid wins over chart", []string{"notes.chart", "notes.mid", "song.ini"}, FormatMid, true},
		{"midi wins over chart", []string{"notes.chart", "notes.midi"}, FormatMidi, true},
		{"chart alone", []string{"song.ini", "notes.chart", "guitar.ogg"}, FormatChart, true},
		{"ultrastar alone", []string{"notes.txt"}, FormatUltraStar, true},
		{"case insensitive", []string{"Notes.Mid"}, FormatMid, true},
		{"no chart", []string{"song.ini", "album.png"}, 0, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got, ok := PickChartFile(tc.files)
			if ok != tc.ok {
				t.Fatalf("ok = %v, want %v", ok, tc.ok)
			}
			if ok && got.Format != tc.want {
				t.Fatalf("format = %v, want %v", got.Format, tc.want)
			}
		})
	}
}

func TestHashChartIsSHA1OfBytes(t *testing.T) {
	// SHA-1 of "abc" - the value the client must independently arrive at.
	h, err := HashChart(strings.NewReader("abc"))
	if err != nil {
		t.Fatal(err)
	}
	const want = "a9993e364706816aba3e25717850c26c9cd0d89d"
	if h.String() != want {
		t.Fatalf("hash = %s, want %s", h, want)
	}
}

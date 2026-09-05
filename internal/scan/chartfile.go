// Package scan discovers songs on disk (or inside a .sng) and computes the
// identity YARG itself uses for them.
package scan

import (
	"crypto/sha1"
	"encoding/hex"
	"io"
	"strings"
)

// ChartFormat identifies which chart notation a song uses.
type ChartFormat uint8

const (
	FormatMid ChartFormat = iota
	FormatMidi
	FormatChart
	FormatUltraStar
)

func (f ChartFormat) String() string {
	switch f {
	case FormatMid:
		return "mid"
	case FormatMidi:
		return "midi"
	case FormatChart:
		return "chart"
	case FormatUltraStar:
		return "ultrastar"
	}
	return "unknown"
}

// ChartFile is one recognised chart filename and the format it implies.
type ChartFile struct {
	Filename string
	Format   ChartFormat
}

// ChartFileTypes mirrors YARG.Core's CHART_FILE_TYPES
// (YARG.Core/Song/Entries/Ini/SongEntry.IniBase.cs).
//
// ORDER IS LOAD-BEARING: first match wins. A folder containing both notes.mid
// and notes.chart is a notes.mid song, and hashing the wrong one gives an
// identity the client will never agree with.
var ChartFileTypes = []ChartFile{
	{"notes.mid", FormatMid},
	{"notes.midi", FormatMidi},
	{"notes.chart", FormatChart},
	{"notes.txt", FormatUltraStar},
}

// PickChartFile returns the chart file YARG would choose from the given set of
// filenames, matched case-insensitively. ok is false if none is present, which
// means the folder is not a song.
func PickChartFile(names []string) (chart ChartFile, ok bool) {
	present := make(map[string]struct{}, len(names))
	for _, n := range names {
		present[strings.ToLower(n)] = struct{}{}
	}
	for _, ct := range ChartFileTypes {
		if _, found := present[ct.Filename]; found {
			return ct, true
		}
	}
	return ChartFile{}, false
}

// ChartHash is YARG's song identity: SHA-1 over the raw bytes of the chart
// file, and nothing else.
//
// Deliberately NOT part of the hash:
//   - song.ini. Editing metadata does not change a song's identity, so two
//     entries with the same chart and different metadata collide. YARG models
//     this as hash -> []SongEntry and flags duplicates; so must we.
//   - the audio stems. Same chart with different or missing audio is the same
//     song by this definition. If you need package-level distinction, that is a
//     separate hash of our own, not this one.
//
// Upstream: HashWrapper (YARG.Core/Song/Entries/Types/HashWrapper.cs), computed
// in ScanChart (SongEntry.IniBase.cs).
type ChartHash [sha1.Size]byte

func (h ChartHash) String() string { return hex.EncodeToString(h[:]) }

// HashChart computes the chart hash by streaming r, which must yield exactly
// the chart file's bytes.
func HashChart(r io.Reader) (ChartHash, error) {
	sum := sha1.New()
	if _, err := io.Copy(sum, r); err != nil {
		return ChartHash{}, err
	}
	var h ChartHash
	copy(h[:], sum.Sum(nil))
	return h, nil
}

// Package catalog defines what the server stores and serves about a song.
//
// This is deliberately OUR schema, not YARG's. YARG's own songcache.bin is
// version-stamped, holds absolute local filesystem paths and rejects itself on
// any mismatch, so it can be neither produced nor consumed by an external tool.
// See docs/research/yarg-song-formats.md.
package catalog

import "time"

// ChartFormat is the notation the chart is written in.
type ChartFormat string

const (
	FormatMid       ChartFormat = "mid"
	FormatMidi      ChartFormat = "midi"
	FormatChart     ChartFormat = "chart"
	FormatUltraStar ChartFormat = "ultrastar"
)

// Song is one scanned song.
//
// The field set is the v1 scope from the research: everything YARG itself sorts
// by, plus identity, plus what a browse UI needs to render a row. Keys we do
// not model are NOT lost - RawMetadata carries every key as read, so a repack
// re-emits parse-tuning keys (hopo_frequency, five_lane_drums, star_power_note
// and friends) untouched. Dropping those would change how a chart plays.
type Song struct {
	// --- identity ---

	// ChartHash is SHA1 of the chart file bytes, hex encoded. This is YARG's
	// own identity for a song, so client and server agree exactly.
	//
	// It deliberately excludes song.ini and the audio, which means two songs
	// with the same chart and different metadata SHARE this hash. That is not a
	// bug to fix: upstream models it as hash -> []SongEntry and so must we.
	ChartHash string `json:"chart_hash"`

	// PackageHash distinguishes packages that share a chart but differ in audio
	// or art. It is SHA-256 over the sorted (lowercased filename, sha256 of
	// contents) pairs of every file in the package.
	//
	// This is OUR invention, not something YARG computes. Do not present it to
	// a client as if it were a YARG concept.
	PackageHash string `json:"package_hash"`

	ChartFormat ChartFormat `json:"chart_format"`
	ChartFile   string      `json:"chart_file"`

	// SourcePath is where this song was found, relative to the library root.
	// It is a scan-time convenience for humans reading a catalog dump; the
	// server must never hand a client a path from its own filesystem.
	SourcePath string `json:"source_path,omitempty"`

	// --- indexed: the twelve attributes YARG sorts by, plus year ---

	Name          string `json:"name"`
	Artist        string `json:"artist"`
	Album         string `json:"album"`
	Genre         string `json:"genre"`
	Subgenre      string `json:"subgenre,omitempty"`
	Charter       string `json:"charter,omitempty"`
	Source        string `json:"source,omitempty"`
	Playlist      string `json:"playlist,omitempty"`
	AlbumTrack    int    `json:"album_track,omitempty"`
	PlaylistTrack int    `json:"playlist_track,omitempty"`

	// Year is free text on purpose: charts carry "1994", ", 1994" and "1994?".
	// YearAsNumber is a derived view and is 0 when no year could be read.
	Year         string `json:"year,omitempty"`
	YearAsNumber int    `json:"year_as_number,omitempty"`

	SongLengthMS int64     `json:"song_length_ms,omitempty"`
	DateAdded    time.Time `json:"date_added"`

	// --- stored, surfaced, not indexed ---

	PreviewStartMS int64  `json:"preview_start_ms,omitempty"`
	PreviewEndMS   int64  `json:"preview_end_ms,omitempty"`
	DelayMS        int64  `json:"delay_ms,omitempty"`
	LoadingPhrase  string `json:"loading_phrase,omitempty"`
	Icon           string `json:"icon,omitempty"`
	Rating         int64  `json:"rating,omitempty"`
	VocalGender    string `json:"vocal_gender,omitempty"`
	CleanVocals    bool   `json:"clean_vocals,omitempty"`
	Modchart       bool   `json:"modchart,omitempty"`
	VideoStartMS   int64  `json:"video_start_ms,omitempty"`
	VideoEndMS     int64  `json:"video_end_ms,omitempty"`
	VideoLoop      bool   `json:"video_loop,omitempty"`

	// --- instruments ---

	Parts Parts `json:"parts"`

	// PartsDerived reports whether Parts.Difficulties was established by
	// reading the chart. In v1 it is always false: intensities come from the
	// diff_* keys, and which difficulties actually EXIST needs MIDI
	// preparsing, which is the last step of Phase 1. A client must not read an
	// empty difficulty mask as "no difficulties" while this is false.
	PartsDerived bool `json:"parts_derived"`

	// --- files ---

	Assets Assets `json:"assets"`

	// RawMetadata is every song.ini key as read, keys lowercased, values
	// untouched. It exists so a repack loses nothing.
	RawMetadata map[string]string `json:"raw_metadata,omitempty"`

	// UnknownKeys lists keys not in the recognised table. Informational: they
	// are preserved in RawMetadata regardless.
	UnknownKeys []string `json:"unknown_keys,omitempty"`
}

// PartValues is one instrument's state.
type PartValues struct {
	// Intensity is the charter's difficulty rating. -1 means UNKNOWN, which is
	// not the same as 0 ("trivially easy"). Upstream uses the same sentinel.
	Intensity int8 `json:"intensity"`
	// Difficulties is a bitmask of which difficulties exist, 1<<Difficulty.
	// Zero while PartsDerived is false means "not yet determined".
	Difficulties uint8 `json:"difficulties"`
}

// UnknownIntensity is the sentinel for "the chart did not say".
const UnknownIntensity int8 = -1

// Parts mirrors YARG.Core's AvailableParts. All 21 slots are present even when
// empty, so a client can render a consistent instrument grid.
type Parts struct {
	FiveFretGuitar     PartValues `json:"five_fret_guitar"`
	FiveFretBass       PartValues `json:"five_fret_bass"`
	FiveFretRhythm     PartValues `json:"five_fret_rhythm"`
	FiveFretCoopGuitar PartValues `json:"five_fret_coop_guitar"`

	SixFretGuitar     PartValues `json:"six_fret_guitar"`
	SixFretBass       PartValues `json:"six_fret_bass"`
	SixFretRhythm     PartValues `json:"six_fret_rhythm"`
	SixFretCoopGuitar PartValues `json:"six_fret_coop_guitar"`

	FourLaneDrums PartValues `json:"four_lane_drums"`
	ProDrums      PartValues `json:"pro_drums"`
	FiveLaneDrums PartValues `json:"five_lane_drums"`
	EliteDrums    PartValues `json:"elite_drums"`

	ProGuitar17Fret PartValues `json:"pro_guitar_17_fret"`
	ProGuitar22Fret PartValues `json:"pro_guitar_22_fret"`
	ProBass17Fret   PartValues `json:"pro_bass_17_fret"`
	ProBass22Fret   PartValues `json:"pro_bass_22_fret"`
	ProKeys         PartValues `json:"pro_keys"`

	Keys PartValues `json:"keys"`

	LeadVocals    PartValues `json:"lead_vocals"`
	HarmonyVocals PartValues `json:"harmony_vocals"`

	BandDifficulty PartValues `json:"band_difficulty"`
}

// NewParts returns Parts with every slot marked unknown rather than zero, so
// "the chart said easy" and "the chart said nothing" stay distinguishable.
func NewParts() Parts {
	u := PartValues{Intensity: UnknownIntensity}
	return Parts{
		FiveFretGuitar: u, FiveFretBass: u, FiveFretRhythm: u, FiveFretCoopGuitar: u,
		SixFretGuitar: u, SixFretBass: u, SixFretRhythm: u, SixFretCoopGuitar: u,
		FourLaneDrums: u, ProDrums: u, FiveLaneDrums: u, EliteDrums: u,
		ProGuitar17Fret: u, ProGuitar22Fret: u, ProBass17Fret: u, ProBass22Fret: u,
		ProKeys: u, Keys: u, LeadVocals: u, HarmonyVocals: u, BandDifficulty: u,
	}
}

// Asset is one file in the package.
type Asset struct {
	Name   string `json:"name"`
	Ext    string `json:"ext"`
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Assets is everything in the package that is not the chart itself.
type Assets struct {
	AlbumArt     *Asset  `json:"album_art,omitempty"`
	Background   *Asset  `json:"background,omitempty"`
	Video        *Asset  `json:"video,omitempty"`
	PreviewAudio *Asset  `json:"preview_audio,omitempty"`
	Stems        []Stem  `json:"stems,omitempty"`
	Other        []Asset `json:"other,omitempty"`
}

// Stem is one audio track, named by the stem it represents ("guitar",
// "drums_2", "vocals_clean") rather than by its filename.
type Stem struct {
	Asset
	// Stem is the recognised stem name, without extension.
	Stem string `json:"stem"`
	// Variant is "", "clean" or "explicit" - YARG's censored-audio toggle.
	Variant string `json:"variant,omitempty"`
}

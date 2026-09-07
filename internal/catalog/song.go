// Package catalog defines what the server stores and serves about a song.
//
// This is deliberately OUR schema, not YARG's.
//
// YARG's own songcache.bin is not encrypted or signed, so an external tool
// COULD read or write it. It must not, for three reasons, and only the third
// is about difficulty:
//
//  1. It is machine-specific. Song locations are written as absolute paths
//     (directory.FullName / info.FullName), so a cache built on a server says
//     nothing about where a client will put the files. This alone rules out
//     the one use that would justify the work - shipping a prebuilt cache to
//     clients so they start fast.
//  2. There is no stability contract. CACHE_VERSION is a date stamp
//     (26_09_04_00 at time of writing, changed 2026-09-04) checked on load
//     with NO compatibility window and no migration path: a mismatch logs
//     "Cache outdated", returns null, and the whole library is rescanned. The
//     field layout has no version of its own, and Serialize/Deserialize are
//     `internal` and `private protected` - nothing about the format is public
//     API, so it may change in any release without notice.
//  3. Failure is silent. Get any of it wrong and YARG simply rebuilds the
//     cache. The tool appears to work and accomplishes nothing.
//
// So the server ships its own catalog and lets the client scan normally. If
// fast client startup is ever wanted, the right shape is a remote-source
// provider inside YARG's own CacheHandler - a Phase 3 conversation with
// upstream, not a forged file. See docs/research/yarg-song-formats.md.
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

	Name     string `json:"name"`
	Artist   string `json:"artist"`
	Album    string `json:"album"`
	Genre    string `json:"genre"`
	Subgenre string `json:"subgenre,omitempty"`
	Charter  string `json:"charter,omitempty"`
	Source   string `json:"source,omitempty"`
	Playlist string `json:"playlist,omitempty"`
	// 16000 means "unnumbered" - YARG's own default, which sorts such songs to
	// the end of an album rather than the front. Not omitempty: a client must
	// be able to tell 16000 from a missing field.
	AlbumTrack    int `json:"album_track"`
	PlaylistTrack int `json:"playlist_track"`

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

	// License is the song.ini `credit_license` value, promoted out of the
	// generic credit keys because for a SERVER it is not a credit - it is the
	// answer to "may this be redistributed".
	//
	// YARN's submission guidelines require it for every Creative Commons and
	// royalty-free song, in the form "Released under CC BY-NC-SA 3.0. <link>"
	// or "Music provided by NoCopyrightSounds. <link>". A song whose audio is
	// redistributable says so here; one that says nothing is not thereby
	// permitted, only unlabelled.
	License      string `json:"license,omitempty"`
	Rating       int64  `json:"rating,omitempty"`
	VocalGender  string `json:"vocal_gender,omitempty"`
	CleanVocals  bool   `json:"clean_vocals,omitempty"`
	Modchart     bool   `json:"modchart,omitempty"`
	VideoStartMS int64  `json:"video_start_ms,omitempty"`
	VideoEndMS   int64  `json:"video_end_ms,omitempty"`
	VideoLoop    bool   `json:"video_loop,omitempty"`

	// --- instruments ---

	Parts Parts `json:"parts"`

	// PartsDerived reports whether Parts.Difficulties was established by
	// reading the chart itself. When false, an empty difficulty mask means
	// "not determined", not "no difficulties" - a client must not present the
	// two the same way.
	PartsDerived bool `json:"parts_derived"`

	// HarmonyCount is how many harmony vocal lines the chart carries: 0, 2 or
	// 3. A lone HARM1 track is the lead line, not a harmony arrangement, so it
	// reports 0.
	HarmonyCount int `json:"harmony_count,omitempty"`

	// DerivedParts names parts the chart does not contain but which the client
	// will present anyway - currently the 4-lane, Pro and 5-lane drum parts
	// that YARG downcharts from an Elite Drums track. Reporting them without
	// this distinction would claim they were charted; omitting them entirely
	// would tell a player a song has no drums when the game will show drums.
	DerivedParts []string `json:"derived_parts,omitempty"`

	// PartsNotes records what the preparser noticed but could not resolve - an
	// unrecognised track carrying notes, a truncated read, a drum track with
	// contradictory markers. Not errors; context for a human reading an entry.
	PartsNotes []string `json:"parts_notes,omitempty"`

	// --- files ---

	Assets Assets `json:"assets"`

	// RawMetadata is every song.ini key as read, keys lowercased, values
	// untouched. It exists so a repack loses nothing.
	RawMetadata map[string]string `json:"raw_metadata,omitempty"`

	// UnknownKeys lists keys not in the recognised table. Informational: they
	// are preserved in RawMetadata regardless.
	UnknownKeys []string `json:"unknown_keys,omitempty"`

	// Issues records reasons the real YARG client would refuse or mishandle
	// this package. We still catalog it - a server that silently drops content
	// is impossible to debug - but a server should not offer a song the client
	// cannot play without saying so.
	//
	// Every value here was established by scanning a corpus with YARG itself
	// and reading its badsongs report, not inferred from the source.
	Issues []Issue `json:"issues,omitempty"`
}

// Issue is one client-compatibility problem.
type Issue struct {
	Code   string `json:"code"`
	Detail string `json:"detail"`
}

// Issue codes. Each corresponds to an observed YARG behaviour.
const (
	// IssueNoAudio: YARG rejects a chart with no accompanying audio outright -
	// "No audio accompanying the chart file" in its badsongs report.
	IssueNoAudio = "no_audio"

	// IssueNoSongIni: a folder with a chart but no song.ini was neither cached
	// nor reported as bad by YARG - it was silently skipped. Recorded with
	// that uncertainty rather than asserted as a rejection.
	IssueNoSongIni = "no_song_ini"

	// IssueNoMetadataSection: a song.ini with no [Song] header. YARG reads
	// nothing from it and titles the song after its folder, so any metadata in
	// the file is invisible to the player.
	IssueNoMetadataSection = "no_metadata_section"

	// IssueNoNotes: the chart parsed cleanly and contains no playable notes on
	// any instrument at any difficulty. YARG rejects it - "No notes found" in
	// its badsongs report, measured against 13-mid-beats-chart on 2026-09-05,
	// a notes.mid holding a beat track and nothing else.
	//
	// Only ever raised when PartsDerived is true. Zero difficulties on a chart
	// we failed to parse means "not determined", and reporting that as "no
	// notes" would be asserting something we did not measure.
	IssueNoNotes = "no_notes"

	// IssueChartUnreadable: the chart file could not be parsed at all - a
	// notes.mid that is not a standard MIDI file, or an empty one. YARG
	// refuses these with "Corruption of either the ini file or chart/mid
	// file", measured 2026-09-07 against a plain text file named notes.mid.
	//
	// This is separate from IssueNoNotes on purpose. "We read the chart and it
	// has nothing to play" and "we could not read the chart" are different
	// statements, and the second must never be reported as the first - the
	// PartsDerived guard on IssueNoNotes exists precisely so it is not.
	IssueChartUnreadable = "chart_unreadable"

	// IssueChartTruncated: the chart file ends inside a chunk it declared. It
	// says it is longer than it is.
	//
	// This one matters more than it sounds. A real nine-track chart cut to a
	// third still has a valid header and several complete tracks, so it
	// preparses cleanly and reports real instruments - measured 2026-09-07:
	// parts found, no note, no issue, indexed as perfectly healthy while YARG
	// refuses it. A magic-byte check would not have caught it; only the file
	// contradicting its own chunk lengths does.
	//
	// It does NOT catch a cut landing exactly on a chunk boundary, which is
	// indistinguishable from a chart with fewer tracks.
	IssueChartTruncated = "chart_truncated"

	// IssueUltraStarNoTitle: an UltraStar notes.txt with no #TITLE tag. Unlike
	// every other format, UltraStar takes its title from the chart rather than
	// song.ini. As a LOOSE FOLDER YARG refuses it - "Name metadata not
	// provided". Packed into a .sng it PLAYS, because the packer writes the
	// name into the archive's metadata section from song.ini. Both measured
	// against the oracle; the second is why this is a note and not a refusal.
	IssueUltraStarNoTitle = "ultrastar_no_title"
)

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

// AnyPlayableNotes reports whether any instrument carries at least one
// difficulty.
//
// BandDifficulty is deliberately excluded: it is a band-wide rating, not an
// instrument anyone plays, so a song carrying only that would still be a song
// with nothing to play. It is listed here explicitly rather than skipped
// silently, because a future part added to the struct and forgotten here would
// make this quietly answer the wrong question.
func (p *Parts) AnyPlayableNotes() bool {
	for _, v := range []PartValues{
		p.FiveFretGuitar, p.FiveFretBass, p.FiveFretRhythm, p.FiveFretCoopGuitar,
		p.SixFretGuitar, p.SixFretBass, p.SixFretRhythm, p.SixFretCoopGuitar,
		p.FourLaneDrums, p.ProDrums, p.FiveLaneDrums, p.EliteDrums,
		p.ProGuitar17Fret, p.ProGuitar22Fret, p.ProBass17Fret, p.ProBass22Fret,
		p.ProKeys, p.Keys, p.LeadVocals, p.HarmonyVocals,
	} {
		if v.Difficulties != 0 {
			return true
		}
	}
	return false
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

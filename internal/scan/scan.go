package scan

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
	"github.com/coffeehedake/yarg-song-server/internal/songini"
)

// ErrNoChart means the directory or archive holds no recognised chart file, so
// it is not a song. This is an ordinary outcome when walking a library, not a
// failure worth aborting a scan over.
var ErrNoChart = errors.New("scan: no chart file")

// ScanDir scans a loose song folder. Metadata comes from song.ini inside it.
func ScanDir(fsys fs.FS) (*catalog.Song, error) {
	names, err := rootNames(fsys)
	if err != nil {
		return nil, err
	}
	var ini *songini.File
	var issues []catalog.Issue
	if raw, err := fs.ReadFile(fsys, findCaseInsensitive(names, "song.ini")); err == nil {
		ini = songini.Parse(raw)
		if !ini.SawSection {
			issues = append(issues, catalog.Issue{
				Code:   catalog.IssueNoMetadataSection,
				Detail: "song.ini has no [Song] header, so YARG reads no metadata from it and titles the song after its folder",
			})
		}
	} else {
		ini = songini.Parse(nil)
		issues = append(issues, catalog.Issue{
			Code:   catalog.IssueNoSongIni,
			Detail: "no song.ini; YARG neither cached this folder nor reported it as bad when measured, so treat it as skipped by the client",
		})
	}
	song, err := scan(fsys, names, ini)
	if err != nil {
		return nil, err
	}
	song.Issues = append(issues, song.Issues...)
	return song, nil
}

// ScanArchive scans a .sng.
//
// The only real difference from a folder is where the metadata lives: a .sng
// carries the song.ini key namespace in its header rather than as a contained
// file, so there is no song.ini to read. Everything after that is identical,
// which is the whole reason the archive is an fs.FS.
func ScanArchive(a *sng.Archive) (*catalog.Song, error) {
	ini := songini.FromMap(a.Metadata, a.MetaOrder)
	return scan(a, a.Names(), ini)
}

func rootNames(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("scan: read root: %w", err)
	}
	var names []string
	for _, e := range entries {
		if !e.IsDir() {
			names = append(names, e.Name())
		}
	}
	return names, nil
}

func findCaseInsensitive(names []string, want string) string {
	for _, n := range names {
		if strings.EqualFold(n, want) {
			return n
		}
	}
	return want
}

func scan(fsys fs.FS, names []string, ini *songini.File) (*catalog.Song, error) {
	chart, ok := PickChartFile(names)
	if !ok {
		return nil, ErrNoChart
	}
	chartName := findCaseInsensitive(names, chart.Filename)

	chartFile, err := fsys.Open(chartName)
	if err != nil {
		return nil, fmt.Errorf("scan: open chart %q: %w", chartName, err)
	}
	hash, err := HashChart(chartFile)
	_ = chartFile.Close()
	if err != nil {
		return nil, fmt.Errorf("scan: hash chart: %w", err)
	}

	s := &catalog.Song{
		ChartHash:   hash.String(),
		ChartFormat: catalog.ChartFormat(chart.Format.String()),
		ChartFile:   chartName,
		DateAdded:   time.Now().UTC(),
		Parts:       catalog.NewParts(),
		RawMetadata: ini.Values,
		UnknownKeys: ini.Unknown,
	}
	applyMetadata(s, ini)

	if err := collectAssets(s, fsys, names, chartName, ini); err != nil {
		return nil, err
	}
	pkg, err := packageHash(fsys, names)
	if err != nil {
		return nil, err
	}
	s.PackageHash = pkg

	// YARG refuses a chart with no audio outright - "No audio accompanying the
	// chart file". Measured, not inferred. We still catalog it, because a
	// server that silently drops content is impossible to debug, but a server
	// must not offer it as playable.
	if len(s.Assets.Stems) == 0 {
		s.Issues = append(s.Issues, catalog.Issue{
			Code:   catalog.IssueNoAudio,
			Detail: "no recognised audio stem; YARG rejects a chart with no accompanying audio",
		})
	}
	return s, nil
}

func applyMetadata(s *catalog.Song, ini *songini.File) {
	str := func(k string) string { v, _ := ini.String(k); return v }
	num := func(k string) int64 { v, _ := ini.Int(k); return v }
	flag := func(k string) bool { v, _ := ini.Bool(k); return v }

	s.Name = str("name")
	s.Artist = str("artist")
	s.Album = str("album")
	s.Genre = str("genre")
	s.Subgenre = str("sub_genre")
	// `frets` is a deprecated alias for `charter`, and the wiki is explicit
	// that the modern key wins outright rather than only filling a gap.
	s.Charter = str("charter")
	if s.Charter == "" {
		s.Charter = str("frets")
	}
	s.Playlist = str("playlist")

	// Source comes from "source", falling back to "icon" - charts in the wild
	// use whichever their authoring tool wrote.
	if v := str("source"); v != "" {
		s.Source = v
	} else {
		s.Source = str("icon")
	}
	s.Icon = str("icon")

	s.Year = str("year")
	if y, ok := ini.YearAsNumber(); ok {
		s.YearAsNumber = y
	}
	// album_track / playlist_track default to 16000 when absent, NOT 0. The
	// difference is visible to a user: sorting an album puts unnumbered songs
	// at the end, where 0 would put them first. Documented on the wiki; the
	// key table alone does not tell you this.
	//
	// `track` is a deprecated alias, and album_track wins when BOTH are
	// present - including when album_track is explicitly 0, which an
	// "if zero, fall back" reading would get wrong.
	s.AlbumTrack = trackNumber(ini, "album_track", "track")
	s.PlaylistTrack = trackNumber(ini, "playlist_track", "")

	s.SongLengthMS = num("song_length")
	s.DelayMS = num("delay")
	if start, end, ok := ini.Preview(); ok {
		s.PreviewStartMS, s.PreviewEndMS = start, end
	}
	s.VideoStartMS = num("video_start_time")
	s.VideoEndMS = num("video_end_time")
	s.VideoLoop = flag("video_loop")

	s.LoadingPhrase = str("loading_phrase")
	s.Rating = num("rating")
	// Stored RAW and not normalised: the wiki documents vocal_gender as an
	// integer enum (0=Female .. 4=Unspecified) while YARG.Core parses the key
	// as a String. Until that is settled, converting either way would be
	// inventing a fact.
	s.VocalGender = str("vocal_gender")
	s.CleanVocals = flag("clean_vocals")
	s.Modchart = flag("modchart")

	applyIntensities(s, ini)
}

// diffKeyToPart maps song.ini diff_* keys onto instrument slots.
//
// Most are unambiguous. The ones that are NOT are left out rather than guessed:
//
//   - diff_drums_real_ps and diff_keys_real_ps are Phase Shift era keys whose
//     exact slot has not been confirmed against upstream. Mapping them wrongly
//     would put a rating on the wrong instrument, which is worse than leaving
//     that instrument marked unknown - the sentinel says "we do not know",
//     which is true, whereas a wrong number says something false.
//   - five-lane drums has no diff_* key of its own; its presence is signalled
//     by the five_lane_drums boolean and confirmed by chart preparsing, which
//     is Phase 1 step 6.
//
// Settle these when the MIDI preparsers land and the real part list can be
// compared against what the ini claimed.
var diffKeyToPart = map[string]func(*catalog.Parts) *catalog.PartValues{
	"diff_band":            func(p *catalog.Parts) *catalog.PartValues { return &p.BandDifficulty },
	"diff_guitar":          func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretGuitar },
	"diff_bass":            func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretBass },
	"diff_rhythm":          func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretRhythm },
	"diff_guitar_coop":     func(p *catalog.Parts) *catalog.PartValues { return &p.FiveFretCoopGuitar },
	"diff_guitarghl":       func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretGuitar },
	"diff_bassghl":         func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretBass },
	"diff_rhythm_ghl":      func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretRhythm },
	"diff_guitar_coop_ghl": func(p *catalog.Parts) *catalog.PartValues { return &p.SixFretCoopGuitar },
	"diff_drums":           func(p *catalog.Parts) *catalog.PartValues { return &p.FourLaneDrums },
	"diff_drums_real":      func(p *catalog.Parts) *catalog.PartValues { return &p.ProDrums },
	"diff_elite_drums":     func(p *catalog.Parts) *catalog.PartValues { return &p.EliteDrums },
	"diff_guitar_real":     func(p *catalog.Parts) *catalog.PartValues { return &p.ProGuitar17Fret },
	"diff_guitar_real_22":  func(p *catalog.Parts) *catalog.PartValues { return &p.ProGuitar22Fret },
	"diff_bass_real":       func(p *catalog.Parts) *catalog.PartValues { return &p.ProBass17Fret },
	"diff_bass_real_22":    func(p *catalog.Parts) *catalog.PartValues { return &p.ProBass22Fret },
	"diff_keys":            func(p *catalog.Parts) *catalog.PartValues { return &p.Keys },
	"diff_keys_real":       func(p *catalog.Parts) *catalog.PartValues { return &p.ProKeys },
	"diff_vocals":          func(p *catalog.Parts) *catalog.PartValues { return &p.LeadVocals },
	"diff_vocals_harm":     func(p *catalog.Parts) *catalog.PartValues { return &p.HarmonyVocals },
}

// trackNumber resolves a track-order key, honouring its deprecated alias and
// YARG's 16000 default for "unnumbered".
func trackNumber(ini *songini.File, key, deprecated string) int {
	if v, ok := ini.Int(key); ok {
		return int(v)
	}
	if deprecated != "" {
		if v, ok := ini.Int(deprecated); ok {
			return int(v)
		}
	}
	return songini.NoTrackNumber
}

func applyIntensities(s *catalog.Song, ini *songini.File) {
	for key, sel := range diffKeyToPart {
		v, ok := ini.Int(key)
		if !ok {
			continue
		}
		// Clamp into int8. Charts carry -1 for "unknown" and occasionally
		// absurd values; a wrapped int8 would turn 200 into -56, which reads
		// as a valid unknown-ish rating and is worse than clamping.
		switch {
		case v < -1:
			v = -1
		case v > 127:
			v = 127
		}
		sel(&s.Parts).Intensity = int8(v)
	}
	// PartsDerived stays false: which difficulties actually exist needs MIDI
	// preparsing (Phase 1 step 6). Setting it true here would claim the empty
	// masks below are a measurement.
	s.PartsDerived = false
}

func collectAssets(s *catalog.Song, fsys fs.FS, names []string, chartName string, ini *songini.File) error {
	coverOverride, _ := ini.String("cover")

	claimed := map[string]bool{chartName: true}
	for _, n := range names {
		if strings.EqualFold(n, "song.ini") {
			claimed[n] = true
		}
	}

	take := func(n string) (*catalog.Asset, error) {
		a, err := assetFor(fsys, n)
		if err != nil {
			return nil, err
		}
		claimed[n] = true
		return a, nil
	}

	// Album art: the "cover" key wins if it names a file that exists, then the
	// conventional album.<ext>.
	if coverOverride != "" {
		if n := findCaseInsensitive(names, coverOverride); containsName(names, n) {
			a, err := take(n)
			if err != nil {
				return err
			}
			s.Assets.AlbumArt = a
		}
	}
	if s.Assets.AlbumArt == nil {
		if n, ok := findByBase(names, AlbumArtBase, ImageExtensions); ok {
			a, err := take(n)
			if err != nil {
				return err
			}
			s.Assets.AlbumArt = a
		}
	}

	// Background: yarground venue, then video, then image - the order YARG
	// checks in.
	if n := findCaseInsensitive(names, YargroundFile); containsName(names, n) {
		a, err := take(n)
		if err != nil {
			return err
		}
		s.Assets.Background = a
	}
	for _, base := range BackgroundNames {
		if s.Assets.Video == nil {
			if n, ok := findByBase(names, base, VideoExtensions); ok {
				a, err := take(n)
				if err != nil {
					return err
				}
				s.Assets.Video = a
			}
		}
		if s.Assets.Background == nil {
			if n, ok := findByBase(names, base, ImageExtensions); ok {
				a, err := take(n)
				if err != nil {
					return err
				}
				s.Assets.Background = a
			}
		}
	}

	// Preview clip.
	for _, base := range []string{PreviewBase, CleanPreviewBase} {
		if s.Assets.PreviewAudio != nil {
			break
		}
		if n, ok := findByBase(names, base, AudioExtensions); ok {
			a, err := take(n)
			if err != nil {
				return err
			}
			s.Assets.PreviewAudio = a
		}
	}

	// Stems.
	for _, n := range names {
		if claimed[n] {
			continue
		}
		stem, variant, ok := ClassifyStem(n)
		if !ok {
			continue
		}
		a, err := take(n)
		if err != nil {
			return err
		}
		s.Assets.Stems = append(s.Assets.Stems, catalog.Stem{
			Asset: *a, Stem: stem, Variant: string(variant),
		})
	}
	sort.Slice(s.Assets.Stems, func(i, j int) bool { return s.Assets.Stems[i].Stem < s.Assets.Stems[j].Stem })

	// Anything left over is carried, not discarded: a package may hold files a
	// newer YARG understands and this build does not.
	for _, n := range names {
		if claimed[n] {
			continue
		}
		a, err := assetFor(fsys, n)
		if err != nil {
			return err
		}
		s.Assets.Other = append(s.Assets.Other, *a)
	}
	return nil
}

func containsName(names []string, want string) bool {
	for _, n := range names {
		if n == want {
			return true
		}
	}
	return false
}

// findByBase looks for <base><ext> for each extension in order.
func findByBase(names []string, base string, exts []string) (string, bool) {
	for _, e := range exts {
		want := base + e
		for _, n := range names {
			if strings.EqualFold(n, want) {
				return n, true
			}
		}
	}
	return "", false
}

func assetFor(fsys fs.FS, name string) (*catalog.Asset, error) {
	f, err := fsys.Open(name)
	if err != nil {
		return nil, fmt.Errorf("scan: open %q: %w", name, err)
	}
	defer f.Close()

	sum := sha256.New()
	n, err := io.Copy(sum, f)
	if err != nil {
		return nil, fmt.Errorf("scan: hash %q: %w", name, err)
	}
	return &catalog.Asset{
		Name:   name,
		Ext:    strings.TrimPrefix(path.Ext(strings.ToLower(name)), "."),
		Size:   n,
		SHA256: hex.EncodeToString(sum.Sum(nil)),
	}, nil
}

// packageHash is SHA-256 over the sorted "<lowercased name>:<sha256>\n" lines
// of every file in the package.
//
// It exists because the chart hash deliberately ignores audio and art, so two
// packages carrying the same chart with different stems are the same song but
// not the same download. This is our own construction and has no counterpart in
// YARG - never present it to a client as a YARG concept.
func packageHash(fsys fs.FS, names []string) (string, error) {
	lines := make([]string, 0, len(names))
	for _, n := range names {
		a, err := assetFor(fsys, n)
		if err != nil {
			return "", err
		}
		lines = append(lines, strings.ToLower(n)+":"+a.SHA256+"\n")
	}
	sort.Strings(lines)
	sum := sha256.New()
	for _, l := range lines {
		sum.Write([]byte(l))
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

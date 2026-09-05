package scan

import (
	"path"
	"strings"
)

// The name tables YARG uses to decide what a file in a song folder IS.
//
// These orders and spellings are load-bearing. If the server picks a different
// file as "the" album art or "the" chart than the client does, the two disagree
// about the same song while both look correct in isolation.
//
// Upstream: YARG.Core/Song/Entries/Ini/SongEntry.IniBase.cs (IniAudio) and
// YARG.Core/Song/Entries/SongEntry.cs (image/video/background tables).

// AudioExtensions are the container formats YARG will decode.
var AudioExtensions = []string{".opus", ".ogg", ".mp3", ".wav", ".aiff"}

// ImageExtensions are what YARG decodes for album art. Note .tga, .psd and
// .pic: YARG carries an stb_image-derived decoder, Go's stdlib does not. We
// recognise all of them and only generate thumbnails for the ones we can read.
var ImageExtensions = []string{".png", ".jpg", ".jpeg", ".tga", ".bmp", ".psd", ".gif", ".pic"}

// VideoExtensions are background video containers.
var VideoExtensions = []string{".mp4", ".mov", ".webm"}

// StandardStems are the 14 ordinary audio tracks.
var StandardStems = []string{
	"song", "guitar", "bass", "rhythm", "keys",
	"vocals", "vocals_1", "vocals_2",
	"drums", "drums_1", "drums_2", "drums_3", "drums_4",
	"crowd",
}

// CleanStems and ExplicitStems implement YARG's censored-audio toggle. This is
// a genuine YARG extension - Clone Hero lineage tooling such as scan-chart does
// not know these names, so a library indexed by that tooling will be missing
// them.
var CleanStems = []string{"song_clean", "vocals_clean", "vocals_1_clean", "vocals_2_clean", "crowd_clean"}

var ExplicitStems = []string{"vocals_explicit", "vocals_1_explicit", "vocals_2_explicit", "crowd_explicit"}

// BackgroundNames are the base names checked for a background image or video.
var BackgroundNames = []string{"bg", "background", "video"}

// YargroundFile is a Unity AssetBundle venue. It can only be built by Unity, so
// it is carried through as an opaque blob and never inspected.
const YargroundFile = "bg.yarground"

// AlbumArtBase is the conventional album art name; the song.ini "cover" key
// overrides it and is checked first.
const AlbumArtBase = "album"

// PreviewBase and CleanPreviewBase are dedicated preview clips. When absent,
// YARG generates a preview from the main mix using the preview window.
const (
	PreviewBase      = "preview"
	CleanPreviewBase = "preview_clean"
)

// StemVariant classifies a recognised stem name.
type StemVariant string

const (
	VariantNormal   StemVariant = ""
	VariantClean    StemVariant = "clean"
	VariantExplicit StemVariant = "explicit"
)

var stemLookup = func() map[string]StemVariant {
	m := make(map[string]StemVariant, len(StandardStems)+len(CleanStems)+len(ExplicitStems))
	for _, s := range StandardStems {
		m[s] = VariantNormal
	}
	for _, s := range CleanStems {
		m[s] = VariantClean
	}
	for _, s := range ExplicitStems {
		m[s] = VariantExplicit
	}
	return m
}()

// ClassifyStem reports whether a filename is a recognised audio stem, and which
// variant it is. Matching is on the base name plus a supported audio extension.
func ClassifyStem(filename string) (stem string, variant StemVariant, ok bool) {
	ext := strings.ToLower(path.Ext(filename))
	if !contains(AudioExtensions, ext) {
		return "", "", false
	}
	base := strings.ToLower(strings.TrimSuffix(path.Base(filename), ext))
	v, found := stemLookup[base]
	if !found {
		return "", "", false
	}
	return base, v, true
}

// IsImage, IsVideo and IsAudio test a filename's extension.
func IsImage(filename string) bool { return contains(ImageExtensions, ext(filename)) }
func IsVideo(filename string) bool { return contains(VideoExtensions, ext(filename)) }
func IsAudio(filename string) bool { return contains(AudioExtensions, ext(filename)) }

func ext(filename string) string { return strings.ToLower(path.Ext(filename)) }

func contains(list []string, want string) bool {
	for _, s := range list {
		if s == want {
			return true
		}
	}
	return false
}

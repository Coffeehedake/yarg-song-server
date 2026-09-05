// Package songini reads the song.ini metadata that accompanies a YARG chart.
//
// The same key namespace is used by the .sng container's metadata section, so
// this package is shared by both scanners.
//
// Upstream reference: YARG.Core/IO/Ini/SongIniHandler.cs (the key table),
// YARG.Core/IO/Ini/YARGIniReader.cs (the reader) and
// YARG.Core/IO/Ini/IniModifierCollection.cs (storage semantics).
package songini

// Tier records whether YARG actually ACTS on a key today.
//
// This comes from the official wiki's compatibility column, which the source
// alone does not give you: a key can be in SONG_INI_OUTLINES - and so parsed -
// while the game does nothing with it yet. A server that presents a
// TierFuture key as meaningful is promising behaviour the client does not have.
type Tier uint8

const (
	// TierYARG: YARG uses this today.
	TierYARG Tier = iota
	// TierFuture: recognised, but not yet acted on by the game.
	TierFuture
	// TierDeprecated: an old name kept for compatibility. See the Aliases map -
	// the modern key wins when both are present.
	TierDeprecated
)

// Kind is the value type YARG parses a given key as. It matters because a
// server that re-emits song.ini must not silently change a value's shape, and
// because "1"/"true"/"True" are all the same boolean to YARG.
type Kind uint8

const (
	KindString Kind = iota
	KindBool
	KindInt16
	KindInt32
	KindInt64
	KindUint32
	// KindInt64Array is the "preview" key only: a start and end in one value.
	KindInt64Array
)

// Keys is every song.ini key YARG recognises, lowercased, with the type it is
// parsed as. Transcribed from SONG_INI_OUTLINES in SongIniHandler.cs.
//
// A key absent from this table is NOT an error: YARG ignores unknown keys, and
// so do we - but we preserve them verbatim on re-emit, because a chart may
// carry keys a newer YARG understands and we do not.
var Keys = map[string]Kind{
	// identity and display
	"name": KindString, "artist": KindString, "album": KindString,
	"genre": KindString, "sub_genre": KindString, "charter": KindString,
	"icon": KindString, "source": KindString, "covered_by": KindString,
	"loading_phrase": KindString, "location": KindString, "tags": KindString,
	"playlist": KindString, "sub_playlist": KindString,
	"venue_hint": KindString, "vocal_character_hint": KindString,
	"vocal_gender": KindString, "frets": KindString,
	// year is a STRING on purpose: charts carry "1994", ", 1994" and "1994?".
	// A numeric year is derived separately and must never replace the raw value.
	"year": KindString,

	// ordering
	"album_track": KindInt32, "track": KindInt32, "playlist_track": KindInt32,

	// timing
	"song_length": KindInt64, "delay": KindInt64,
	"preview":            KindInt64Array,
	"preview_start_time": KindInt64, "preview_end_time": KindInt64,
	"video_start_time": KindInt64, "video_end_time": KindInt64,

	// media
	"background": KindString, "video": KindString, "cover": KindString,
	"video_loop": KindBool,

	// difficulty intensities (-1 means unknown, not "easy")
	"diff_band": KindInt32, "diff_guitar": KindInt32, "diff_guitar_coop": KindInt32,
	"diff_guitar_coop_ghl": KindInt32, "diff_guitarghl": KindInt32,
	"diff_guitar_real": KindInt32, "diff_guitar_real_22": KindInt32,
	"diff_bass": KindInt32, "diff_bassghl": KindInt32,
	"diff_bass_real": KindInt32, "diff_bass_real_22": KindInt32,
	"diff_rhythm": KindInt32, "diff_rhythm_ghl": KindInt32,
	"diff_drums": KindInt32, "diff_drums_real": KindInt32, "diff_drums_real_ps": KindInt32,
	"diff_elite_drums": KindInt32,
	"diff_keys":        KindInt32, "diff_keys_real": KindInt32, "diff_keys_real_ps": KindInt32,
	"diff_vocals": KindInt32, "diff_vocals_harm": KindInt32,
	"diff_dance": KindInt32,

	// per-instrument charter credits
	"charter_audio": KindString, "charter_bass": KindString, "charter_bass_6f": KindString,
	"charter_drums": KindString, "charter_elite_drums": KindString,
	"charter_guitar": KindString, "charter_guitar_6f": KindString,
	"charter_keys": KindString, "charter_lower_diff": KindString,
	"charter_pro_bass": KindString, "charter_pro_keys": KindString,
	"charter_pro_guitar": KindString, "charter_rhythm": KindString,
	"charter_rhythm_6f": KindString, "charter_venue": KindString,
	"charter_vocals": KindString,

	// credits
	"credit_album_art_by": KindString, "credit_album_art_designed_by": KindString,
	"credit_album_cover": KindString, "credit_arranged_by": KindString,
	"credit_background": KindString, "credit_composed_by": KindString,
	"credit_courtesy_of": KindString, "credit_engineered_by": KindString,
	"credit_license": KindString, "credit_mastered_by": KindString,
	"credit_mixed_by": KindString, "credit_other": KindString,
	"credit_performed_by": KindString, "credit_produced_by": KindString,
	"credit_published_by": KindString, "credit_written_by": KindString,

	// links
	"link_bandcamp": KindString, "link_bluesky": KindString, "link_facebook": KindString,
	"link_instagram": KindString, "link_spotify": KindString, "link_twitter": KindString,
	"link_other": KindString, "link_youtube": KindString,

	// gameplay flags and parse tuning. These change how a chart PLAYS, so they
	// must survive a repack byte-for-byte even though we do not index them.
	"pro_drum": KindBool, "pro_drums": KindBool, "five_lane_drums": KindBool,
	"drum_fallback_blue": KindBool, "eighthnote_hopo": KindBool,
	"end_events": KindBool, "lyrics": KindBool, "modchart": KindBool,
	"clean_vocals": KindBool, "tutorial": KindBool,
	"hopo_frequency": KindInt64, "hopofreq": KindInt32,
	"sustain_cutoff_threshold": KindInt64,
	"multiplier_note":          KindInt32, "star_power_note": KindInt32,
	"tuning_offset_cents": KindInt16, "vocal_scroll_speed": KindInt16,

	// instrument type hints and tunings
	"bass_type": KindUint32, "guitar_type": KindUint32, "keys_type": KindUint32,
	"kit_type": KindUint32, "dance_type": KindUint32,
	"real_bass_tuning": KindUint32, "real_bass_22_tuning": KindUint32,
	"real_guitar_tuning": KindUint32, "real_guitar_22_tuning": KindUint32,
	"real_keys_lane_count_left": KindUint32, "real_keys_lane_count_right": KindUint32,

	// documented on the wiki, missed when this table was built from source alone
	"year_recorded": KindString, "year_released": KindString,
	"parts_vocals_harm":        KindInt32,
	"diff_guitar_coop_real":    KindInt32,
	"diff_guitar_coop_real_22": KindInt32,
	"diff_rhythm_real":         KindInt32,
	"diff_rhythm_real_22":      KindInt32,
	"link_newgrounds":          KindString,
	"link_soundcloud":          KindString,
	"link_tiktok":              KindString,

	// misc
	"rating": KindUint32, "count": KindUint32, "version": KindUint32,
	"unlock_completed": KindUint32, "unlock_id": KindString,
	"unlock_require": KindString, "unlock_text": KindString,
}

// Section is the section header YARG reads modifiers from. Comparison is
// lowercased, so [Song], [song] and [SONG] are the same section.
const Section = "song"

// Aliases maps a deprecated key to the modern one it stands in for.
//
// The wiki is explicit that the modern key WINS: "track is an old name for the
// album_track tag. This tag should be ignored by the game if album_track is
// present", and likewise frets for charter. That is stricter than "use the old
// one if the new one is missing or zero" - an explicit album_track of 0 must
// still beat a track of 5.
var Aliases = map[string]string{
	"track": "album_track",
	"frets": "charter",
}

// NoTrackNumber is the value YARG substitutes when album_track or
// playlist_track is absent.
//
// It is 16000, not 0, and the difference is visible: sorting an album puts
// unnumbered songs at the END, where 0 would put them first. Documented on the
// wiki; not discoverable from the key table.
const NoTrackNumber = 16000

// Rating values for the `rating` key, per the wiki. Stored as a number by the
// scanner; this is what the numbers mean.
const (
	RatingFamilyFriendly       = 1
	RatingSupervisionRecommend = 2
	RatingMatureContent        = 3
	RatingNotRated             = 4
	RatingSensitiveContent     = 5
)

// VocalGender values, per the wiki, which documents `vocal_gender` as an
// INTEGER enum.
//
// UNRESOLVED: YARG.Core's SONG_INI_OUTLINES parses this key as a String, and
// SongMetadata carries a VocalGender enum backed by a byte. The wiki and the
// source disagree about the on-disk shape, so the scanner stores the RAW value
// and does not normalise it. Settle this by reading the modifier's conversion
// before anything relies on the value.
const (
	VocalGenderFemale      = 0
	VocalGenderMale        = 1
	VocalGenderNonBinary   = 2
	VocalGenderOther       = 3
	VocalGenderUnspecified = 4
)

// TagCover is the value of the `tags` key that makes YARG show "As made famous
// by" beside the artist, marking an in-house cover rather than the original.
const TagCover = "cover"

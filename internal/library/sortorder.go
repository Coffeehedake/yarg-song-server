package library

import "strings"

// Attribute is one of the twelve values of upstream's SongAttribute enum -
// the complete set of things the client will order a song list by.
//
// The names are the wire values the HTTP API accepts.
type Attribute string

const (
	ByName        Attribute = "name"
	ByArtist      Attribute = "artist"
	ByAlbum       Attribute = "album"
	ByArtistAlbum Attribute = "artist_album"
	ByGenre       Attribute = "genre"
	BySubgenre    Attribute = "subgenre"
	ByYear        Attribute = "year"
	ByCharter     Attribute = "charter"
	ByPlaylist    Attribute = "playlist"
	BySource      Attribute = "source"
	BySongLength  Attribute = "song_length"
	ByDateAdded   Attribute = "date_added"
)

// Attributes is the twelve, in upstream's declaration order.
var Attributes = []Attribute{
	ByName, ByArtist, ByAlbum, ByArtistAlbum, ByGenre, BySubgenre,
	ByYear, ByCharter, ByPlaylist, BySource, BySongLength, ByDateAdded,
}

// ParseAttribute resolves a wire value, case-insensitively.
func ParseAttribute(s string) (Attribute, bool) {
	want := Attribute(strings.ToLower(strings.TrimSpace(s)))
	for _, a := range Attributes {
		if a == want {
			return a, true
		}
	}
	return "", false
}

// comparer orders two entries. Negative means a sorts first.
type comparer func(a, b *Entry) int

// metadataCompare is upstream's MetadataComparer: Name, then Artist, then
// Album, then Charter, then the entry's location.
//
// Upstream's final tie-breaker is SortBasedLocation, an absolute path on the
// player's own machine. That is exactly the kind of value this server must not
// order by - it would make the same library sort differently on two machines,
// and the server has no such path for a client anyway. PackageHash is used
// instead: stable, content-derived, identical everywhere, and unique per
// package by construction. This is a deliberate divergence, recorded in
// docs/ADR-002-v1-store.md rather than left to be rediscovered.
func metadataCompare(a, b *Entry) int {
	if c := a.Name.Compare(b.Name); c != 0 {
		return c
	}
	if c := a.Artist.Compare(b.Artist); c != 0 {
		return c
	}
	if c := a.Album.Compare(b.Album); c != 0 {
		return c
	}
	if c := a.Charter.Compare(b.Charter); c != 0 {
		return c
	}
	return strings.Compare(a.Song.PackageHash, b.Song.PackageHash)
}

// The comparers below reproduce upstream's chains, which are WITHIN-GROUP
// orderings: the client groups a browse list by the chosen attribute first and
// then orders inside each group, which is why ArtistComparer never compares
// Artist and AlbumComparer starts at AlbumTrack. A flat API has no grouping
// step, so each comparer here compares its own attribute first and then defers
// to upstream's chain - which produces the same sequence a player would see
// reading the client's grouped list top to bottom.
func comparerFor(attr Attribute) comparer {
	switch attr {
	case ByArtist:
		// Upstream: Name -> Album -> Charter -> location, within one artist.
		return func(a, b *Entry) int {
			if c := a.Artist.Compare(b.Artist); c != 0 {
				return c
			}
			if c := a.Name.Compare(b.Name); c != 0 {
				return c
			}
			if c := a.Album.Compare(b.Album); c != 0 {
				return c
			}
			if c := a.Charter.Compare(b.Charter); c != 0 {
				return c
			}
			return strings.Compare(a.Song.PackageHash, b.Song.PackageHash)
		}

	case ByAlbum:
		// Upstream: AlbumTrack -> Name -> Album -> Charter -> location. Track
		// number leads, which is why album_track defaulting to 16000 rather
		// than 0 matters: an unnumbered song belongs at the END of the album.
		return func(a, b *Entry) int {
			if c := a.Album.Compare(b.Album); c != 0 {
				return c
			}
			if c := cmpInt(a.Song.AlbumTrack, b.Song.AlbumTrack); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case ByArtistAlbum:
		return func(a, b *Entry) int {
			if c := a.Artist.Compare(b.Artist); c != 0 {
				return c
			}
			if c := a.Album.Compare(b.Album); c != 0 {
				return c
			}
			if c := cmpInt(a.Song.AlbumTrack, b.Song.AlbumTrack); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case ByCharter:
		// Upstream: AlbumTrack -> Name -> Album -> location. Note it does NOT
		// fall through to Charter, which is why this is not metadataCompare.
		return func(a, b *Entry) int {
			if c := a.Charter.Compare(b.Charter); c != 0 {
				return c
			}
			if c := cmpInt(a.Song.AlbumTrack, b.Song.AlbumTrack); c != 0 {
				return c
			}
			if c := a.Name.Compare(b.Name); c != 0 {
				return c
			}
			if c := a.Album.Compare(b.Album); c != 0 {
				return c
			}
			return strings.Compare(a.Song.PackageHash, b.Song.PackageHash)
		}

	case ByYear:
		// Upstream sorts on YearAsNumber and puts "no year" LAST, which it
		// represents as int.MaxValue. Our catalog uses 0 for "no year could be
		// read", so the sentinel is translated rather than compared raw -
		// comparing raw would file every undated chart first.
		return func(a, b *Entry) int {
			if c := cmpInt(yearOrLast(a), yearOrLast(b)); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case ByPlaylist:
		// Upstream: PlaylistTrack, then a Rock Band band-difficulty step that
		// only applies to RBCONEntry - content this server refuses on ingest -
		// then MetadataComparer.
		return func(a, b *Entry) int {
			if c := a.Playlist.Compare(b.Playlist); c != 0 {
				return c
			}
			if c := cmpInt(a.Song.PlaylistTrack, b.Song.PlaylistTrack); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case BySongLength:
		return func(a, b *Entry) int {
			if c := cmpInt64(a.Song.SongLengthMS, b.Song.SongLengthMS); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case ByDateAdded:
		// No comparer exists upstream for this attribute, so the tie-breaking
		// chain below is OURS. Said plainly rather than presented as parity.
		return func(a, b *Entry) int {
			if a.Song.DateAdded.Before(b.Song.DateAdded) {
				return -1
			}
			if a.Song.DateAdded.After(b.Song.DateAdded) {
				return 1
			}
			return metadataCompare(a, b)
		}

	case ByGenre:
		// Genre, Subgenre and Source have no comparer upstream either - they
		// are collected as grouping values, not ordered. Same caveat.
		return func(a, b *Entry) int {
			if c := a.Genre.Compare(b.Genre); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case BySubgenre:
		return func(a, b *Entry) int {
			if c := a.Subgenre.Compare(b.Subgenre); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	case BySource:
		return func(a, b *Entry) int {
			if c := a.Source.Compare(b.Source); c != 0 {
				return c
			}
			return metadataCompare(a, b)
		}

	default: // ByName, and anything unrecognised
		return metadataCompare
	}
}

// yearOrLast maps our "no year" (0) onto upstream's "sorts last" sentinel.
func yearOrLast(e *Entry) int {
	if e.Song.YearAsNumber == 0 {
		return int(^uint(0) >> 1) // max int, upstream's int.MaxValue
	}
	return e.Song.YearAsNumber
}

func cmpInt(a, b int) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

func cmpInt64(a, b int64) int {
	switch {
	case a < b:
		return -1
	case a > b:
		return 1
	}
	return 0
}

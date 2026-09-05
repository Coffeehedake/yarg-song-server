// Package sortkey reproduces YARG's SortString: the normalisation the client
// applies to a metadata field before it sorts or searches on it.
//
// Why a server needs this at all. The browse API orders results by the twelve
// attributes the client itself sorts by, and a server that orders them
// differently produces a list the player cannot recognise - "The Beatles"
// filed under T, or "Bjork" landing away from "Björk". No test of our own can
// catch that, because our own ordering is self-consistently wrong. Only
// agreement with the client is correct.
//
// This is one of the places docs/SOURCES.md lists as genuinely undocumented:
// there is no wiki page for sorting, so this was reproduced from YARG.Core.
//
//	SortString.cs             search = RemoveUnwantedWhitespace(RemoveDiacritics(StripRichTextTags(s)))
//	                          sort   = RemoveArticle(search)
//	                          group  = GetCharacterGrouping(sort)
//	                          compare: group difference first, then ORDINAL on sort
//	StringTransformations.cs  the three transforms, the article list, the grouping
//	RichTextUtils.cs          the 36 recognised rich-text tag names
//
// Three upstream behaviours are reproduced faithfully although each looks like
// a defect, because agreeing with the client is the entire point:
//
//   - Only uppercase "Æ" is expanded to "AE". The substitution list is ordinal,
//     so lowercase "æ" is left alone; it does not decompose under NFD either,
//     so it survives normalisation and lands in the non-ASCII group, sorting
//     nowhere near "ae".
//   - Comparison is by UTF-16 code unit, not by code point. See CompareStrings.
//   - The article list is scanned in declaration order and has "le " ahead of
//     "les ", which is harmless only because "les " cannot match once "le "
//     has failed - "les x" does not start with "le ".
package sortkey

import (
	"strings"
	"unicode"
	"unicode/utf16"
	"unicode/utf8"

	"golang.org/x/text/unicode/norm"
)

// CharGroup is upstream's CharacterGroup: the primary sort bucket, decided by
// the first character of the sort string. It exists so symbols, numbers and
// letters form blocks in a browse list instead of interleaving by code point.
//
// The values are upstream's declaration order and are compared numerically, so
// they must not be reordered.
type CharGroup int

const (
	GroupEmpty CharGroup = iota
	GroupASCIISymbol
	GroupASCIINumber
	GroupASCIILetter
	GroupNonASCII
)

// Key is one metadata value with the client's two derived forms.
//
// Search keeps the article ("the beatles") and is what a text query matches
// against. Sort drops it ("beatles") and is what ordering uses. Upstream keeps
// both for the same reason: a player searching "the beatles" should find the
// band, and a player browsing by artist should find it under B.
type Key struct {
	Original string
	Search   string
	Sort     string
	Group    CharGroup
}

// New builds a Key exactly as SortString's constructor does.
func New(s string) Key {
	search := RemoveUnwantedWhitespace(RemoveDiacritics(StripRichTextTags(s)))
	sortStr := RemoveArticle(search)
	return Key{Original: s, Search: search, Sort: sortStr, Group: GroupOf(sortStr)}
}

// Compare orders two keys the way the client does: group first, then an
// ordinal comparison of the sort string.
func (k Key) Compare(other Key) int {
	if k.Group != other.Group {
		return int(k.Group) - int(other.Group)
	}
	return CompareStrings(k.Sort, other.Sort)
}

// CompareStrings orders two strings by UTF-16 code unit, which is what C#'s
// string.CompareOrdinal does and what upstream calls.
//
// This is NOT the same as comparing Go strings with "<", and the difference is
// reachable. A character above U+FFFF is a surrogate pair in UTF-16, and
// surrogates occupy U+D800-U+DFFF - below U+E000-U+FFFF. So an emoji in a song
// title sorts BEFORE a U+E000-range character under the client's comparison and
// AFTER it under a byte-wise Go comparison. Song titles with emoji are not
// hypothetical, so this walks code units rather than bytes.
func CompareStrings(a, b string) int {
	ai, bi := utf16Iter{s: a, pending: -1}, utf16Iter{s: b, pending: -1}
	for {
		au, aok := ai.next()
		bu, bok := bi.next()
		switch {
		case !aok && !bok:
			return 0
		case !aok:
			return -1
		case !bok:
			return 1
		case au != bu:
			return int(au) - int(bu)
		}
	}
}

// utf16Iter yields a string's UTF-16 code units without allocating a slice.
type utf16Iter struct {
	s       string
	i       int
	pending rune // the low surrogate owed from the last supplementary rune, or -1
}

func (it *utf16Iter) next() (rune, bool) {
	if it.pending >= 0 {
		u := it.pending
		it.pending = -1
		return u, true
	}
	if it.i >= len(it.s) {
		return 0, false
	}
	r, size := utf8.DecodeRuneInString(it.s[it.i:])
	it.i += size
	if r > 0xFFFF {
		hi, lo := utf16.EncodeRune(r)
		it.pending = lo
		return hi, true
	}
	return r, true
}

// GroupOf reproduces GetCharacterGrouping, which switches on the first
// character of the sort string.
//
// Upstream's letter test is on a string that RemoveDiacritics has already
// lowercased, so whether it admits A-Z as well as a-z is unobservable in
// practice. Both are accepted here; the distinction is unreachable through New
// and is therefore NOT asserted against upstream anywhere.
func GroupOf(s string) CharGroup {
	if s == "" {
		return GroupEmpty
	}
	c := s[0]
	switch {
	case c >= 'a' && c <= 'z', c >= 'A' && c <= 'Z':
		return GroupASCIILetter
	case c >= '0' && c <= '9':
		return GroupASCIINumber
	case c > 127:
		// The lead byte of any multi-byte UTF-8 sequence is >= 0xC2, so a byte
		// test answers "is the first character non-ASCII" without decoding.
		return GroupNonASCII
	default:
		return GroupASCIISymbol
	}
}

// articles is upstream's list, in upstream's order. Order is load-bearing:
// RemoveArticle returns on the first match.
var articles = []string{"the ", "el ", "la ", "le ", "les ", "los "}

// RemoveArticle strips a leading article, case-insensitively.
//
// The comparison is deliberately ASCII-only rather than strings.EqualFold:
// upstream lowercases each source character with ToLowerInvariant and compares
// to a pre-lowercased literal, which is ASCII case folding. Full Unicode
// folding would match characters upstream does not.
func RemoveArticle(s string) string {
	if s == "" {
		return s
	}
	for _, a := range articles {
		if hasPrefixASCIIFold(s, a) {
			return s[len(a):]
		}
	}
	return s
}

func hasPrefixASCIIFold(s, prefix string) bool {
	if len(s) < len(prefix) {
		return false
	}
	for i := 0; i < len(prefix); i++ {
		c := s[i]
		if c >= 'A' && c <= 'Z' {
			c += 'a' - 'A'
		}
		if c != prefix[i] {
			return false
		}
	}
	return true
}

// RemoveDiacritics folds a value to its searchable form: expand "Æ", decompose,
// drop the marks, lowercase, recompose.
//
// The categories dropped are upstream's three - NonSpacingMark, Format and
// SpacingCombiningMark - which is why a zero-width joiner disappears along with
// an acute accent.
func RemoveDiacritics(s string) string {
	s = strings.ReplaceAll(s, "Æ", "AE")

	decomposed := norm.NFD.String(s)
	var b strings.Builder
	b.Grow(len(decomposed))
	for _, r := range decomposed {
		if unicode.Is(unicode.Mn, r) || unicode.Is(unicode.Cf, r) || unicode.Is(unicode.Mc, r) {
			continue
		}
		b.WriteRune(r)
	}
	return norm.NFC.String(strings.ToLower(b.String()))
}

// RemoveUnwantedWhitespace collapses every run of characters at or below U+0020
// into a single space, and drops such a run entirely at the start or end.
//
// This works on bytes because every UTF-8 byte at or below 0x20 is an ASCII
// character - continuation bytes are 0x80 and above - so a byte scan and
// upstream's UTF-16 code-unit scan agree on every input.
func RemoveUnwantedWhitespace(s string) string {
	buf := make([]byte, 0, len(s))
	i := 0
	for i < len(s) {
		c := s[i]
		i++
		if c > 32 {
			buf = append(buf, c)
			continue
		}
		for i < len(s) && s[i] <= 32 {
			i++
		}
		// Nothing emitted yet means this run was leading whitespace; reaching
		// the end means it was trailing. Either way it contributes nothing.
		if len(buf) > 0 && i < len(s) {
			buf = append(buf, ' ', s[i])
			i++
		}
	}
	return string(buf)
}

package songini

import (
	"strconv"
	"strings"
	"unicode/utf16"
	"unicode/utf8"
)

// File is a parsed song.ini.
//
// Values are kept as the raw strings they appeared as. Typed access goes
// through the accessors, which never mutate the stored text - a repack must be
// able to re-emit exactly what it read, including values we could not parse.
type File struct {
	// Values holds every key from the [song] section, keys lowercased.
	//
	// LAST VALUE WINS on a duplicate key, matching YARG: IniModifierCollection
	// stores modifiers with plain dictionary assignment, so a second occurrence
	// silently overwrites the first.
	Values map[string]string

	// Order records the key order as read, so a re-emit can preserve it.
	// Duplicates appear once, at the position of their FIRST occurrence.
	Order []string

	// Unknown lists keys not in the Keys table. Not an error - YARG ignores
	// them and so do we - but a repack must carry them through, since a chart
	// may use a key a newer YARG understands and this build does not.
	Unknown []string

	// SawSection reports whether a [song] header was found. A file with keys
	// but no header parses to nothing, because that is what YARG does; this
	// flag lets the caller say WHY the metadata is empty rather than leaving a
	// charter wondering where their title went.
	SawSection bool

	// Dropped lists text this reader deliberately threw away because YARG
	// cannot act on it either - the remainder of a line after a bare CR, and
	// a key YARG's own table could never match. Each entry is the text as it
	// appeared.
	//
	// It exists so the loss is visible. YARG discards the same text silently;
	// a server that did too would leave an operator with a song whose
	// playlist_track vanished and nothing anywhere saying why.
	Dropped []string `json:"dropped,omitempty"`
}

// cutAtLineBreak splits a value at the first CR or LF, returning the part YARG
// would read and the part it could not reach.
//
// This is the whole of YARG's bare-CR behaviour, read from
// YARG.Core/IO/TextReader/YARGTextReader.cs rather than assumed:
//
//   - A line ends at '\n' and ONLY at '\n'. TextConstants<TChar>.NEWLINE is
//     initialised from `int newline = '\n'` and nothing else, and GotoNextLine
//     searches for that constant alone. A bare CR - a Mac-classic line ending -
//     is therefore not a line break to YARG at all.
//   - A string value stops at the first CR regardless: ExtractText sets
//     stringEnd at the first '\r' it sees and keeps scanning to the '\n', so
//     the text between the CR and the newline is never part of any value.
//   - A numeric value stops there too, because TryExtract consumes leading
//     digits and halts at the first character that is not one.
//   - A key stops there as well: ExtractModifierName breaks on any character
//     <= WHITESPACE_LIMIT (32), and CR is 13.
//   - Whatever followed the CR is then unreachable, because the reader's loop
//     calls GotoNextLine before looking for another key - it jumps to the next
//     '\n' and starts there.
//
// So one physical line holding `a = 1\rb = 2` gives YARG a = 1 and loses b
// entirely. Cutting at the CR reproduces that, and it has the side effect of
// making it impossible for a song.ini to hand the .sng writer a value carrying
// a control character the format cannot express.
//
// It is applied to the VALUE, never to the whole line, and that distinction is
// a measurement rather than a reading. A CR on the KEY side of the '=' falls in
// the run that ExtractModifierName stops on and SkipWhitespaceAndEquals then
// skips, so YARG reads the pair regardless. Corpus case 26-barecr-before-equals
// exists to settle it: `album = BareCRGoodAlbum` followed by
// `album\r = BareCRBadAlbum` put BareCRBadAlbum in YARG v0.15.0's own
// songcache.bin and left BareCRGoodAlbum out of it entirely, on 2026-11-01.
// Cutting the line instead of the value dropped that pair and was a regression
// this reasoning would not have caught - only the oracle did.
func cutAtLineBreak(s string) (head, rest string) {
	i := strings.IndexAny(s, "\r\n")
	if i < 0 {
		return s, ""
	}
	return s[:i], strings.TrimSpace(s[i+1:])
}

// splitModifier reads one line the way YARG does, which is NOT by splitting on '='.
//
// YARGTextReader.ExtractModifierName takes the key to be the maximal run of characters
// ABOVE 32 that are not '=' - so the key ends at the first whitespace OR the first '='.
// SkipWhitespaceAndEquals then advances over every character that is whitespace (except a
// newline, which ends the line) or '=', and the value is whatever remains.
//
// Three divergences follow from that, and the old split-on-first-'=' reading got all three
// wrong. Measured against the live 4,085-song library on 2026-11-02 - 3,476 song.ini files
// found and 3,476 read, Marathon Hero 2 included - NONE of the three occurs even once:
//
//	A  "album track = 5"   YARG: key "album", value "track = 5".  Was: key "album track",
//	                       value "5". YARG shows the player a wrong ALBUM; we showed a key
//	                       the game never reads. 0 instances.
//	B  "genre Rock"        YARG reads a pair from a line with NO '=' at all. We skipped the
//	                       line entirely, so raw_metadata could never have revealed this -
//	                       it had to be measured on the raw files. 0 instances.
//	C  "name == Foo"       YARG skips BOTH '=' and reads "Foo". We kept the second one and
//	                       stored "= Foo". 0 instances.
//
// So this changes no song in the library today. That is the argument for doing it now rather
// than later: the code path runs for every song, and the behaviour it corrects is one nobody
// is currently relying on. The 4,085-song before/after comparison is what proves the first
// half of that claim.
//
// ok is false for a line that yields no key at all. A key is NOT rejected here for being
// unknown to YARG: upstream discards unknown keys at this point (YARGIniReader only keeps a
// name found in its outlines), and this reader deliberately keeps them, because the catalog
// reports them as `unknown_keys` and an operator finding a typo'd key is worth more than
// matching upstream's forgetting of it.
func splitModifier(line string) (key, val string, ok bool) {
	i := 0
	for i < len(line) && line[i] <= ' ' {
		i++
	}
	start := i
	for i < len(line) && line[i] > ' ' && line[i] != '=' {
		i++
	}
	if i == start {
		return "", "", false
	}
	key = strings.ToLower(line[start:i])

	for i < len(line) && (line[i] <= ' ' || line[i] == '=') {
		i++
	}
	return key, strings.TrimRight(line[i:], " \t"), true
}

// reservedInKey are the characters the .sng writer cannot put at the start of a
// re-emitted song.ini line (see sng.validateMeta). A key containing one is
// dropped rather than carried into metadata, and nothing is lost by that: YARG
// matches a key against SONG_INI_OUTLINES, no entry of which contains any of
// these, so such a key is read and then ignored by the game. Carrying it
// through would turn one stray character in one line into a song the server
// cannot pack at all.
//
// '\r' and '\n' are here for completeness; cutAtLineBreak has already removed
// them by the time a key is formed from a song.ini line.
const reservedInKey = "=;\r\n"

// Parse reads a song.ini from raw bytes.
//
// It is deliberately lenient, because charts in the wild are. It accepts UTF-8,
// UTF-16 with a BOM, and Latin-1; lines with no '='; values containing '='; and
// blank lines. It never returns an error: an unreadable line is skipped, because
// refusing a whole library over one malformed line is worse than ignoring the
// line, and that is what YARG does too.
//
// It is NOT lenient about the section header, and that is a correction. An
// earlier version read a song.ini with no [Song] line at all, on the reasoning
// that some charts omit it. Measured against YARG on 2026-09-05, that is wrong:
// given a headerless song.ini, YARG read NO metadata from it and fell back to
// the folder name for the title. Reading keys the client ignores is worse than
// reading nothing, because the server's catalog would then disagree with what
// the player sees on their own screen.
//
// VERIFIED against upstream, 2026-11-02. This used to split on the FIRST '='
// and trim both sides - "the conventional Clone Hero reading", recorded as an
// assumption rather than a measurement. YARGTextReader has now been read and
// the assumption was wrong in three separate ways; see splitModifier.
func Parse(raw []byte) *File {
	f := &File{Values: make(map[string]string)}

	inSection := false

	for _, line := range strings.Split(decodeText(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			// Trailing text after the closing bracket is ignored, and the name
			// is lowercased - YARG does `PeekLine(...).ToLower()`.
			name := line[1:]
			if i := strings.IndexByte(name, ']'); i >= 0 {
				name = name[:i]
			}
			if strings.EqualFold(strings.TrimSpace(name), Section) {
				inSection = true
				f.SawSection = true
			} else {
				inSection = false
			}
			continue
		}

		// Keys are read ONLY inside [song]. Before the first header, and
		// inside any other section, they are skipped - matching YARG, which
		// seeks to the next '[' rather than reading loose keys.
		if !inSection {
			continue
		}

		key, val, ok := splitModifier(line)
		if !ok {
			continue
		}
		// A bare CR inside the VALUE ends it, and whatever followed on this
		// physical line is beyond YARG's reach - see cutAtLineBreak. The cut
		// is on the value and not on the whole line because a CR on the KEY
		// side of the '=' is whitespace to YARG, which reads the pair anyway;
		// measured, see cutAtLineBreak's last paragraph.
		if head, rest := cutAtLineBreak(val); rest != "" {
			f.Dropped = append(f.Dropped, rest)
			val = head
		}
		// A key the .sng writer could not re-emit is dropped here instead of
		// being carried into metadata and refused there. YARG ignores such a
		// key too, so the song keeps every field the game can see - whereas
		// refusing it at the writer loses the whole song. See reservedInKey.
		//
		// NOT recorded in Dropped, and that changed with splitModifier. Dropped
		// means "text YARG can read and we cannot represent", and it is what
		// raises metadata_unreachable. A key can now only reach here by
		// containing ';' - every other reserved character ends the key instead
		// of entering it - and YARG reads such a name, finds it in no outline
		// and discards it. Nothing is unreachable, so nothing is reported.
		// Recording it would fire metadata_unreachable on every song.ini with a
		// ';' comment line, which is a large share of any real library.
		if strings.ContainsAny(key, reservedInKey) {
			continue
		}

		if _, seen := f.Values[key]; !seen {
			f.Order = append(f.Order, key)
			if _, known := Keys[key]; !known {
				f.Unknown = append(f.Unknown, key)
			}
		}
		f.Values[key] = val // last wins, as upstream does
	}
	return f
}

// String returns the raw value for a key, and whether it was present.
func (f *File) String(key string) (string, bool) {
	v, ok := f.Values[key]
	return v, ok
}

// Int returns a key parsed as a signed integer. ok is false if the key is
// absent or the value is not an integer - a malformed number is treated as
// absent rather than as zero, because zero is a meaningful value for most of
// these keys and "0" and "banana" must not look the same to a caller.
func (f *File) Int(key string) (v int64, ok bool) {
	s, present := f.Values[key]
	if !present {
		return 0, false
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, false
	}
	return n, true
}

// Bool returns a key parsed the lenient way charts are written: "1", "true"
// and "True" are all true; "0" and "false" are false.
func (f *File) Bool(key string) (v bool, ok bool) {
	s, present := f.Values[key]
	if !present {
		return false, false
	}
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "true", "yes":
		return true, true
	case "0", "false", "no":
		return false, true
	}
	return false, false
}

// Preview returns the start and end of the preview window in milliseconds.
//
// It reads the two-value "preview" key first, then falls back to the separate
// preview_start_time / preview_end_time keys. Both forms coexist in the wild.
func (f *File) Preview() (start, end int64, ok bool) {
	if s, present := f.Values["preview"]; present {
		fields := strings.FieldsFunc(s, func(r rune) bool {
			return r == ' ' || r == ',' || r == '\t'
		})
		if len(fields) >= 2 {
			a, err1 := strconv.ParseInt(fields[0], 10, 64)
			b, err2 := strconv.ParseInt(fields[1], 10, 64)
			if err1 == nil && err2 == nil {
				return a, b, true
			}
		}
	}
	s, sok := f.Int("preview_start_time")
	e, eok := f.Int("preview_end_time")
	if sok || eok {
		return s, e, true
	}
	return 0, 0, false
}

// YearAsNumber pulls a four-digit year out of the raw year string, which is
// free text: charts carry "1994", ", 1994" and "1994?". The raw value is left
// untouched; this is a derived view of it.
func (f *File) YearAsNumber() (int, bool) {
	s := f.Values["year"]
	for i := 0; i+4 <= len(s); i++ {
		chunk := s[i : i+4]
		if allDigits(chunk) {
			n, err := strconv.Atoi(chunk)
			if err == nil {
				return n, true
			}
		}
	}
	return 0, false
}

func allDigits(s string) bool {
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

// decodeText turns chart bytes into a Go string.
//
// Charts are not reliably UTF-8. This handles, in order: UTF-8 BOM, UTF-16
// LE/BE BOM, valid UTF-8, and finally Latin-1 as the fallback - which is what
// makes an accented artist name from a Windows-authored chart survive instead
// of turning into replacement characters.
func decodeText(b []byte) string {
	switch {
	case len(b) >= 3 && b[0] == 0xEF && b[1] == 0xBB && b[2] == 0xBF:
		return string(b[3:])
	case len(b) >= 2 && b[0] == 0xFF && b[1] == 0xFE:
		return decodeUTF16(b[2:], false)
	case len(b) >= 2 && b[0] == 0xFE && b[1] == 0xFF:
		return decodeUTF16(b[2:], true)
	case utf8.Valid(b):
		return string(b)
	default:
		// Latin-1: every byte is its own code point.
		runes := make([]rune, len(b))
		for i, c := range b {
			runes[i] = rune(c)
		}
		return string(runes)
	}
}

func decodeUTF16(b []byte, bigEndian bool) string {
	if len(b)%2 != 0 {
		b = b[:len(b)-1]
	}
	u := make([]uint16, len(b)/2)
	for i := range u {
		if bigEndian {
			u[i] = uint16(b[2*i])<<8 | uint16(b[2*i+1])
		} else {
			u[i] = uint16(b[2*i+1])<<8 | uint16(b[2*i])
		}
	}
	return string(utf16.Decode(u))
}

// FromMap builds a File from an already-parsed key/value set - the .sng
// metadata section, which carries the same key namespace as song.ini but is
// encoded in the container header rather than stored as a file.
//
// Keys are lowercased here. Upstream lowercases filenames inside a .sng but NOT
// metadata keys, so a .sng written with "Name" instead of "name" would miss
// every lookup if we did not normalise. Order is preserved for re-emit.
func FromMap(kv map[string]string, order []string) *File {
	f := &File{Values: make(map[string]string, len(kv)), SawSection: true}
	seen := make(map[string]bool, len(kv))

	add := func(k, v string) {
		lk := strings.ToLower(strings.TrimSpace(k))
		if lk == "" {
			return
		}
		// Same two rules as the song.ini path, for the same reason: one stray
		// character in one pair must not cost the whole song.
		//
		// This IS a deliberate divergence, and a narrow one. A .sng stores its
		// metadata length-prefixed, so YARG's reader does not tokenize it -
		// IniModifierCollection.AddSng decodes a String value whole, CR and
		// all, and only the numeric kinds stop at a non-digit. We truncate
		// anyway, because we REPACK: the archive the client downloads is the
		// one we wrote, so after truncation the catalog and the served file
		// agree with each other and with what the game reads out of it. The
		// alternative - carrying the CR through - would need the .sng writer's
		// guard relaxed to buy agreement we already have.
		//
		// Unlike the song.ini case this is reasoned, not measured: no .sng or
		// .yargsong carrying a line break in a metadata value has been
		// observed. It is here because the failure mode is identical and the
		// repack path reaches sng.Write the same way.
		head, rest := cutAtLineBreak(v)
		if rest != "" {
			f.Dropped = append(f.Dropped, rest)
		}
		v = head
		if strings.ContainsAny(lk, reservedInKey) {
			f.Dropped = append(f.Dropped, lk+" = "+v)
			return
		}
		if !seen[lk] {
			seen[lk] = true
			f.Order = append(f.Order, lk)
			if _, known := Keys[lk]; !known {
				f.Unknown = append(f.Unknown, lk)
			}
		}
		f.Values[lk] = v
	}

	for _, k := range order {
		if v, ok := kv[k]; ok {
			add(k, v)
		}
	}
	// Anything the order slice missed still gets in.
	for k, v := range kv {
		if !seen[strings.ToLower(strings.TrimSpace(k))] {
			add(k, v)
		}
	}
	return f
}

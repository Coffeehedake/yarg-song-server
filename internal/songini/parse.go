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
}

// Parse reads a song.ini from raw bytes.
//
// It is deliberately lenient, because charts in the wild are. It accepts UTF-8,
// UTF-16 with a BOM, and Latin-1; a missing or misspelled section header; lines
// with no '='; and blank lines. It never returns an error: an unreadable line is
// skipped, because refusing a whole library over one malformed line is worse
// than ignoring the line, and that is what YARG does too.
//
// UNVERIFIED against upstream: the exact whitespace and multiple-'=' handling
// lives in YARGTextReader.ExtractModifierName, which has not been read. The
// behaviour here - split on the FIRST '=', trim spaces and tabs from both sides
// - is the conventional Clone Hero reading and matches every chart tested, but
// it is an assumption, not a measurement. Settle it before shipping a writer.
func Parse(raw []byte) *File {
	f := &File{Values: make(map[string]string)}

	inSection := false
	sawAnySection := false

	for _, line := range strings.Split(decodeText(raw), "\n") {
		line = strings.TrimRight(line, "\r")
		line = strings.TrimSpace(line)
		if line == "" {
			continue
		}

		if strings.HasPrefix(line, "[") {
			sawAnySection = true
			// Trailing text after the closing bracket is ignored, and the name
			// is lowercased - YARG does `PeekLine(...).ToLower()`.
			name := line[1:]
			if i := strings.IndexByte(name, ']'); i >= 0 {
				name = name[:i]
			}
			inSection = strings.EqualFold(strings.TrimSpace(name), Section)
			continue
		}

		// A file with no section header at all is still worth reading: some
		// charts omit it. A file WITH sections is read only inside [song].
		if sawAnySection && !inSection {
			continue
		}

		eq := strings.IndexByte(line, '=')
		if eq < 0 {
			continue
		}
		key := strings.ToLower(strings.TrimSpace(line[:eq]))
		val := strings.TrimSpace(line[eq+1:])
		if key == "" {
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

package sortkey

import "strings"

// richTextTags is upstream's recognised tag set (RichTextUtils.cs), the same 36
// names SortString strips when it is called with no exclusion list.
//
// Matching is CASE-SENSITIVE upstream and is here too: "<B>" is left in the
// string. That looks wrong and is reproduced deliberately - a chart whose title
// carries "<B>" sorts under the symbol group in the client, and a server that
// tidied it would disagree.
var richTextTags = map[string]struct{}{
	"align": {}, "allcaps": {}, "alpha": {}, "b": {}, "br": {},
	"color": {}, "cspace": {}, "font": {}, "font-weight": {}, "gradient": {},
	"i": {}, "indent": {}, "line-height": {}, "line-indent": {}, "link": {},
	"lowercase": {}, "margin": {}, "mark": {}, "mspace": {}, "noparse": {},
	"nobr": {}, "page": {}, "pos": {}, "rotate": {}, "size": {},
	"smallcaps": {}, "space": {}, "sprite": {}, "s": {}, "style": {},
	"sub": {}, "sup": {}, "u": {}, "uppercase": {}, "voffset": {}, "width": {},
}

// StripRichTextTags removes Unity rich-text markup from a value.
//
// Charts do carry it: a charter styling a title with <color=#ff0000> would
// otherwise have every such song sort under "<". Only the three recognised
// shapes of a KNOWN tag are removed - <name>, <name=value> and </name>. An
// unrecognised angle-bracketed run is left exactly as it is, which is upstream's
// behaviour and matters because "<3" is a heart, not markup.
func StripRichTextTags(s string) string {
	if !strings.ContainsRune(s, '<') {
		return s
	}

	var b strings.Builder
	b.Grow(len(s))
	for i := 0; i < len(s); {
		if s[i] != '<' {
			b.WriteByte(s[i])
			i++
			continue
		}
		end := strings.IndexByte(s[i:], '>')
		if end < 0 {
			// An unterminated '<' is not a tag; the rest of the string is text.
			b.WriteString(s[i:])
			break
		}
		end += i
		if isRichTextTag(s[i+1 : end]) {
			i = end + 1
			continue
		}
		b.WriteByte('<')
		i++
	}
	return b.String()
}

// isRichTextTag reports whether the text between '<' and '>' names a tag
// upstream recognises, in any of its three shapes.
func isRichTextTag(inner string) bool {
	if inner == "" {
		return false
	}
	name := inner
	if name[0] == '/' {
		name = name[1:]
	} else if eq := strings.IndexByte(name, '='); eq >= 0 {
		name = name[:eq]
	}
	_, ok := richTextTags[name]
	return ok
}

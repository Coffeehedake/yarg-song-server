package config

// Writing a setting back into the operator's own config file.
//
// This is the half that makes a settings page something other than a trap. A
// toggle that takes effect and then quietly forgets at the next restart is
// worse than no toggle at all, because the operator has no reason to doubt it.
//
// The whole risk here is that this file BELONGS TO SOMEBODY. It is full of
// comments explaining what each setting costs, some of which they may have
// written. So the rule is narrow: change one line, leave every other byte
// exactly as it was, and never write a partial file.

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"
)

// setLine matches a setting line, commented out or not, capturing the key.
//
// The template ships every setting commented, so "# check_uploads = no" has to
// be recognised as being ABOUT check_uploads rather than as an opaque comment -
// otherwise a first write appends a line that visibly contradicts the one just
// above it.
var setLine = regexp.MustCompile(`^(\s*)(#\s*)?([a-z_]+)(\s*)=`)

// SetKey writes key = value into the config file at path.
//
// Where it writes is decided by how the file already reads, and by one fact
// about the parser: LAST ONE WINS, because Apply walks the file in order. So
//
//   - if the key already appears UNCOMMENTED, the LAST such line is rewritten.
//     Rewriting the first would leave a later line silently overriding it, and
//     the operator would see their file say one thing and the server do another.
//   - otherwise the setting is APPENDED. Appending always wins, and it leaves
//     the commented template - which is documentation - untouched. Uncommenting
//     the template line in place would look tidier and would be wrong whenever
//     a later uncommented line for the same key exists.
//
// Every other byte of the file survives. The write is atomic: a temp file in
// the same directory, then a rename, so an interrupted write cannot leave the
// operator with half a config.
func SetKey(path, key, value string) error {
	original, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	// Refuse to write a key the parser would then reject. Persisting something
	// that stops the server starting next time is the worst possible outcome
	// for a convenience feature.
	probe := Defaults()
	if err := Apply(&probe, strings.NewReader(key+" = "+value+"\n")); err != nil {
		return fmt.Errorf("refusing to write a line the server could not read back: %w", err)
	}

	text := string(original)
	// Keep the file's own line ending. A config edited on Windows should not
	// come back with mixed endings because this function preferred "\n".
	nl := "\n"
	if strings.Contains(text, "\r\n") {
		nl = "\r\n"
	}
	lines := strings.Split(strings.TrimSuffix(text, nl), nl)

	last := -1
	for i, l := range lines {
		m := setLine.FindStringSubmatch(l)
		if m == nil || m[3] != key {
			continue
		}
		if m[2] == "" { // uncommented: a line the parser actually reads
			last = i
		}
	}

	if last >= 0 {
		m := setLine.FindStringSubmatch(lines[last])
		// m[4] is the whitespace the operator already had before the "=", so
		// their alignment survives. Note there is no space of our own before
		// it: adding one produced "check_uploads  = yes", which a test caught
		// and reading the code twice had not.
		lines[last] = m[1] + key + m[4] + "= " + value
	} else {
		if len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) != "" {
			lines = append(lines, "")
		}
		lines = append(lines,
			"# Set from the browse page on "+time.Now().Format("2006-01-02 15:04")+".",
			key+" = "+value)
	}

	out := strings.Join(lines, nl) + nl
	return writeAtomically(path, []byte(out))
}

// writeAtomically writes through a temp file in the SAME directory, because a
// rename is only atomic within a filesystem and /tmp is often a different one.
func writeAtomically(path string, body []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".yss-conf-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp) // no-op once the rename has succeeded

	// Carry the original's permissions over rather than leaving whatever
	// CreateTemp chose. A config that becomes 0600 because it was edited would
	// be a surprising side effect of pressing a switch.
	if st, err := os.Stat(path); err == nil {
		_ = f.Chmod(st.Mode().Perm())
	}

	if _, err := f.Write(body); err != nil {
		f.Close()
		return err
	}
	// Flush to disk before the rename. Without this a power cut between the
	// two can leave the rename durable and the contents not.
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

// YesNo is the spelling written back for a boolean, chosen to match the
// template rather than strconv's "true"/"false".
func YesNo(b bool) string {
	if b {
		return "yes"
	}
	return "no"
}

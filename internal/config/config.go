// Package config resolves the server's settings from three places, in
// increasing order of authority: built-in defaults, a config file, then flags.
//
// The format is deliberately `key = value` with `#` comments rather than YAML or
// TOML. It keeps the module's dependency list at one (golang.org/x/text, for
// Unicode normalisation the standard library does not provide), which is what
// keeps the cross-compile to every promised platform a single `go build` — the
// Raspberry Pi target in ADR-001 rests on that. A settings file with three keys
// does not justify a parser dependency.
//
// The key names are exactly the flag names, so there is one vocabulary to learn
// rather than two that can drift.
package config

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"
)

// Config is everything the server can be told.
type Config struct {
	Listen string
	Songs  string
	Data   string
}

// Defaults are what the server does when told nothing.
func Defaults() Config {
	return Config{
		Listen: ":8080",
		Songs:  "./songs",
		Data:   "./data",
	}
}

// DefaultPath is looked for when no config file is named. Absent is fine and
// silent — a first run should not require a file.
const DefaultPath = "yarg-song-server.conf"

// LoadFile applies a config file onto c.
//
// Only keys present in the file are touched, so the file overrides defaults and
// leaves everything else alone. Flags are applied after this and therefore win.
func LoadFile(c *Config, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	if err := Apply(c, f); err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	return nil
}

// Apply parses `key = value` lines from r onto c.
//
// An unrecognised key is an ERROR, not a warning and not silence. A typo in a
// settings file that is quietly ignored leaves an operator certain they changed
// something they did not, and the server behaving in a way its own config file
// contradicts. Refusing to start is the kinder failure.
func Apply(c *Config, r io.Reader) error {
	sc := bufio.NewScanner(r)
	line := 0
	for sc.Scan() {
		line++
		text := strings.TrimSpace(sc.Text())
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}

		key, value, ok := strings.Cut(text, "=")
		if !ok {
			return fmt.Errorf("line %d: %q is not `key = value`", line, text)
		}
		key = strings.ToLower(strings.TrimSpace(key))
		value = strings.TrimSpace(value)
		// A quoted value is accepted so a path with trailing spaces can be
		// written down unambiguously, but quotes are not required.
		value = trimMatchingQuotes(value)

		switch key {
		case "listen":
			c.Listen = value
		case "songs":
			c.Songs = value
		case "data":
			c.Data = value
		default:
			return fmt.Errorf("line %d: unknown setting %q; valid settings are listen, songs, data", line, key)
		}
	}
	return sc.Err()
}

func trimMatchingQuotes(s string) string {
	if len(s) >= 2 && (s[0] == '"' || s[0] == '\'') && s[len(s)-1] == s[0] {
		return s[1 : len(s)-1]
	}
	return s
}

// Example is a commented file a new operator can start from.
const Example = `# yarg-song-server configuration.
#
# Every setting here can also be given as a flag, and a flag WINS over this file.
# Unknown settings are an error rather than a warning: a typo that is silently
# ignored leaves you certain you changed something you did not.

# Address to listen on. ":8080" means every interface.
# listen = :8080

# The song library. Read-only in normal operation.
# songs = ./songs

# Where the server keeps its own state, including packed .sng archives.
# Must be writable.
# data = ./data
`

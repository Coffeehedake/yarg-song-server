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
	"strconv"
	"strings"
)

// Config is everything the server can be told.
type Config struct {
	Listen string
	Songs  string
	Data   string
	// PackCacheMax bounds the on-demand pack cache in bytes. Zero means
	// unbounded, which an operator may choose but which is not the default.
	PackCacheMax int64
}

// Defaults are what the server does when told nothing.
//
// PackCacheMax defaults to 2 GiB rather than to unbounded, and that is a
// judgement worth stating. A packed archive is within 2% of the size of the
// folder it came from (measured 2026-09-05: 225,406 bytes of library produced
// 229,515 bytes of cache), so an unbounded cache over a library of loose
// folders eventually needs a second copy of the whole library on the data disk.
// On this project's primary target - a Raspberry Pi with the library on
// external storage and -data on the SD card - that fills the card.
//
// 2 GiB is generous for a modest library and small enough to matter on a card.
// An operator with a large library and a large disk should raise it; nothing
// breaks when the bound is hit, because eviction only costs a re-pack.
func Defaults() Config {
	return Config{
		Listen:       ":8080",
		Songs:        "./songs",
		Data:         "./data",
		PackCacheMax: 2 << 30, // 2 GiB
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
		case "pack_cache_max":
			n, err := parseSize(value)
			if err != nil {
				return fmt.Errorf("line %d: pack_cache_max: %w", line, err)
			}
			c.PackCacheMax = n
		default:
			return fmt.Errorf("line %d: unknown setting %q; valid settings are listen, songs, data, pack_cache_max", line, key)
		}
	}
	return sc.Err()
}

// parseSize reads a byte count, optionally with a K/M/G/T suffix, binary units.
//
// "0" is accepted and means unbounded. A NEGATIVE value is rejected rather than
// silently treated as unbounded: someone writing -1 is expressing an intent the
// parser should not guess at, and quietly turning it into "no limit at all"
// would be the worst possible reading of it.
func parseSize(s string) (int64, error) {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0, fmt.Errorf("empty value; use a byte count such as 2G, or 0 for unbounded")
	}
	mult := int64(1)
	switch last := s[len(s)-1]; last {
	case 'k', 'K':
		mult, s = 1<<10, s[:len(s)-1]
	case 'm', 'M':
		mult, s = 1<<20, s[:len(s)-1]
	case 'g', 'G':
		mult, s = 1<<30, s[:len(s)-1]
	case 't', 'T':
		mult, s = 1<<40, s[:len(s)-1]
	}
	n, err := strconv.ParseInt(strings.TrimSpace(s), 10, 64)
	if err != nil {
		return 0, fmt.Errorf("%q is not a byte count; use e.g. 2G, 512M, or 0 for unbounded", s)
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative; use 0 for unbounded")
	}
	if mult > 1 && n > (1<<62)/mult {
		return 0, fmt.Errorf("%q overflows", s)
	}
	return n * mult, nil
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

# Bound the on-demand pack cache. Accepts a plain byte count or a K/M/G/T
# suffix (binary units). 0 means UNBOUNDED.
#
# A song stored as a loose folder is packed to .sng on first request and the
# archive is kept. An archive is within about 2% of the size of the folder it
# came from, so an unbounded cache over a library of loose folders eventually
# needs a second copy of that library on this disk. On a Raspberry Pi with the
# library on external storage and this directory on the SD card, that fills the
# card.
#
# Hitting the bound costs a re-pack, never data: an evicted archive is rebuilt
# byte-identically from the folder, and its package hash does not change. That
# holds because the archive's obfuscation mask is derived from the package hash
# rather than drawn at random - it was random until 2026-09-06, when two
# machines syncing one server were found to have received 16 different archives
# out of 22.
# Raise it if you have the disk; set 0 only if you mean it.
# pack_cache_max = 2G
`

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
	// BrowseUI serves a phone-friendly catalog page at "/".
	BrowseUI bool
	// CheckUploads enables POST /api/v1/check, which scans an uploaded archive
	// and returns the verdict without keeping it. OFF by default: see Defaults.
	CheckUploads bool
	// CheckMaxBytes bounds one uploaded body. Ignored when CheckUploads is off.
	CheckMaxBytes int64
	// ConfigWrites says who may change a feature at runtime over HTTP: off,
	// local, or lan. See writes.go for why it is not a bool.
	ConfigWrites WriteAccess
	// UnpackOps says who may unpack an archive in the library over HTTP: off,
	// local, or lan. Same vocabulary as ConfigWrites, deliberately, because an
	// operator should not have to learn two of them.
	//
	// OFF by default. Unpacking writes to the library, and no deployment should
	// acquire the ability to do that by being upgraded.
	UnpackOps WriteAccess
	// AdminSecret is a shared secret a caller must present to unpack.
	//
	// It is what locality cannot provide. The deployment this is for is a phone
	// on the same LAN as the server, so UnpackOps must be "lan" for it to work
	// at all - and "lan" alone means every device on the network, including the
	// ones nobody is thinking about.
	//
	// It is NOT a security boundary and must not be described as one. This
	// server speaks plain HTTP, so the secret crosses the network in clear and
	// anyone who can watch the traffic has it. What it buys is that unpacking
	// cannot happen by accident, by a misconfigured client, or by somebody
	// poking at the port out of curiosity. Treat it as the cover on a switch.
	//
	// Empty with UnpackOps set to anything but off is refused at startup: a
	// secret nobody set is not a secret, and silently allowing it would make
	// the cover ornamental.
	AdminSecret string
	// IncomingDir is the drop folder somebody puts songs and archives into for
	// the server to take in. Empty means <songs>/_incoming.
	//
	// It is configurable for the same reason UnpackDir is: the library is
	// mounted read-only on the deployment this was built for, so the one place
	// anything may be written has to be sayable. It must be a directory the
	// server can WRITE to - things are moved out of it, not copied - and
	// nothing is ever taken from it without being asked.
	IncomingDir string
	// UnpackDir is where unpacked songs are written. Empty means beside the
	// archive they came from.
	//
	// It exists because the live deployment mounts the library READ-ONLY, on
	// purpose, so that the server cannot damage what it serves. Unpacking needs
	// somewhere writable, and the choice between "make the whole library
	// writable" and "make one directory writable" belongs to the operator, at
	// the machine. This is how they say which. It must be a directory the
	// server also scans, or the songs will be extracted and never appear.
	UnpackDir string
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
		// On by default. The page shows only what /api/v1/songs already serves
		// to anyone who can reach the port, so enabling it grants no access that
		// was not already there - the decision that matters is the one ADR-001
		// already made, that this server is read-only and must not be exposed
		// publicly. An operator who wants the API without a page sets it false.
		BrowseUI: true,
		// OFF by default, unlike browse_ui, and the difference is the point.
		// The browse page shows what the API already served to anyone who could
		// reach the port; this accepts a large upload and spends CPU and temp
		// disk on it. That is new surface, not a new view of old surface, so an
		// existing deployment must not acquire it by being upgraded. One line
		// of config turns it on.
		CheckUploads: false,
		// 128 MiB. A packed song is normally a few MB and the largest in the
		// real corpus on hand is under 40; 128 leaves room for an album-length
		// chart with lossless stems without letting one request write a
		// gigabyte to a Pi's SD card.
		CheckMaxBytes: 128 << 20,
		// OFF, for the same reason check_uploads is off and then some. An
		// existing deployment must not acquire the ability to be reconfigured
		// over the network by being upgraded, and this server has no
		// authentication to put in front of that ability.
		ConfigWrites: WritesOff,
		// Off, like ConfigWrites and for a stronger reason: this one writes to the
		// library. No deployment may acquire that by being upgraded.
		UnpackOps: WritesOff,
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
	_, err := LoadFileTracked(c, path)
	return err
}

// LoadFileTracked is LoadFile, and also reports which keys the file set.
//
// The error from a missing file is returned unwrapped so a caller can still
// test it with errors.Is(err, fs.ErrNotExist) — the conventional file simply
// not being there is a normal first run, and only the caller knows whether this
// path was named on the command line or guessed at.
func LoadFileTracked(c *Config, path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	keys, err := ApplyTracked(c, f)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", path, err)
	}
	return keys, nil
}

// Apply parses `key = value` lines from r onto c.
//
// An unrecognised key is an ERROR, not a warning and not silence. A typo in a
// settings file that is quietly ignored leaves an operator certain they changed
// something they did not, and the server behaving in a way its own config file
// contradicts. Refusing to start is the kinder failure.
func Apply(c *Config, r io.Reader) error {
	_, err := ApplyTracked(c, r)
	return err
}

// ApplyTracked is Apply, and also reports which keys the file actually set.
//
// It exists so the feature registry can say WHERE a value came from rather than
// only what it is. Deriving that by diffing the config before and after would
// be wrong in the case that matters most - a file setting a key to the value it
// already had is still the file speaking, and a diff would report it as a
// default.
//
// Apply delegates here rather than the two parsing separately. Two parsers for
// one format agree for a while and then quietly stop, which is the failure this
// project has already paid for in the packcache path and in both sync clients.
func ApplyTracked(c *Config, r io.Reader) ([]string, error) {
	var keys []string
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
			return nil, fmt.Errorf("line %d: %q is not `key = value`", line, text)
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
				return nil, fmt.Errorf("line %d: pack_cache_max: %w", line, err)
			}
			c.PackCacheMax = n
		case "browse_ui":
			b, err := parseBool(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: browse_ui: %w", line, err)
			}
			c.BrowseUI = b
		case "check_uploads":
			b, err := parseBool(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: check_uploads: %w", line, err)
			}
			c.CheckUploads = b
		case "check_max_bytes":
			n, err := parseSize(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: check_max_bytes: %w", line, err)
			}
			c.CheckMaxBytes = n
		case "config_writes":
			w, err := ParseWriteAccess(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: config_writes: %w", line, err)
			}
			c.ConfigWrites = w
		case "unpack_ops":
			w, err := ParseWriteAccess(value)
			if err != nil {
				return nil, fmt.Errorf("line %d: unpack_ops: %w", line, err)
			}
			c.UnpackOps = w
		case "admin_secret":
			c.AdminSecret = value
		case "unpack_dir":
			c.UnpackDir = value
		case "incoming_dir":
			c.IncomingDir = value
		default:
			return nil, fmt.Errorf("line %d: unknown setting %q; valid settings are listen, songs, data, pack_cache_max, browse_ui, check_uploads, check_max_bytes, config_writes, unpack_ops, admin_secret, unpack_dir, incoming_dir", line, key)
		}
		keys = append(keys, key)
	}
	if err := sc.Err(); err != nil {
		return nil, err
	}
	return keys, nil
}

// parseBool accepts the spellings a person actually writes in a settings file.
//
// strconv.ParseBool alone would reject "yes" and "on", which is the sort of
// pedantry that makes an operator edit a file three times. Anything it does not
// recognise is still an error rather than a guess - "maybe" must not silently
// become false.
func parseBool(s string) (bool, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "1", "t", "true", "y", "yes", "on":
		return true, nil
	case "0", "f", "false", "n", "no", "off":
		return false, nil
	}
	return false, fmt.Errorf("%q is not a yes/no value", s)
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

# Serve a phone-friendly page listing the library at "/".
#
# On by default. It shows only what /api/v1/songs already serves to anyone who
# can reach this port, so turning it on grants no access that was not already
# there. Set it to no if you want the API and nothing else.
# browse_ui = yes

# Accept an uploaded archive at POST /api/v1/check, scan it, and answer with the
# verdict - the same verdict a library scan would give the same file.
#
# OFF by default, and the difference from browse_ui is deliberate. The browse
# page shows what the API already served to anyone who could reach this port;
# this accepts a large upload and spends CPU and temporary disk on it. That is
# new surface rather than a new view of old surface, so an existing server must
# not acquire it merely by being upgraded.
#
# It KEEPS NOTHING. The upload is written to a temporary file, scanned, and
# deleted before the response is written. This server is still read-only with
# respect to your library.
# check_uploads = no

# The largest body POST /api/v1/check will accept. Same size suffixes as
# pack_cache_max. Anything larger is refused with 413 before it is read.
# check_max_bytes = 128M

# Who may turn features on and off from the browse page, rather than by editing
# this file and restarting. One of:
#
#   off     nobody. The route does not exist. THIS IS THE DEFAULT.
#   local   only from this machine. Open the page on the server itself and you
#           get switches; open the same page on your phone and you do not.
#   lan     anyone who can reach this port.
#
# Not a yes/no, on purpose. "yes" would have had to mean "lan", and on a home
# network that is every device including the ones nobody is thinking about.
# THIS SERVER HAS NO AUTHENTICATION, so "lan" means exactly what it says: any
# device on your network can turn features on and off. That is a reasonable
# choice for a box you administer from a laptop, and it should be a choice.
#
# Changes take effect immediately and DO NOT SURVIVE A RESTART. To make one
# permanent, set it in this file. The server tells you the line to write.
#
# config_writes itself can never be changed this way - a write surface that can
# widen its own access has no bound at all. It is only ever set here.
# config_writes = off
`

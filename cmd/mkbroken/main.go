// mkbroken damages real songs on purpose, so the oracle has disagreements to
// find.
//
// # Why this exists
//
// The oracle's standard is "every song YARG rejects should be one our scanner
// independently flags". On 2026-09-07 it was run against 128 real community
// songs for the first time and held - trivially. YARG refused none, we flagged
// none, and a comparison where both sides say nothing cannot distinguish a
// scanner that agrees with YARG from one that is asleep. Curated packs that
// people actually play are close to the least informative input possible for
// this test, because the thing being tested is behaviour on BROKEN songs.
//
// Synthetic songs do not close that gap either, and cmd/mkcorpus says why: we
// can write the mistakes we thought of, not the ones we did not. What this does
// is narrower and more useful - it takes songs with real structure and breaks
// them in specific, named ways, each with a PREDICTED verdict.
//
// # The three-way comparison is the point
//
// Every case carries what we expect YARG to do with it. That turns the oracle
// from a two-column table into a three-column one:
//
//	prediction | our scanner | YARG
//
// and each disagreement means something different. YARG rejecting something we
// passed is the standard being violated. YARG ACCEPTING something we predicted
// it would reject means our model of YARG is wrong, which is worth knowing and
// which a two-column comparison cannot see at all.
//
// # What this does not produce
//
// Output derives from the input, so if the input is copyrighted audio then so is
// the output. **Neither the input nor the output of this tool may be committed
// or redistributed.** The tool is committed; what it makes is not. That is the
// same line drawn in docs/TEST-CORPUS.md, and it is why the manifest records
// only names and verdicts rather than any content.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

// Verdict is what we expect YARG to do with a case.
type Verdict string

const (
	// Reject: YARG should refuse this and name it in badsongs.txt.
	Reject Verdict = "reject"
	// Accept: YARG should load it despite the damage. These matter as much as
	// the rejections - a scanner that flags everything is as useless as one
	// that flags nothing, and these are where false positives show up.
	Accept Verdict = "accept"
	// Skip: YARG neither caches nor reports it; the folder is simply not a song
	// as far as the scanner is concerned. Distinct from Reject, and the
	// difference has already bitten this project once.
	Skip Verdict = "skip"
)

// A mutation takes a healthy song folder's files and damages them.
//
// It receives the files as a map so a mutation can add, remove or rewrite any
// of them, and returns the map it wants written. Returning nil means "this
// mutation does not apply to this song" - not every song has separate stems.
type mutation struct {
	name string
	// why records what real-world failure this imitates. A case whose reason
	// nobody can state is a case nobody can act on when it fails.
	why    string
	expect Verdict
	damage func(files map[string][]byte) map[string][]byte
}

func main() {
	from := flag.String("from", "", "a folder of healthy song folders to damage (required)")
	out := flag.String("out", "broken-corpus", "where to write the damaged copies")
	limit := flag.Int("limit", 0, "use at most this many source songs (0 = all needed)")
	flag.Parse()

	if *from == "" {
		fmt.Fprintln(os.Stderr, "mkbroken: -from is required: point it at real songs")
		os.Exit(2)
	}
	if err := run(*from, *out, *limit); err != nil {
		fmt.Fprintln(os.Stderr, "mkbroken:", err)
		os.Exit(1)
	}
}

func mutations() []mutation {
	return []mutation{
		{
			name:   "no-audio",
			why:    "a chart uploaded without its stems, or an extraction that dropped them",
			expect: Reject,
			damage: func(f map[string][]byte) map[string][]byte {
				for n := range f {
					if isAudio(n) {
						delete(f, n)
					}
				}
				return f
			},
		},
		{
			name:   "no-chart",
			why:    "audio and art with the chart missing - a half-finished upload",
			expect: Skip,
			damage: func(f map[string][]byte) map[string][]byte {
				for n := range f {
					if isChart(n) {
						delete(f, n)
					}
				}
				return f
			},
		},
		{
			name:   "chart-truncated",
			why:    "an interrupted download or a full disk mid-write",
			expect: Reject,
			damage: func(f map[string][]byte) map[string][]byte {
				for n, b := range f {
					if isChart(n) && len(b) > 40 {
						f[n] = b[:len(b)/3]
						return f
					}
				}
				return nil
			},
		},
		{
			name:   "chart-is-not-a-chart",
			why:    "a text file that got the chart's name - the extension lying about the contents",
			expect: Reject,
			damage: func(f map[string][]byte) map[string][]byte {
				for n := range f {
					if isChart(n) {
						f[n] = []byte("this is not a MIDI file, it is a note to self\n")
						return f
					}
				}
				return nil
			},
		},
		{
			name:   "stem-truncated",
			why:    "a valid header with the data cut short; the file opens and then ends",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				for n, b := range f {
					if isAudio(n) && len(b) > 4096 {
						f[n] = b[:2048]
						return f
					}
				}
				return nil
			},
		},
		{
			name:   "stem-empty",
			why:    "a zero-byte stem, which a failed copy leaves behind",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				for n := range f {
					if isAudio(n) {
						f[n] = []byte{}
						return f
					}
				}
				return nil
			},
		},
		{
			name:   "no-song-ini",
			why:    "the chart and audio without metadata",
			expect: Skip,
			damage: func(f map[string][]byte) map[string][]byte {
				delete(f, "song.ini")
				return f
			},
		},
		{
			name:   "song-ini-empty",
			why:    "the file exists and says nothing",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				f["song.ini"] = []byte{}
				return f
			},
		},
		{
			name:   "song-ini-no-header",
			why:    "keys with no [Song] section above them - a hand-edited file",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				f["song.ini"] = []byte("name = Headerless\nartist = Nobody\n")
				return f
			},
		},
		{
			name:   "song-ini-utf16-no-bom",
			why:    "an editor that saved UTF-16 without a byte order mark",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				var b []byte
				for _, r := range "[Song]\nname = UTF16 No BOM\nartist = Nobody\n" {
					b = append(b, byte(r), 0)
				}
				f["song.ini"] = b
				return f
			},
		},
		{
			name:   "song-ini-invalid-utf8",
			why:    "bytes that are not valid UTF-8 in any encoding the reader guesses",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				f["song.ini"] = []byte("[Song]\nname = Bad \xff\xfe\xfd Bytes\nartist = Nobody\n")
				return f
			},
		},
		{
			name:   "song-ini-nul-bytes",
			why:    "NUL in the middle of a value; a truncated write over an existing file",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				f["song.ini"] = []byte("[Song]\nname = Nul\x00Inside\nartist = Nobody\n")
				return f
			},
		},
		{
			name:   "song-ini-absurd-length",
			why:    "a 64 KB name field; nothing forbids it and nothing expects it",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				f["song.ini"] = []byte("[Song]\nname = " + strings.Repeat("A", 64<<10) + "\nartist = Nobody\n")
				return f
			},
		},
		{
			name:   "audio-extension-lies",
			why:    "an Ogg renamed .mp3 - the container disagreeing with the name",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				for n, b := range f {
					if isAudio(n) && strings.HasSuffix(strings.ToLower(n), ".ogg") {
						delete(f, n)
						f[strings.TrimSuffix(n, filepath.Ext(n))+".mp3"] = b
						return f
					}
				}
				return nil
			},
		},
		{
			name:   "art-is-not-an-image",
			why:    "album art that is text; art is decoration and must not sink a song",
			expect: Accept,
			damage: func(f map[string][]byte) map[string][]byte {
				for n := range f {
					if strings.HasSuffix(strings.ToLower(n), ".png") || strings.HasSuffix(strings.ToLower(n), ".jpg") {
						f[n] = []byte("not an image")
						return f
					}
				}
				return nil
			},
		},
		{
			name:   "everything-but-audio-removed",
			why:    "a folder holding only stems - the inverse of no-audio",
			expect: Skip,
			damage: func(f map[string][]byte) map[string][]byte {
				for n := range f {
					if !isAudio(n) {
						delete(f, n)
					}
				}
				return f
			},
		},
	}
}

type record struct {
	Case   string  `json:"case"`
	Folder string  `json:"folder"`
	Expect Verdict `json:"expect"`
	Why    string  `json:"why"`
}

func run(from, out string, limit int) error {
	sources, err := healthySongs(from)
	if err != nil {
		return err
	}
	if len(sources) == 0 {
		return fmt.Errorf("no song folders under %s", from)
	}
	if limit > 0 && limit < len(sources) {
		sources = sources[:limit]
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	muts := mutations()
	var manifest []record
	skipped := 0

	for i, m := range muts {
		// Spread the mutations across different source songs, so a single odd
		// source cannot colour every case, and so the corpus keeps some of the
		// variety the real library has.
		src := sources[i%len(sources)]
		files, err := readSong(src)
		if err != nil {
			return err
		}
		damaged := m.damage(files)
		if damaged == nil {
			// The mutation did not apply to this song. Try the others before
			// giving up - a corpus quietly missing a case is worse than one
			// that says so.
			found := false
			for _, alt := range sources {
				files, err = readSong(alt)
				if err != nil {
					return err
				}
				if d := m.damage(files); d != nil {
					damaged, src, found = d, alt, true
					break
				}
			}
			if !found {
				fmt.Fprintf(os.Stderr, "  skipped %s: no source song it applies to\n", m.name)
				skipped++
				continue
			}
		}
		folder := fmt.Sprintf("%02d-%s", i+1, m.name)
		if err := writeSong(filepath.Join(out, folder), damaged); err != nil {
			return err
		}
		manifest = append(manifest, record{Case: m.name, Folder: folder, Expect: m.expect, Why: m.why})
	}

	// The manifest is what makes this a three-way comparison rather than a pile
	// of broken folders. It records names and verdicts only - never content.
	b, err := json.MarshalIndent(manifest, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(out, "manifest.json"), append(b, '\n'), 0o644); err != nil {
		return err
	}

	fmt.Printf("wrote %d damaged song(s) to %s from %d source song(s)", len(manifest), out, len(sources))
	if skipped > 0 {
		fmt.Printf(" (%d case(s) skipped)", skipped)
	}
	fmt.Println()
	byVerdict := map[Verdict]int{}
	for _, r := range manifest {
		byVerdict[r.Expect]++
	}
	fmt.Printf("expected: %d reject, %d accept, %d skip\n", byVerdict[Reject], byVerdict[Accept], byVerdict[Skip])
	return nil
}

// healthySongs finds folders that look like songs: a chart and at least one
// audio file. Anything else is not a usable starting point for damage.
func healthySongs(root string) ([]string, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil, err
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(root, e.Name())
		files, err := os.ReadDir(dir)
		if err != nil {
			continue
		}
		var chart, audio bool
		for _, f := range files {
			if f.IsDir() {
				continue
			}
			if isChart(f.Name()) {
				chart = true
			}
			if isAudio(f.Name()) {
				audio = true
			}
		}
		if chart && audio {
			out = append(out, dir)
		}
	}
	sort.Strings(out)
	return out, nil
}

func readSong(dir string) (map[string][]byte, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, err
	}
	files := map[string][]byte{}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		b, err := os.ReadFile(filepath.Join(dir, e.Name()))
		if err != nil {
			return nil, err
		}
		files[e.Name()] = b
	}
	return files, nil
}

func writeSong(dir string, files map[string][]byte) error {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	for name, body := range files {
		if err := os.WriteFile(filepath.Join(dir, name), body, 0o644); err != nil {
			return err
		}
	}
	return nil
}

func isChart(name string) bool {
	switch strings.ToLower(name) {
	case "notes.mid", "notes.midi", "notes.chart", "notes.txt":
		return true
	}
	return false
}

func isAudio(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".ogg", ".opus", ".mp3", ".wav", ".flac":
		return true
	}
	return false
}

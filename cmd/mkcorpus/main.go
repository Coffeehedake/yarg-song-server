// Command mkcorpus writes a deliberately awkward song library.
//
// The Official Setlist is proprietary and licensed for use in YARG only, and
// bulk community charts carry mixed provenance, so this project authors its own
// corpus instead. See docs/TEST-CORPUS.md.
//
// This is not a substitute for real charts and does not pretend to be: it can
// only contain mistakes we thought of. Its value is that YARG itself can scan
// the output, and YARG's verdict on each case is a real measurement of what the
// client accepts - which is the question the server needs answered.
//
//	go run ./cmd/mkcorpus -out <dir>
package main

import (
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"unicode/utf16"
)

func main() {
	out := flag.String("out", "corpus", "directory to write the corpus into")
	flag.Parse()

	if err := write(*out); err != nil {
		fmt.Fprintln(os.Stderr, "mkcorpus:", err)
		os.Exit(1)
	}
	fmt.Printf("wrote %d cases to %s\n", len(cases), *out)
}

// aCase is one song folder plus a note on what it is probing.
type aCase struct {
	dir   string
	probe string
	// files maps a filename to its bytes. A nil value means "the standard
	// generated WAV", so cases stay readable.
	files map[string][]byte
	// ini, when non-empty, is written as song.ini verbatim - including any
	// deliberate encoding weirdness.
	ini []byte
	// chart overrides the default notes.chart name.
	chartName string
}

const baseINI = `[Song]
name = %s
artist = Corpus Artist
album = Corpus Album
genre = Rock
year = 1994
charter = mkcorpus
song_length = 60000
diff_guitar = 3
`

func ini(name string) []byte { return []byte(fmt.Sprintf(baseINI, name)) }

var cases = []aCase{
	{dir: "01-plain", probe: "the ordinary case, everything nominal", ini: ini("Plain")},

	{dir: "02-utf8-bom", probe: "UTF-8 byte order mark before the section header",
		ini: append([]byte{0xEF, 0xBB, 0xBF}, ini("UTF8 BOM")...)},

	{dir: "03-latin1", probe: "Latin-1 high bytes in the artist - must not become U+FFFD",
		ini: func() []byte {
			b := []byte("[Song]\nname = Latin1\nartist = Bj")
			b = append(b, 0xF6)
			return append(b, []byte("rk\ngenre = Rock\nyear = 1994\n")...)
		}()},

	{dir: "04-utf16le", probe: "UTF-16 LE with a BOM",
		ini: func() []byte {
			s := "[Song]\nname = UTF16\nartist = Corpus Artist\nyear = 1994\n"
			b := []byte{0xFF, 0xFE}
			for _, u := range utf16.Encode([]rune(s)) {
				b = append(b, byte(u), byte(u>>8))
			}
			return b
		}()},

	{dir: "05-no-section-header", probe: "no [Song] line at all",
		ini: []byte("name = No Header\nartist = Corpus Artist\nyear = 1994\n")},

	{dir: "06-uppercase-section", probe: "[SONG] rather than [Song]",
		ini: []byte("[SONG]\nname = Upper Section\nartist = Corpus Artist\nyear = 1994\n")},

	{dir: "07-duplicate-keys", probe: "same key twice - which value wins",
		ini: []byte("[Song]\nname = FIRST\nartist = Corpus Artist\nname = SECOND\nyear = 1994\n")},

	{dir: "08-messy-year", probe: "free-text year, which is why year is a string",
		ini: []byte("[Song]\nname = Messy Year\nartist = Corpus Artist\nyear = , 1994?\n")},

	{dir: "09-equals-in-value", probe: "a value containing = , split must be on the FIRST one",
		ini: []byte("[Song]\nname = Equals\nartist = Corpus Artist\nyear = 1994\nlink_other = https://x.test/?a=1&b=2\n")},

	{dir: "10-unknown-keys", probe: "keys no build knows - must survive a repack",
		ini: []byte("[Song]\nname = Unknown Keys\nartist = Corpus Artist\nyear = 1994\nsome_future_key = preserved\nanother_one = also\n")},

	{dir: "11-absurd-numbers", probe: "out-of-range difficulty values - clamp, do not wrap",
		ini: []byte("[Song]\nname = Absurd\nartist = Corpus Artist\nyear = 1994\ndiff_guitar = 9999\ndiff_bass = -32768\ndiff_drums = banana\n")},

	{dir: "12-crlf-and-spacing", probe: "CRLF line endings and ragged whitespace around keys",
		ini: []byte("[Song]\r\n   name   =   Spacing   \r\n\tartist\t=\tCorpus Artist\t\r\nyear = 1994\r\n")},

	{dir: "13-mid-beats-chart", probe: "both notes.mid and notes.chart - mid must win",
		ini: ini("Mid Beats Chart"),
		files: map[string][]byte{
			"notes.mid":   fakeMIDI(),
			"notes.chart": nil, // filled by the writer with the default chart
		}},

	{dir: "14-clean-explicit-stems", probe: "the censored-audio stem variants",
		ini: ini("Clean And Explicit"),
		files: map[string][]byte{
			"vocals.ogg":          nil,
			"vocals_clean.ogg":    nil,
			"vocals_explicit.ogg": nil,
		}},

	{dir: "15-multitrack-drums", probe: "drums_1..drums_4 split kit",
		ini: ini("Multitrack Drums"),
		files: map[string][]byte{
			"drums_1.ogg": nil, "drums_2.ogg": nil, "drums_3.ogg": nil, "drums_4.ogg": nil,
		}},

	{dir: "16-cover-override", probe: "the cover key naming art that is not album.<ext>",
		ini:   []byte("[Song]\nname = Cover Override\nartist = Corpus Artist\nyear = 1994\ncover = artwork.png\n"),
		files: map[string][]byte{"artwork.png": tinyPNG()}},

	{dir: "17-no-song-ini", probe: "a chart with no song.ini at all", ini: nil},

	{dir: "18-empty-song-ini", probe: "a song.ini that is present but empty", ini: []byte("")},

	{dir: "19-no-audio", probe: "chart and metadata but no audio - does YARG accept it",
		ini: ini("No Audio"), files: map[string][]byte{"__noaudio": nil}},

	{dir: "20-ultrastar", probe: "notes.txt, the UltraStar format most tooling ignores",
		ini: ini("UltraStar"), chartName: "notes.txt"},
}

func write(root string) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	audio := wav()
	for _, c := range cases {
		dir := filepath.Join(root, c.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return err
		}

		if c.ini != nil {
			if err := os.WriteFile(filepath.Join(dir, "song.ini"), c.ini, 0o644); err != nil {
				return err
			}
		}

		chartName := c.chartName
		if chartName == "" {
			chartName = "notes.chart"
		}
		if err := os.WriteFile(filepath.Join(dir, chartName), chart(c.dir), 0o644); err != nil {
			return err
		}

		noAudio := false
		for name, data := range c.files {
			if name == "__noaudio" {
				noAudio = true
				continue
			}
			if data == nil {
				if filepath.Ext(name) == ".chart" || filepath.Ext(name) == ".mid" {
					data = chart(c.dir)
				} else {
					data = audio
				}
			}
			if err := os.WriteFile(filepath.Join(dir, name), data, 0o644); err != nil {
				return err
			}
		}
		if !noAudio {
			if _, done := c.files["vocals.ogg"]; !done {
				if err := os.WriteFile(filepath.Join(dir, "song.ogg"), audio, 0o644); err != nil {
					return err
				}
			}
		}
		if _, has := c.files["artwork.png"]; !has {
			if err := os.WriteFile(filepath.Join(dir, "album.png"), tinyPNG(), 0o644); err != nil {
				return err
			}
		}
		// A note beside each case, so a human opening the corpus knows what it
		// is for without reading this file.
		note := fmt.Sprintf("%s\n\nprobes: %s\n", c.dir, c.probe)
		if err := os.WriteFile(filepath.Join(dir, "PROBE.txt"), []byte(note), 0o644); err != nil {
			return err
		}
	}
	return nil
}

func chart(name string) []byte {
	return []byte("[Song]\n{\n  Name = \"" + name + "\"\n  Resolution = 192\n}\n[SyncTrack]\n{\n  0 = TS 4\n  0 = B 120000\n}\n[ExpertSingle]\n{\n  768 = N 0 0\n}\n")
}

// fakeMIDI is a header-only SMF. It is enough for a scanner to identify the
// file; it is NOT a playable chart, and any case relying on it should be about
// file selection rather than musical content.
func fakeMIDI() []byte {
	var b bytes.Buffer
	b.WriteString("MThd")
	_ = binary.Write(&b, binary.BigEndian, uint32(6))
	_ = binary.Write(&b, binary.BigEndian, uint16(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(1))
	_ = binary.Write(&b, binary.BigEndian, uint16(192))
	b.WriteString("MTrk")
	track := []byte{0x00, 0xFF, 0x2F, 0x00}
	_ = binary.Write(&b, binary.BigEndian, uint32(len(track)))
	b.Write(track)
	return b.Bytes()
}

// wav is half a second of a 440 Hz tone, mono, 8 kHz, 16-bit PCM. Real audio
// rather than a fake header, because tools that scan a library open it.
func wav() []byte {
	const rate, seconds = 8000, 0.5
	n := int(rate * seconds)
	var b bytes.Buffer
	dataLen := n * 2
	b.WriteString("RIFF")
	_ = binary.Write(&b, binary.LittleEndian, uint32(36+dataLen))
	b.WriteString("WAVEfmt ")
	_ = binary.Write(&b, binary.LittleEndian, uint32(16))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint16(1))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate))
	_ = binary.Write(&b, binary.LittleEndian, uint32(rate*2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(2))
	_ = binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	_ = binary.Write(&b, binary.LittleEndian, uint32(dataLen))
	for i := 0; i < n; i++ {
		_ = binary.Write(&b, binary.LittleEndian, int16(math.Sin(2*math.Pi*440*float64(i)/rate)*8000))
	}
	return b.Bytes()
}

// tinyPNG is a 1x1 opaque pixel - a real, decodable PNG.
func tinyPNG() []byte {
	return []byte{
		0x89, 'P', 'N', 'G', 0x0D, 0x0A, 0x1A, 0x0A,
		0, 0, 0, 0x0D, 'I', 'H', 'D', 'R',
		0, 0, 0, 1, 0, 0, 0, 1, 8, 2, 0, 0, 0, 0x90, 0x77, 0x53, 0xDE,
		0, 0, 0, 0x0C, 'I', 'D', 'A', 'T',
		0x08, 0xD7, 0x63, 0xF8, 0xCF, 0xC0, 0x00, 0x00, 0x03, 0x01, 0x01, 0x00,
		0x18, 0xDD, 0x8D, 0xB0,
		0, 0, 0, 0, 'I', 'E', 'N', 'D', 0xAE, 0x42, 0x60, 0x82,
	}
}

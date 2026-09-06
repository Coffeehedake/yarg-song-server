// mkscale writes a large, synthetic song library for measuring scan and serve
// cost at a size no hand-written corpus reaches.
//
// It is deliberately NOT the semantic corpus. cmd/mkcorpus writes 23 songs that
// each probe one parsing decision, and every one of them matters; this writes
// thousands of ordinary songs whose only job is volume and variety. Keeping the
// two apart means a scale run cannot quietly change what the oracle measures.
//
// Nothing here is copyrighted. The audio is a generated sine wave and the art is
// a generated PNG, so a scale library can be built, measured and deleted on any
// machine without a licensing question - which the alternative, a pile of real
// community charts, does not allow us to say.
//
// It is deterministic: the same -seed and -n give the same library, byte for
// byte, so two runs on two machines are comparable.
package main

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"flag"
	"fmt"
	"math"
	"math/rand"
	"os"
	"path"
	"path/filepath"
	"strings"
)

func main() {
	out := flag.String("out", "scale-corpus", "directory to write the library into")
	n := flag.Int("n", 1000, "how many songs")
	audio := flag.Int("audio-bytes", 64<<10, "approximate size of each song's audio")
	zipPct := flag.Int("zip-percent", 10, "percentage delivered as a .zip rather than a loose folder")
	seed := flag.Int64("seed", 1, "seed; the same seed and -n give the same library")
	flag.Parse()

	if err := write(*out, *n, *audio, *zipPct, *seed); err != nil {
		fmt.Fprintln(os.Stderr, "mkscale:", err)
		os.Exit(1)
	}
}

var (
	adjectives = []string{"Broken", "Silent", "Crimson", "Hollow", "Iron", "Velvet", "Glass", "Northern", "Electric", "Paper", "Golden", "Restless", "Quiet", "Savage", "Distant", "Bitter"}
	nouns      = []string{"Machine", "Harbour", "Signal", "Cathedral", "Winter", "Engine", "Lantern", "Anthem", "Circuit", "Reverie", "Highway", "Static", "Ember", "Compass", "Marrow", "Tide"}
	bands      = []string{"The Ninth Ward", "Cassette Ghost", "Meridian Fault", "Low Orbit Choir", "Brass Cathedral", "Nightshift Radio", "Palisade", "The Long Now", "Copper Wren", "Signal Hill"}
	genres     = []string{"Rock", "Metal", "Punk", "Indie", "Prog", "Blues", "Funk", "Electronic", "Folk", "Jazz"}
	charters   = []string{"mkscale", "anon", "chartbot", "hand-charted", "community"}
)

func write(root string, n, audioBytes, zipPct int, seed int64) error {
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	rng := rand.New(rand.NewSource(seed))
	art := tinyPNG()

	for i := 0; i < n; i++ {
		name := fmt.Sprintf("%s %s %d", adjectives[rng.Intn(len(adjectives))], nouns[rng.Intn(len(nouns))], i)
		files := map[string][]byte{
			"song.ini":    songINI(name, rng),
			"notes.chart": chart(name, i),
			"song.ogg":    audio(audioBytes, rng),
			"album.png":   art,
		}
		// Some songs carry separate stems, because a multi-file song exercises
		// a different amount of work per song than a single-stem one.
		if rng.Intn(4) == 0 {
			files["guitar.ogg"] = audio(audioBytes/4, rng)
			files["drums.ogg"] = audio(audioBytes/4, rng)
		}

		dir := filepath.Join(root, sanitise(name))
		if rng.Intn(100) < zipPct {
			if err := writeZip(dir+".zip", sanitise(name), files); err != nil {
				return err
			}
			continue
		}
		if err := writeLoose(dir, files); err != nil {
			return err
		}
	}
	fmt.Printf("wrote %d songs to %s (seed=%d, ~%d bytes of audio each, %d%% zipped)\n", n, root, seed, audioBytes, zipPct)
	return nil
}

func songINI(name string, rng *rand.Rand) []byte {
	var b strings.Builder
	b.WriteString("[Song]\n")
	fmt.Fprintf(&b, "name = %s\n", name)
	fmt.Fprintf(&b, "artist = %s\n", bands[rng.Intn(len(bands))])
	fmt.Fprintf(&b, "album = %s %s\n", adjectives[rng.Intn(len(adjectives))], nouns[rng.Intn(len(nouns))])
	fmt.Fprintf(&b, "genre = %s\n", genres[rng.Intn(len(genres))])
	fmt.Fprintf(&b, "year = %d\n", 1968+rng.Intn(58))
	fmt.Fprintf(&b, "charter = %s\n", charters[rng.Intn(len(charters))])
	fmt.Fprintf(&b, "song_length = %d\n", 90_000+rng.Intn(300_000))
	fmt.Fprintf(&b, "diff_guitar = %d\n", rng.Intn(7))
	fmt.Fprintf(&b, "diff_drums = %d\n", rng.Intn(7))
	return []byte(b.String())
}

// chart carries the song's index verbatim, so every song has a distinct chart
// hash and the catalog holds n distinct identities.
//
// The first version derived every note from the NAME alone, and 10,000 songs
// produced only 9,733 distinct charts: note values were rune%5, so two names of
// equal length whose characters happened to agree mod 5 - "... 4" and "... 9",
// for instance - charted identically. That was a generator bug, not a server
// one, but it is worth keeping in mind when reading any scale number: a
// synthetic library can be less varied than its song count suggests, and
// "10,000 songs" was really 9,733 identities with 267 shared-chart groups.
func chart(name string, index int) []byte {
	var b strings.Builder
	b.WriteString("[Song]\n{\n  Offset = 0\n  Resolution = 192\n}\n")
	fmt.Fprintf(&b, "// mkscale %d\n", index)
	b.WriteString("[SyncTrack]\n{\n  0 = TS 4\n  0 = B 120000\n}\n")
	b.WriteString("[ExpertSingle]\n{\n")
	for i, r := range name {
		fmt.Fprintf(&b, "  %d = N %d 0\n", 192*(i+1), int(r)%5)
	}
	b.WriteString("}\n")
	return []byte(b.String())
}

// audio is a WAV of roughly the requested size. It is a real, parseable WAV
// rather than random bytes, because a scanner that rejected it would make the
// whole measurement meaningless.
func audio(size int, rng *rand.Rand) []byte {
	const rate = 8000
	samples := (size - 44) / 2
	if samples < 1 {
		samples = 1
	}
	var b bytes.Buffer
	b.Grow(size + 64)
	dataLen := samples * 2
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
	freq := 220.0 + float64(rng.Intn(440))
	for i := 0; i < samples; i++ {
		_ = binary.Write(&b, binary.LittleEndian, int16(math.Sin(2*math.Pi*freq*float64(i)/rate)*8000))
	}
	return b.Bytes()
}

func writeLoose(dir string, files map[string][]byte) error {
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

func writeZip(p, base string, files map[string][]byte) error {
	f, err := os.Create(p)
	if err != nil {
		return err
	}
	defer f.Close()
	zw := zip.NewWriter(f)
	for name, body := range files {
		w, err := zw.Create(path.Join(base, name))
		if err != nil {
			return err
		}
		if _, err := w.Write(body); err != nil {
			return err
		}
	}
	return zw.Close()
}

// sanitise keeps folder names portable. A scale library that will not copy to
// another filesystem is not much use for comparing two machines.
func sanitise(s string) string {
	return strings.Map(func(r rune) rune {
		if strings.ContainsRune(`<>:"/\|?*`, r) {
			return '-'
		}
		return r
	}, s)
}

// tinyPNG is a 1x1 opaque pixel - a real, decodable PNG. Duplicated from
// mkcorpus on purpose: the two generators are independent by design, and a
// shared helper is exactly the coupling that would let a scale-run change
// quietly alter the semantic corpus.
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

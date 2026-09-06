package scan

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
	"github.com/coffeehedake/yarg-song-server/internal/songini"
)

// PackDir repacks a loose song folder into a .sng.
//
// song.ini becomes the archive's metadata section rather than a contained file,
// which is the one structural difference between the two shapes.
//
// Keys are emitted LOWERCASED. That is not laziness: YARG lowercases filenames
// inside a .sng but not metadata keys, and it matches those keys against a
// lowercase table, so a faithful "Name" would fail the lookup that a lowercased
// "name" passes. The reference encoder does the same - decoding its output
// yields an all-lowercase song.ini.
//
// The chart is copied byte for byte, so the song's identity is unchanged by
// packing. Every other guarantee this server makes rests on that one.
//
// PackDir is deterministic: the same folder produces the same archive, byte for
// byte, on every machine and every run. That is what lets the server hand out a
// strong ETag, honour a Range resume across a cache eviction, and tell two
// clients they have the same download. It is achieved by deriving the .sng
// header mask from the package hash instead of drawing it at random - see
// sng.MaskKeyFor for why that costs nothing.
func PackDir(dir string, w io.Writer) error { return PackFS(os.DirFS(dir), w) }

// PackFS is PackDir over any filesystem, so a song inside a .zip or a .7z packs
// through exactly the same code as one in a folder.
//
// That shared path is not tidiness, it is the guarantee: the archive produced
// from a zipped song is byte-identical to the one produced from the same song
// unzipped, because nothing about the container reaches this function. If these
// ever diverge, changing how a library is stored would silently invalidate
// every client's copy of every song in it.
func PackFS(fsys fs.FS, w io.Writer) error {
	names, err := rootNames(fsys)
	if err != nil {
		return err
	}
	if _, ok := PickChartFile(names); !ok {
		return ErrNoChart
	}

	var meta []sng.Pair
	var files []string
	for _, n := range names {
		if strings.EqualFold(n, "song.ini") {
			raw, err := fs.ReadFile(fsys, n)
			if err != nil {
				return fmt.Errorf("pack: read song.ini: %w", err)
			}
			ini := songini.Parse(raw)
			// Order is the order the keys were read in, so a repack does not
			// gratuitously reshuffle the file.
			for _, k := range ini.Order {
				meta = append(meta, sng.Pair{Key: k, Value: ini.Values[k]})
			}
			continue
		}
		files = append(files, n)
	}

	// Seed the mask with the package hash, over the FULL name list including
	// song.ini - the same input scan.newSong hashes - so the mask is a function
	// of the package's content and matches the ETag the server will send for it.
	pkg, err := packageHash(fsys, names)
	if err != nil {
		return fmt.Errorf("pack: derive mask: %w", err)
	}
	return sng.Write(w, sng.MaskKeyFor(pkg), meta, fsys, files)
}

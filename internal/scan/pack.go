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
func PackDir(dir string, w io.Writer) error {
	fsys := os.DirFS(dir)
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
	return sng.Write(w, meta, fsys, files)
}

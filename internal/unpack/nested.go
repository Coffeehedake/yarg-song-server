package unpack

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// Some archives hold archives rather than songs.
//
// Measured on vault2: "Rock Band 2 DLC" is sixteen parts holding 931 entries, almost all of
// them .7z files with one song in each, and it reports ZERO songs because the chart is one
// archive further down than songRoots looks. Extracting it would leave 931 small archives in
// the library. That is not useless - the server indexes a .7z as a container and would serve
// every one of those songs - but it pays a decompression on every fetch, which is the exact
// cost unpacking exists to remove.
//
// So the inner archives are opened too. Each is extracted beside itself and then moved into a
// _packed directory INSIDE the destination, which the library walk skips at any depth: the
// original is kept, costs nothing, and sits with the songs it produced.
//
// One level, deliberately. That is the shape that exists here, and a general recursion would
// need a cycle guard, a depth limit and an answer for what a zip of a zip of a zip means - all
// of it reasoning about cases nobody has, paid for in code that cannot be tested against
// anything real.

// expandNested opens archives found inside a freshly extracted tree.
//
// It returns how many it opened. A failure on one is reported and the rest continue: a pack of
// nine hundred songs must not be lost to one bad file in it.
func expandNested(root string) (opened int, problems []string) {
	var archives []string

	// Collected before anything is changed. Walking a tree while extracting into it invites
	// finding what was just written and opening it again.
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p != root && scan.IsReservedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if scan.IsContainer(d.Name()) {
			archives = append(archives, p)
		}
		return nil
	})
	if len(archives) == 0 {
		return 0, nil
	}
	sort.Strings(archives)

	parked := filepath.Join(root, scan.IgnoredDirName)
	if err := os.MkdirAll(parked, 0o755); err != nil {
		return 0, []string{fmt.Sprintf("could not create %s: %v", parked, err)}
	}

	for _, a := range archives {
		dest, err := freeDirName(strings.TrimSuffix(a, filepath.Ext(a)))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(a), err))
			continue
		}
		if _, _, err := scan.ExtractTo(a, dest); err != nil {
			// Left where it is. An archive this server could not open is still served as a
			// container, so refusing to extract it costs a decompression per fetch rather
			// than the song itself.
			os.RemoveAll(dest)
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(a), err))
			continue
		}

		target, err := freeFileName(filepath.Join(parked, filepath.Base(a)))
		if err != nil {
			problems = append(problems, fmt.Sprintf("%s: %v", filepath.Base(a), err))
			continue
		}
		if err := os.Rename(a, target); err != nil {
			problems = append(problems, fmt.Sprintf("%s: could not park: %v", filepath.Base(a), err))
			continue
		}
		opened++
	}
	return opened, problems
}

func freeDirName(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, nil
	}
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)", path, i)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot find an unused name for %s", filepath.Base(path))
}

// freeFileName never returns a path that already exists. Two inner archives can share a name
// across different folders of one pack, and parking them must not collapse one onto the other.
func freeFileName(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, nil
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 10000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("cannot find an unused name for %s", filepath.Base(path))
}

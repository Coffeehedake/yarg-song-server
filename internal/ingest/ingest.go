// Package ingest takes songs and archives out of a drop folder and into the library.
//
// It exists because adding a song otherwise means knowing where the library lives, what shape
// the server expects, and that an archive has to be unpacked before anything can see inside it.
// A drop folder replaces all of that with "put it here and press the button".
//
// Nothing happens on its own. The folder is watched by nobody: the walk skips it (see
// scan.IncomingDirName) so that a file still being copied cannot be indexed half-written, and
// the contents move only when somebody asks. That is the same decision unpacking made, for the
// same reason - writing to the library is an act somebody takes, not something that happens
// because a file appeared - and it is what makes the whole thing safe to leave switched on.
package ingest

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
	"github.com/coffeehedake/yarg-song-server/internal/unpack"
)

// QuietPeriod is how long an entry must have sat unchanged before it is taken in.
//
// There is no way to ask a filesystem whether a copy has finished. A file arriving over SMB is
// a normal file of the wrong length, and taking one in produces a truncated chart - which still
// hashes to something, and that something gets published as a song that no client can play.
//
// So this is a heuristic and is written down as one: an entry whose modification time is recent
// is assumed to still be arriving and is left for next time. It is deliberately generous,
// because the cost of waiting is that somebody presses the button again in a minute, and the
// cost of being wrong is a corrupt song in the catalogue under a hash that looks legitimate.
const QuietPeriod = 60 * time.Second

// partialSuffixes are names that announce themselves as incomplete. Checked as well as the
// quiet period, not instead of it: a paused download can sit untouched for hours and is still
// not a song.
var partialSuffixes = []string{
	".part", ".partial", ".filepart", ".crdownload", ".tmp", ".temp", ".!ut", ".downloading",
}

// Kind is what an entry in the drop folder turned out to be.
type Kind string

const (
	KindSongFolder Kind = "song folder"
	KindSongFile   Kind = "song file"
	KindArchive    Kind = "archive"
	KindFolder     Kind = "folder of songs"
	KindUnknown    Kind = "not a song"
)

// Item is one thing sitting in the drop folder.
type Item struct {
	Name string `json:"name"`
	Kind Kind   `json:"kind"`
	// Songs is how many songs it would add. Read from stored names for an archive, so it
	// costs a directory read rather than a decompression.
	Songs int `json:"songs"`
	// Skip, with Reason, when it will be left where it is. Reported rather than hidden: an
	// operator who dropped a file and saw nothing happen deserves to know why.
	Skip   bool   `json:"skip"`
	Reason string `json:"reason,omitempty"`
}

// Plan is what taking in the drop folder would do.
type Plan struct {
	Incoming string `json:"incoming"`
	Dest     string `json:"dest"`
	Items    []Item `json:"items"`
}

// Taken is one thing that was moved in.
type Taken struct {
	Name  string `json:"name"`
	Kind  Kind   `json:"kind"`
	Dest  string `json:"dest"`
	Songs int    `json:"songs"`
	Err   string `json:"error,omitempty"`
}

// Result is what taking in the drop folder did.
type Result struct {
	Taken   []Taken `json:"taken"`
	Skipped []Item  `json:"skipped"`
}

// Options says where to look and where things land.
type Options struct {
	// LibraryRoot is the directory the server indexes.
	LibraryRoot string
	// Incoming is the drop folder. Empty means <LibraryRoot>/_incoming.
	Incoming string
	// Dest is where songs are put. Empty means the library root, which is right for a library
	// the server can write to and wrong for the deployment this was built against - vault2
	// mounts the library read-only on purpose, with one writable directory inside it.
	Dest string
}

func (o Options) incoming() string {
	if o.Incoming != "" {
		return o.Incoming
	}
	return filepath.Join(o.LibraryRoot, scan.IncomingDirName)
}

func (o Options) dest() string {
	if o.Dest != "" {
		return o.Dest
	}
	return o.LibraryRoot
}

// Inspect reports what taking in would do. It reads names, modification times and archive
// headers; it decompresses nothing and writes nothing.
func (o Options) Inspect() (*Plan, error) {
	in := o.incoming()
	entries, err := os.ReadDir(in)
	if err != nil {
		if os.IsNotExist(err) {
			return &Plan{Incoming: in, Dest: o.dest()}, nil
		}
		return nil, fmt.Errorf("ingest: read %s: %w", in, err)
	}

	plan := &Plan{Incoming: in, Dest: o.dest()}
	for _, e := range entries {
		plan.Items = append(plan.Items, o.classify(in, e))
	}
	sort.Slice(plan.Items, func(i, j int) bool { return plan.Items[i].Name < plan.Items[j].Name })
	return plan, nil
}

// classify decides what one entry is, without reading more of it than it has to.
func (o Options) classify(in string, e os.DirEntry) Item {
	item := Item{Name: e.Name()}
	full := filepath.Join(in, e.Name())

	info, err := e.Info()
	if err != nil {
		return skip(item, KindUnknown, "could not be read: "+err.Error())
	}

	lower := strings.ToLower(e.Name())
	for _, s := range partialSuffixes {
		if strings.HasSuffix(lower, s) {
			return skip(item, KindUnknown, "looks like an unfinished download")
		}
	}
	if age := time.Since(info.ModTime()); age < QuietPeriod {
		return skip(item, KindUnknown,
			fmt.Sprintf("changed %s ago; waiting in case it is still arriving", age.Round(time.Second)))
	}

	if e.IsDir() {
		if _, serr := scan.ScanDir(os.DirFS(full)); serr == nil {
			item.Kind, item.Songs = KindSongFolder, 1
			return item
		}
		// Not a song itself. It may still be a folder someone dropped with songs inside, which
		// is what a whole setlist copied off another machine looks like.
		n := 0
		_ = scan.WalkLibrary(full, func(r scan.Result) {
			if r.Err == nil && r.Song != nil {
				n++
			}
		})
		if n == 0 {
			return skip(item, KindUnknown, "no songs in it")
		}
		item.Kind, item.Songs = KindFolder, n
		return item
	}

	if scan.IsContainer(e.Name()) {
		_, roots, closer, aerr := scan.OpenContainerAll(full)
		if aerr != nil {
			return skip(item, KindArchive, aerr.Error())
		}
		closer.Close()
		item.Kind, item.Songs = KindArchive, len(roots)
		return item
	}

	if _, serr := scan.ScanFile(full); serr == nil {
		item.Kind, item.Songs = KindSongFile, 1
		return item
	}
	return skip(item, KindUnknown, "not a song, an archive, or a package this server reads")
}

func skip(item Item, k Kind, why string) Item {
	item.Kind, item.Skip, item.Reason = k, true, why
	return item
}

// Run takes in everything the plan did not skip.
//
// One failure does not stop the rest: a drop folder is somebody emptying a USB stick into it,
// and the twentieth item being broken is no reason for the other nineteen to stay put. Each
// failure is reported against the item it belongs to.
func (o Options) Run() (*Result, error) {
	plan, err := o.Inspect()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(o.dest(), 0o755); err != nil {
		return nil, fmt.Errorf("ingest: create %s: %w", o.dest(), err)
	}

	res := &Result{}
	for _, item := range plan.Items {
		if item.Skip {
			res.Skipped = append(res.Skipped, item)
			continue
		}
		res.Taken = append(res.Taken, o.take(plan.Incoming, item))
	}
	return res, nil
}

func (o Options) take(in string, item Item) Taken {
	t := Taken{Name: item.Name, Kind: item.Kind, Songs: item.Songs}
	src := filepath.Join(in, item.Name)

	if item.Kind == KindArchive {
		// Unpacked rather than moved, because an archive in the library is a song nobody can
		// see without decompressing the whole thing first. The archive itself is then parked,
		// not deleted - the operator decides whether to keep it, same as everywhere else.
		rel, rerr := filepath.Rel(o.LibraryRoot, src)
		if rerr != nil {
			t.Err = "archive is not inside the library: " + rerr.Error()
			return t
		}
		// o.dest(), never o.Dest: an empty Dir makes unpack extract BESIDE the archive, which
		// here is inside the drop folder - a directory the walk skips, so the songs would have
		// landed correctly and been invisible, with nothing reporting a problem.
		u := unpack.Options{LibraryRoot: o.LibraryRoot, Dir: o.dest()}
		ures, uerr := u.Run(filepath.ToSlash(rel))
		if uerr != nil {
			t.Err = uerr.Error()
			return t
		}
		t.Dest, t.Songs = ures.Dest, ures.SongsFound

		parked := filepath.Join(o.LibraryRoot, scan.IgnoredDirName)
		if err := os.MkdirAll(parked, 0o755); err != nil {
			t.Err = "unpacked, but could not create " + parked + ": " + err.Error()
			return t
		}
		if err := moveNoClobber(src, filepath.Join(parked, item.Name)); err != nil {
			t.Err = "unpacked, but the archive could not be parked: " + err.Error()
		}
		return t
	}

	dst, err := freeName(filepath.Join(o.dest(), item.Name))
	if err != nil {
		t.Err = err.Error()
		return t
	}
	if err := os.Rename(src, dst); err != nil {
		t.Err = err.Error()
		return t
	}
	if rel, rerr := filepath.Rel(o.LibraryRoot, dst); rerr == nil {
		t.Dest = filepath.ToSlash(rel)
	} else {
		t.Dest = dst
	}
	return t
}

func moveNoClobber(src, dst string) error {
	free, err := freeName(dst)
	if err != nil {
		return err
	}
	return os.Rename(src, free)
}

// freeName never returns a path that already exists.
//
// Overwriting is not an option worth offering here. Two people can drop a folder with the same
// name and mean different songs, and a library that silently replaced one with the other would
// lose a song with nothing to show for it.
func freeName(path string) (string, error) {
	if _, err := os.Stat(path); os.IsNotExist(err) {
		return path, nil
	}
	ext := filepath.Ext(path)
	base := strings.TrimSuffix(path, ext)
	for i := 2; i < 1000; i++ {
		candidate := fmt.Sprintf("%s (%d)%s", base, i, ext)
		if _, err := os.Stat(candidate); os.IsNotExist(err) {
			return candidate, nil
		}
	}
	return "", fmt.Errorf("ingest: cannot find an unused name for %s", filepath.Base(path))
}

// EnsureDir creates the drop folder, so an operator has somewhere to drop things before they
// have ever taken anything in.
func (o Options) EnsureDir() (string, error) {
	in := o.incoming()
	if err := os.MkdirAll(in, 0o755); err != nil {
		return "", fmt.Errorf("ingest: create %s: %w", in, err)
	}
	return in, nil
}

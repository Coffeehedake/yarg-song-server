package main

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/scan"
	"github.com/coffeehedake/yarg-song-server/internal/unpack"
)

// `unpack` does over the command line what POST /api/v1/unpack does over HTTP, and it exists
// because the HTTP route cannot reach the case that needs it most.
//
// The server builds its index BEFORE it listens. An archive in the library is therefore read
// during startup, and reading one song out of a solid .7z was measured on vault2 at about three
// minutes - so a library holding a 2 GB archive does not finish starting, and the endpoint that
// would fix it never answers. Measured, not predicted: deploying multi-song support to vault2
// on 2026-11-01 took the service down for about ten minutes in exactly this way, and had to be
// rolled back.
//
// So the fix has to be reachable without the server running. This runs, unpacks, and exits. It
// builds no index, opens no socket, and can be run as a throwaway container against the library
// while the real server is still serving from its own container.
//
// It reuses internal/unpack rather than reimplementing it, so the safety properties are the
// ones already under test: the archive is never modified or deleted, extraction goes to a
// directory the library walk skips and is renamed only once verified, an existing destination
// is never merged into, and the result is checked by scanning what was written.
func runUnpack(args []string, opt config.Resolved) error {
	dir := opt.UnpackDir
	opts := unpack.Options{LibraryRoot: opt.Songs, Dir: dir}

	switch {
	case len(args) == 1 && args[0] == "list":
		return unpackList(opt.Songs)

	case len(args) == 2 && args[0] == "check":
		plan, err := opts.Inspect(args[1])
		if err != nil {
			return err
		}
		printPlan(plan)
		return nil

	case len(args) == 2 && args[0] == "run":
		plan, err := opts.Inspect(args[1])
		if err != nil {
			return err
		}
		printPlan(plan)
		fmt.Println()
		fmt.Println("unpacking; this reads the archive once, in stored order")

		res, err := opts.Run(args[1])
		if err != nil {
			return err
		}
		fmt.Printf("done: %d files, %.1f MB, %d songs readable afterwards\n",
			res.FilesWritten, float64(res.BytesWritten)/(1<<20), res.SongsFound)
		// The archive is still there and still being read on every startup, which is the
		// cost this was meant to remove. Saying so with the command to fix it, because an
		// operator who stops here has done the expensive half and kept the bill.
		fmt.Printf("\nthe archive is untouched at %s\n", res.Archive)
		fmt.Printf("it is still read on every startup until you move it out of the walk:\n")
		fmt.Printf("  mkdir -p <library>/%s && mv <library>/%q <library>/%s/\n",
			scan.IgnoredDirName, res.Archive, scan.IgnoredDirName)
		fmt.Printf("%s is never indexed, so the archive keeps costing nothing while staying\n",
			scan.IgnoredDirName)
		fmt.Printf("beside the songs it produced.\n")
		return nil
	}

	// Flags come BEFORE the subcommand, because Go's flag package stops parsing at the first
	// argument that is not one. The examples say so rather than leaving it to be discovered.
	return fmt.Errorf("usage - note that flags go BEFORE the subcommand:\n" +
		"  --songs <library> unpack list                list the archives in the library\n" +
		"  --songs <library> unpack check <archive>     say what unpacking would do, writing nothing\n" +
		"  --songs <library> unpack run <archive>       unpack it\n" +
		"\n" +
		"--unpack-dir <dir> sends the result somewhere other than beside the archive,\n" +
		"which is what a library mounted read-only needs")
}

// unpackList finds archives by NAME, walking the library without reading any of them.
//
// Deliberately not the index: building one is the expensive thing this command exists to avoid,
// and an archive's name is enough to list it.
func unpackList(root string) error {
	var found []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable corner is not a reason to list nothing
		}
		if d.IsDir() {
			// Same directories the indexer steps over. An archive already parked in _packed
			// has been dealt with, and listing it as something to unpack would invite doing
			// the whole expensive job a second time.
			if p != root && scan.IsReservedDir(d.Name()) {
				return filepath.SkipDir
			}
			return nil
		}
		if scan.IsContainer(d.Name()) {
			rel, rerr := filepath.Rel(root, p)
			if rerr != nil {
				rel = p
			}
			found = append(found, filepath.ToSlash(rel))
		}
		return nil
	})
	if err != nil {
		return err
	}

	if len(found) == 0 {
		fmt.Println("no archives in", root)
		return nil
	}
	sort.Strings(found)
	for _, f := range found {
		if info, err := os.Stat(filepath.Join(root, filepath.FromSlash(f))); err == nil {
			fmt.Printf("%10.1f MB  %s\n", float64(info.Size())/(1<<20), f)
			continue
		}
		fmt.Printf("%13s  %s\n", "", f)
	}
	return nil
}

func printPlan(p *unpack.Plan) {
	fmt.Printf("archive : %s\n", p.Archive)
	fmt.Printf("into    : %s\n", p.Dest)
	fmt.Printf("holds   : %d songs in %d files, %.1f MB uncompressed\n",
		p.Songs, p.Entries, float64(p.Bytes)/(1<<20))

	if len(p.NameChanges) == 0 {
		return
	}
	// Shown before anything is written, and that is the point of check being separate. A
	// decoded name inside an index costs nothing when it is wrong, because rebuilding drops
	// it; the same name written to disk is permanent.
	fmt.Printf("\n%d name(s) must be rewritten to be usable on disk:\n", len(p.NameChanges))
	for i, c := range p.NameChanges {
		if i >= 20 {
			fmt.Printf("  ... and %d more\n", len(p.NameChanges)-20)
			break
		}
		fmt.Printf("  %s\n    -> %s\n", c.Stored, c.Written)
	}
}

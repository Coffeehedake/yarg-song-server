package main

import (
	"fmt"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/ingest"
)

// `incoming` takes what somebody dropped and puts it in the library.
//
// It runs and exits, like `unpack` and for one of the same reasons: a server that cannot finish
// starting cannot be asked to do anything over HTTP, and a library whose drop folder is full of
// archives is exactly that library. The other reason is plainer - this is how it gets tested
// against a real folder of real songs before any of it is behind a button.
func runIncoming(args []string, opt config.Resolved) error {
	o := ingest.Options{
		LibraryRoot: opt.Songs,
		Incoming:    opt.IncomingDir,
		Dest:        opt.UnpackDir,
	}

	switch {
	case len(args) == 1 && args[0] == "init":
		dir, err := o.EnsureDir()
		if err != nil {
			return err
		}
		fmt.Println("drop songs, folders and archives into:")
		fmt.Println(" ", dir)
		fmt.Println("nothing is taken from it until you run `incoming run`")
		return nil

	case len(args) == 1 && args[0] == "check":
		plan, err := o.Inspect()
		if err != nil {
			return err
		}
		printIncoming(plan)
		return nil

	case len(args) == 1 && args[0] == "run":
		plan, err := o.Inspect()
		if err != nil {
			return err
		}
		printIncoming(plan)
		if len(plan.Items) == 0 {
			return nil
		}

		fmt.Println()
		res, err := o.Run()
		if err != nil {
			return err
		}
		songs := 0
		for _, t := range res.Taken {
			if t.Err != "" {
				fmt.Printf("FAILED  %s: %s\n", t.Name, t.Err)
				continue
			}
			songs += t.Songs
			fmt.Printf("took in %-40s -> %s (%d song(s))\n", t.Name, t.Dest, t.Songs)
		}
		for _, s := range res.Skipped {
			fmt.Printf("left    %-40s %s\n", s.Name, s.Reason)
		}
		fmt.Printf("\n%d song(s) added. Restart the server, or it will not see them until it\n", songs)
		fmt.Printf("next rebuilds its index.\n")
		return nil
	}

	return fmt.Errorf("usage - note that flags go BEFORE the subcommand:\n" +
		"  --songs <library> incoming init     create the drop folder and say where it is\n" +
		"  --songs <library> incoming check    say what would be taken in, and move nothing\n" +
		"  --songs <library> incoming run      take it in")
}

func printIncoming(p *ingest.Plan) {
	fmt.Printf("drop folder : %s\n", p.Incoming)
	fmt.Printf("songs go to : %s\n", p.Dest)

	if len(p.Items) == 0 {
		fmt.Println("\nnothing in it")
		return
	}

	fmt.Println()
	for _, it := range p.Items {
		if it.Skip {
			fmt.Printf("  LEAVE  %-40s %s\n", it.Name, it.Reason)
			continue
		}
		fmt.Printf("  TAKE   %-40s %s, %d song(s)\n", it.Name, it.Kind, it.Songs)
	}
}

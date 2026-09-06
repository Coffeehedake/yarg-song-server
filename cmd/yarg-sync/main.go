// Command yarg-sync pulls a yarg-song-server library into an ordinary local
// songs folder, so an UNMODIFIED YARG plays from a shared library.
//
// It writes nothing but "<chart_hash>.sng" files and never touches anything
// else in the folder, so it is safe to point at a songs folder that already has
// a player's own music in it.
//
//	yarg-sync -server http://pi.local:8080 -songs "%USERPROFILE%\YARG Songs"
package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"

	"github.com/coffeehedake/yarg-song-server/internal/syncclient"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	var (
		server      = flag.String("server", "", "base URL of a yarg-song-server, e.g. http://pi.local:8080")
		songs       = flag.String("songs", "./songs", "local songs folder to sync into")
		dryRun      = flag.Bool("dry-run", false, "report what would change and write nothing")
		prune       = flag.Bool("prune", false, "delete songs the server no longer has (off by default)")
		showVersion = flag.Bool("version", false, "print version and exit")
	)
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *server == "" {
		fmt.Fprintln(os.Stderr, "yarg-sync: -server is required, e.g. -server http://pi.local:8080")
		flag.Usage()
		os.Exit(2)
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	res, err := syncclient.Run(ctx, syncclient.Options{
		ServerURL: *server,
		Dest:      *songs,
		DryRun:    *dryRun,
		Prune:     *prune,
		Log:       log,
	})
	if res != nil {
		verb := "downloaded"
		if *dryRun {
			verb = "would download"
		}
		fmt.Fprintf(os.Stderr,
			"\n%s %d song(s), %d already present, %d on the server, %d pruned, %d failed, %d bytes in %s\n",
			verb, len(res.Downloaded), res.AlreadyHad, res.ServerTotal,
			len(res.Pruned), len(res.Failures), res.BytesFetched, res.Elapsed.Round(1e6))
		if res.Unmanaged > 0 {
			fmt.Fprintf(os.Stderr, "%d item(s) in that folder are not ours and were left alone\n", res.Unmanaged)
		}
		for _, f := range res.Failures {
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", f.ChartHash, f.Err)
		}
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "yarg-sync:", err)
		os.Exit(1)
	}
	// A run that could not fetch some songs is a partial success and must not
	// report success: a sync that silently skipped half a library is exactly the
	// kind of green this project audits other people's scripts for.
	if res != nil && len(res.Failures) > 0 {
		os.Exit(1)
	}
}

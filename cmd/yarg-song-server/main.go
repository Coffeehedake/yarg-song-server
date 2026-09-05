// Command yarg-song-server serves a shared YARG song library over HTTP.
//
// It scans the library once at start, holds the index in memory, and serves
// every song as a .sng - the one format an unmodified YARG reads natively. See
// docs/ROADMAP.md for the build order and docs/API.md for the endpoints.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io/fs"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/httpapi"
	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/packcache"
	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// version is set at build time via -ldflags.
var version = "dev"

func main() {
	def := config.Defaults()

	var (
		flagged     config.Config
		configPath  string
		showVersion = flag.Bool("version", false, "print version and exit")
		writeConfig = flag.Bool("write-config", false, "print a commented example config file and exit")
	)
	flag.StringVar(&configPath, "config", "",
		"path to a config file (default: ./"+config.DefaultPath+" if it exists)")
	flag.StringVar(&flagged.Listen, "listen", def.Listen, "address to listen on")
	flag.StringVar(&flagged.Songs, "songs", def.Songs, "path to the song library")
	flag.StringVar(&flagged.Data, "data", def.Data, "path for catalog and server state")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}
	if *writeConfig {
		fmt.Print(config.Example)
		return
	}

	// `yarg-song-server scan <path>` walks a library and prints the catalog as
	// JSON. It exists so the scanner can be pointed at a real song collection
	// before any of it is behind an HTTP API - the parsers are only as good as
	// the charts they have actually been run against.
	// `yarg-song-server pack <song-folder> <out.sng>` repacks a loose folder.
	// It is here so the writer can be pointed at the reference decoder and at a
	// real YARG install, which are the only two things that can say it is right.
	if args := flag.Args(); len(args) >= 3 && args[0] == "pack" {
		if err := runPack(args[1], args[2]); err != nil {
			fmt.Fprintln(os.Stderr, "pack:", err)
			os.Exit(1)
		}
		return
	}

	if args := flag.Args(); len(args) >= 1 && args[0] == "scan" {
		root := "."
		if len(args) >= 2 {
			root = args[1]
		}
		if err := runScan(root); err != nil {
			fmt.Fprintln(os.Stderr, "scan:", err)
			os.Exit(1)
		}
		return
	}

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	// flag.Visit reports only the flags actually given, which is the whole
	// mechanism: a flag left alone must not overwrite the config file with its
	// own default value. It is lifted out of resolve so precedence is testable
	// without the global flag set.
	given := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { given[f.Name] = true })

	opt, err := resolve(configPath, flagged, given)
	if err != nil {
		log.Error("configuration", "err", err)
		os.Exit(1)
	}

	if err := run(opt, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
}

// resolve applies the three sources in order of authority: defaults, then the
// config file, then any flag the operator actually typed.
//
// Two cases that look alike and are not: a config file NAMED on the command
// line and missing is a mistake and stops the server, while the conventional
// file simply not being there is the normal first run and is silent. A
// malformed file is fatal either way - a settings file the server could not
// read is not a settings file it should guess around.
func resolve(configPath string, flagged config.Config, given map[string]bool) (config.Config, error) {
	cfg := config.Defaults()

	if configPath != "" {
		if err := config.LoadFile(&cfg, configPath); err != nil {
			return cfg, err
		}
	} else if err := config.LoadFile(&cfg, config.DefaultPath); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return cfg, err
	}

	if given["listen"] {
		cfg.Listen = flagged.Listen
	}
	if given["songs"] {
		cfg.Songs = flagged.Songs
	}
	if given["data"] {
		cfg.Data = flagged.Data
	}
	return cfg, nil
}

func runPack(src, dst string) error {
	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	if err := scan.PackDir(src, f); err != nil {
		f.Close()
		os.Remove(dst) // never leave a half-written archive behind
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	st, err := os.Stat(dst)
	if err != nil {
		return err
	}
	fmt.Fprintf(os.Stderr, "packed %s -> %s (%d bytes)\n", src, dst, st.Size())
	return nil
}

func runScan(root string) error {
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")

	var found, failed int
	err := scan.WalkLibrary(root, func(r scan.Result) {
		if r.Err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "  ! %s: %v\n", r.Path, r.Err)
			return
		}
		found++
		r.Song.SourcePath = r.Path
		if err := enc.Encode(r.Song); err != nil {
			fmt.Fprintf(os.Stderr, "  ! encode %s: %v\n", r.Path, err)
		}
	})
	fmt.Fprintf(os.Stderr, "\n%d song(s), %d failure(s)\n", found, failed)
	return err
}

func run(opt config.Config, log *slog.Logger) error {
	// A missing library is refused at start rather than served as an empty one.
	// "The server is up and has no songs" and "the path is wrong" look
	// identical from a client, and only one of them is the operator's fault.
	if st, err := os.Stat(opt.Songs); err != nil || !st.IsDir() {
		return fmt.Errorf("song library %q is not a readable directory: %w", opt.Songs, err)
	}

	started := time.Now()
	ix, err := library.Build(opt.Songs)
	if err != nil {
		return fmt.Errorf("index %q: %w", opt.Songs, err)
	}
	log.Info("library indexed",
		"songs", ix.Len(),
		"distinct_charts", ix.DistinctCharts(),
		"duplicate_packages", ix.DuplicatePackages,
		"problems", len(ix.Problems),
		"took", time.Since(started).Round(time.Millisecond))
	// Every unreadable folder is named once at start. Reporting only the count
	// would leave an operator with "17 problems" and nothing to act on.
	for _, p := range ix.Problems {
		log.Warn("could not index", "path", p.Path, "err", p.Err)
	}

	packs, err := packcache.New(filepath.Join(opt.Data, "packs"))
	if err != nil {
		return err
	}

	api := &httpapi.Server{
		Store:   library.NewStore(ix),
		Packs:   packs,
		Version: version,
		Log:     log,
	}

	srv := &http.Server{
		Addr:              opt.Listen,
		Handler:           api.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", opt.Listen, "songs", opt.Songs, "data", opt.Data, "version", version)
		if err := srv.ListenAndServe(); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		log.Info("shutting down")
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer cancel()
		return srv.Shutdown(shutdownCtx)
	}
}

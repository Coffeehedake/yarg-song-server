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
	"strings"
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
		flagged      config.Config
		configPath   string
		showVersion  = flag.Bool("version", false, "print version and exit")
		configWrites string

		writeConfig = flag.Bool("write-config", false, "print a commented example config file and exit")
	)
	flag.StringVar(&configPath, "config", "",
		"path to a config file (default: ./"+config.DefaultPath+" if it exists)")
	flag.StringVar(&flagged.Listen, "listen", def.Listen, "address to listen on")
	flag.StringVar(&flagged.Songs, "songs", def.Songs, "path to the song library")
	flag.StringVar(&flagged.Data, "data", def.Data, "path for catalog and server state")
	flag.BoolVar(&flagged.BrowseUI, "browse-ui", def.BrowseUI,
		"serve a phone-friendly page listing the library at \"/\"")
	flag.Int64Var(&flagged.PackCacheMax, "pack-cache-max", def.PackCacheMax,
		"bound the on-demand pack cache, in bytes; 0 means unbounded")

	flag.BoolVar(&flagged.CheckUploads, "check-uploads", def.CheckUploads,
		"accept an uploaded archive at POST /api/v1/check, scan it and answer with the verdict; keeps nothing")
	flag.StringVar(&configWrites, "config-writes", string(def.ConfigWrites),
		"who may change a feature at runtime: off, local or lan")
	flag.Int64Var(&flagged.CheckMaxBytes, "check-max-bytes", def.CheckMaxBytes,
		"largest body POST /api/v1/check will accept, in bytes; 0 means unbounded")
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

	// The write-access flag is a string because the setting is a three-way
	// choice, so it is parsed here rather than by the flag package. Only
	// validated when it was actually typed: an untyped flag carries its default
	// and must not be able to fail the parse.
	if given["config-writes"] {
		w, perr := config.ParseWriteAccess(configWrites)
		if perr != nil {
			log.Error("configuration", "err", fmt.Errorf("--config-writes: %w", perr))
			os.Exit(1)
		}
		flagged.ConfigWrites = w
	}

	opt, err := resolveAll(configPath, flagged, given)
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
	res, err := resolveAll(configPath, flagged, given)
	return res.Config, err
}

// resolveAll is resolve, and also records which of the three sources each
// setting's value came from, and which config file was actually read.
//
// That record is what lets the server answer "why is this off?" rather than
// only "this is off". An operator who edits a config file the server never
// looked at gets a server behaving exactly as if the file did not exist, and
// until the file it read is named in the log, those two are the same silence.
//
// Provenance is keyed by the CONFIG-FILE spelling (underscores), not the flag
// spelling (hyphens), because the config file keys are the names the feature
// registry and the API use. One vocabulary, as the config package already
// insists.
func resolveAll(configPath string, flagged config.Config, given map[string]bool) (config.Resolved, error) {
	res := config.Resolved{
		Config:     config.Defaults(),
		Provenance: map[string]config.Source{},
	}

	// Two cases that look alike and are not: a config file NAMED on the command
	// line and missing is a mistake and stops the server, while the
	// conventional file simply not being there is the normal first run and is
	// silent. A malformed file is fatal either way - a settings file the server
	// could not read is not a settings file it should guess around.
	load := func(path string, optional bool) error {
		keys, err := config.LoadFileTracked(&res.Config, path)
		if err != nil {
			if optional && errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			return err
		}
		res.File = path
		for _, k := range keys {
			res.Provenance[k] = config.FromFile
		}
		return nil
	}

	if configPath != "" {
		if err := load(configPath, false); err != nil {
			return res, err
		}
	} else if err := load(config.DefaultPath, true); err != nil {
		return res, err
	}

	flagKeys := map[string]string{
		"listen":          "listen",
		"songs":           "songs",
		"data":            "data",
		"pack-cache-max":  "pack_cache_max",
		"browse-ui":       "browse_ui",
		"check-uploads":   "check_uploads",
		"check-max-bytes": "check_max_bytes",
	}
	apply := func(name string, set func()) {
		if !given[name] {
			return
		}
		set()
		res.Provenance[flagKeys[name]] = config.FromFlag
	}

	apply("listen", func() { res.Listen = flagged.Listen })
	apply("songs", func() { res.Songs = flagged.Songs })
	apply("data", func() { res.Data = flagged.Data })
	apply("pack-cache-max", func() { res.PackCacheMax = flagged.PackCacheMax })
	apply("browse-ui", func() { res.BrowseUI = flagged.BrowseUI })
	apply("check-uploads", func() { res.CheckUploads = flagged.CheckUploads })
	apply("check-max-bytes", func() { res.CheckMaxBytes = flagged.CheckMaxBytes })
	apply("config-writes", func() { res.ConfigWrites = flagged.ConfigWrites })

	return res, nil
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

func run(opt config.Resolved, log *slog.Logger) error {
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

	packs, err := packcache.New(filepath.Join(opt.Data, "packs"),
		packcache.WithMaxBytes(opt.PackCacheMax))
	if err != nil {
		return err
	}

	// Where an upload is staged while it is scanned. Created eagerly rather
	// than on the first request, so an unwritable data disk is a start-up
	// failure an operator sees immediately instead of a 500 the first time
	// somebody uses the feature.
	var checkDir string
	if opt.CheckUploads {
		checkDir = filepath.Join(opt.Data, "check")
		if err := os.MkdirAll(checkDir, 0o755); err != nil {
			return fmt.Errorf("check staging directory: %w", err)
		}
		// Anything left behind by a previous run is not ours to keep: this
		// endpoint promises to keep nothing, and a crash mid-scan is exactly
		// when that promise would otherwise be broken silently.
		if n, err := sweepStale(checkDir); err != nil {
			log.Warn("could not sweep staged uploads", "dir", checkDir, "err", err)
		} else if n > 0 {
			log.Info("swept staged uploads left by a previous run", "files", n)
		}
	}
	// Say the bound out loud at start. An operator who set it wrongly, or who
	// deliberately turned it off, should be able to see which from the log
	// rather than by watching a disk fill.
	if opt.PackCacheMax > 0 {
		log.Info("pack cache bounded", "max_bytes", opt.PackCacheMax)
	} else {
		log.Warn("pack cache is UNBOUNDED; it can grow to the size of the loose part of the library")
	}

	api := &httpapi.Server{
		Store:         library.NewStore(ix),
		Packs:         packs,
		Version:       version,
		Log:           log,
		BrowseUI:      opt.BrowseUI,
		CheckUploads:  opt.CheckUploads,
		CheckMaxBytes: opt.CheckMaxBytes,
		// Staged on the DATA disk, not in the library and not in the OS temp
		// directory. The library is read-only in normal operation - on the live
		// deployment it is literally mounted ro - and /tmp on a container image
		// this small may be memory-backed, where a 128 MiB upload is a very
		// different cost than it looks.
		CheckDir: checkDir,
		Features: opt.Features(),
		Writes:   opt.ConfigWrites,
	}

	// Name the config file that was actually read, or say that none was.
	//
	// This is the one thing the old startup log could not tell you. A file the
	// server never looked at - wrong path, wrong working directory, a container
	// whose bind mount landed elsewhere - produces a server that behaves
	// exactly as if the file did not exist, and says nothing about it. The
	// operator then reads their own file, sees check_uploads = yes, and gets a
	// 404. One line ends that.
	if opt.File != "" {
		log.Info("config file loaded", "path", opt.File)
	} else {
		log.Info("no config file read; using defaults and flags only",
			"looked_for", config.DefaultPath, "in", workingDir())
	}
	// Say what shape this server is, once, at start - EVERY optional
	// capability, on or off, and which of the three sources decided it.
	//
	// Driven from the registry rather than written out by hand, so a capability
	// added later cannot be silently absent from the log: the thing that
	// enables it and the thing that reports it are now the same list. An
	// operator who cannot find the page needs to know whether it is off or
	// whether they are on the wrong port, and guessing between those costs more
	// than a line of log. A feature that is OFF is exactly the case where the
	// line is worth most, which is why "off" is logged too.
	for _, f := range api.Features {
		if f.Enabled {
			log.Info("feature enabled", "feature", f.Name, "at", f.Endpoint, "source", f.Source)
		} else {
			log.Info("feature disabled", "feature", f.Name,
				"enable_with", f.EnableWith, "source", f.Source)
		}
	}

	if opt.CheckUploads {
		log.Info("upload check limits", "max_bytes", opt.CheckMaxBytes, "staged_in", checkDir)
	}

	// Say who can reconfigure this server, every time, including when the
	// answer is nobody. An operator who turned this on should see it confirmed,
	// and one who did not should be able to prove it from the log rather than
	// from a config file they may not be looking at.
	switch opt.ConfigWrites {
	case config.WritesLAN:
		log.Warn("features can be changed over HTTP BY ANY CALLER on this network",
			"config_writes", "lan", "at", "PUT /api/v1/features/{name}")
	case config.WritesLocal:
		log.Info("features can be changed over HTTP from this machine only",
			"config_writes", "local", "at", "PUT /api/v1/features/{name}")
	default:
		log.Info("features cannot be changed over HTTP",
			"config_writes", "off", "enable_with", "config_writes = local")
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

// sweepStale removes whatever a previous run left in the upload staging
// directory.
//
// The handler deletes its own file in a defer, so this only ever finds
// something after a crash or a kill mid-scan. It exists because "keeps
// nothing" is a promise, and a promise that holds only when the process exits
// cleanly is not the promise that was made.
func sweepStale(dir string) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasPrefix(e.Name(), "check-") {
			continue
		}
		if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
			n++
		}
	}
	return n, nil
}

// workingDir is only ever used to make a log line actionable: "I looked for
// yarg-song-server.conf HERE" is the sentence that ends the hunt. It reports
// "?" rather than failing, because not being able to name the directory must
// never be the thing that stops a server starting.
func workingDir() string {
	wd, err := os.Getwd()
	if err != nil {
		return "?"
	}
	return wd
}

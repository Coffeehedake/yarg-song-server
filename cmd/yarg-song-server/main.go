// Command yarg-song-server serves a shared YARG song library over HTTP.
//
// Nothing is implemented yet beyond process lifecycle and health. See
// docs/ROADMAP.md for the build order.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// version is set at build time via -ldflags.
var version = "dev"

type options struct {
	listen string
	songs  string
	data   string
}

func main() {
	var opt options
	flag.StringVar(&opt.listen, "listen", ":8080", "address to listen on")
	flag.StringVar(&opt.songs, "songs", "./songs", "path to the song library")
	flag.StringVar(&opt.data, "data", "./data", "path for catalog and server state")
	showVersion := flag.Bool("version", false, "print version and exit")
	flag.Parse()

	if *showVersion {
		fmt.Println(version)
		return
	}

	// `yarg-song-server scan <path>` walks a library and prints the catalog as
	// JSON. It exists so the scanner can be pointed at a real song collection
	// before any of it is behind an HTTP API - the parsers are only as good as
	// the charts they have actually been run against.
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

	if err := run(opt, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
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

func run(opt options, log *slog.Logger) error {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		_, _ = w.Write([]byte("ok\n"))
	})

	mux.HandleFunc("GET /version", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]string{"version": version})
	})

	srv := &http.Server{
		Addr:              opt.listen,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		log.Info("listening", "addr", opt.listen, "songs", opt.songs, "data", opt.data, "version", version)
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

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

	log := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelInfo}))

	if err := run(opt, log); err != nil {
		log.Error("fatal", "err", err)
		os.Exit(1)
	}
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

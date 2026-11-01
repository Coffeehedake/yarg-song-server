// Command yarn-install fetches YARG setlists from YARN into a songs library.
//
//	yarn-install -library /mnt/user/YARG/songs -list
//	yarn-install -library /mnt/user/YARG/songs -dry-run
//	yarn-install -library /mnt/user/YARG/songs -all
//	yarn-install -library /mnt/user/YARG/songs -only "YARN Pack 1,Assorted Rock 2"
//
// It runs on the HOST rather than in the server container, because the container mounts the
// library read-only on purpose - the server serves songs and does not write them. This is the
// one tool that writes, which is why it is a separate binary rather than a flag on the server.
//
// Installing is idempotent: a pack already present at the published tag is skipped without
// fetching anything, so a run can be repeated or interrupted freely.
package main

import (
	"context"
	"flag"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/yarn"
)

func main() {
	var (
		library = flag.String("library", "", "songs library to install into (required)")
		work    = flag.String("work", "", "staging directory; defaults to <library>/.yarn-staging so the final move is a rename, not a copy")
		all     = flag.Bool("all", false, "install every setlist that is missing or out of date")
		only    = flag.String("only", "", "comma-separated pack names to install")
		list    = flag.Bool("list", false, "list what YARN offers and what is already installed, then exit")
		dryRun  = flag.Bool("dry-run", false, "report what would be downloaded, including total size, without writing anything")
	)
	flag.Parse()

	if *library == "" {
		fmt.Fprintln(os.Stderr, "-library is required")
		flag.Usage()
		os.Exit(2)
	}

	// Ctrl-C leaves the library consistent: an install in progress is staged elsewhere and
	// only renamed in when complete, so cancelling mid-download loses the download and
	// nothing else.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if err := run(ctx, *library, *work, *all, *only, *list, *dryRun); err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, library, work string, all bool, only string, list, dryRun bool) error {
	client := yarn.NewClient()

	entries, err := client.Index(ctx)
	if err != nil {
		return fmt.Errorf("read the YARN index: %w", err)
	}

	var setlists []yarn.Entry
	for _, e := range entries {
		if e.IsSetlist() {
			setlists = append(setlists, e)
		}
	}
	sort.Slice(setlists, func(i, j int) bool { return setlists[i].Name < setlists[j].Name })
	fmt.Printf("YARN offers %d setlists (%d entries in total)\n", len(setlists), len(entries))

	wanted := map[string]bool{}
	for _, n := range strings.Split(only, ",") {
		if n = strings.TrimSpace(n); n != "" {
			wanted[strings.ToLower(n)] = true
		}
	}

	if list || dryRun {
		return report(ctx, client, library, setlists, wanted, dryRun)
	}

	if !all && len(wanted) == 0 {
		return fmt.Errorf("nothing to do: pass -all, -only, -list or -dry-run")
	}

	if work == "" {
		work = library + string(os.PathSeparator) + ".yarn-staging"
	}
	in := &yarn.Installer{
		Client:     client,
		LibraryDir: library,
		WorkDir:    work,
		HTTP:       &http.Client{},
	}

	var installed, skipped, failed int
	var songs int
	start := time.Now()

	for _, e := range setlists {
		if len(wanted) > 0 && !wanted[strings.ToLower(e.Name)] {
			continue
		}
		if ctx.Err() != nil {
			fmt.Println("cancelled; the library is unchanged by the interrupted pack")
			break
		}

		fmt.Printf("  %-34s ", truncate(e.Name, 34))
		res, err := in.Install(ctx, e)
		if err != nil {
			// One bad pack must not end the run: the next one may be fine, and a partial
			// library is the normal state of an interrupted bulk install anyway.
			fmt.Printf("FAILED: %v\n", err)
			failed++
			continue
		}
		if res.Skipped {
			fmt.Printf("already at %s\n", res.Tag)
			skipped++
			continue
		}
		fmt.Printf("installed %3d songs  %s\n", res.Songs, humanBytes(res.Bytes))
		installed++
		songs += res.Songs
	}

	fmt.Printf("\n%d installed (%d songs), %d already present, %d failed, in %s\n",
		installed, songs, skipped, failed, time.Since(start).Round(time.Second))
	if failed > 0 {
		return fmt.Errorf("%d pack(s) failed", failed)
	}
	return nil
}

// report prints what is installed and what is not. With dryRun it also asks each archive how
// big it is, so an operator sees the bill before agreeing to it.
func report(ctx context.Context, client *yarn.Client, library string, setlists []yarn.Entry, wanted map[string]bool, dryRun bool) error {
	var missing, present int
	var totalBytes int64

	for _, e := range setlists {
		if len(wanted) > 0 && !wanted[strings.ToLower(e.Name)] {
			continue
		}

		p, err := client.Profile(ctx, e.URL)
		if err != nil {
			fmt.Printf("  %-34s profile unreadable: %v\n", truncate(e.Name, 34), err)
			continue
		}

		dir := library + string(os.PathSeparator) + sanitize(p.Name())
		m, err := yarn.ReadManifest(dir)
		if err != nil {
			return err
		}

		status := "MISSING"
		switch {
		case m != nil && m.Tag == p.Tag():
			status = "installed"
			present++
		case m != nil:
			status = "out of date (" + m.Tag + ")"
			missing++
		default:
			missing++
		}

		size := ""
		if dryRun && status != "installed" {
			n, err := archiveBytes(ctx, p)
			if err == nil {
				totalBytes += n
				size = "  " + humanBytes(n)
			}
		}

		fmt.Printf("  %-34s %-28s %3d songs%s\n", truncate(p.Name(), 34), status, len(p.Songs()), size)
	}

	fmt.Printf("\n%d installed, %d missing or out of date\n", present, missing)
	if dryRun {
		fmt.Printf("downloading the missing ones would fetch %s\n", humanBytes(totalBytes))
	}
	return nil
}

// archiveBytes asks for each archive's size without downloading it.
func archiveBytes(ctx context.Context, p *yarn.Profile) (int64, error) {
	var total int64
	for _, f := range p.Files() {
		req, err := http.NewRequestWithContext(ctx, http.MethodHead, f.URL, nil)
		if err != nil {
			return 0, err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return 0, err
		}
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return 0, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		total += resp.ContentLength
	}
	return total, nil
}

// sanitize mirrors the installer's directory naming so -list looks in the right place. It is
// deliberately a copy rather than an export: the rule belongs to the installer, and a caller
// that needs to predict it is accepting that it may drift.
func sanitize(s string) string {
	const reserved = `/\:*?"<>|`
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r < 32 || strings.ContainsRune(reserved, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		out = "pack"
	}
	return out
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n-1] + "…"
}

func humanBytes(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for v := n / unit; v >= unit; v /= unit {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}

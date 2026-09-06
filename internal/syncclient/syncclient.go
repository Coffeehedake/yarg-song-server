// Package syncclient pulls a server's library into an ordinary local songs
// folder, so an UNMODIFIED YARG can play from a shared library.
//
// This is the half of Phase 2 that makes the server useful. ADR-001 chose it
// deliberately over changing the client first: it proves the server end to end
// with zero risk to YARG, and it means the project is useful to other people
// long before any fork work lands. It also matches the precedent upstream has
// already set - the Official Setlist is delivered out-of-band by the YARC
// Launcher, not by the game.
//
// # Files are named by chart hash, and that is a deliberate contract
//
// Songs are written as "<chart_hash>.sng". It looks unfriendly in a file
// manager and it is the right choice for three reasons:
//
//   - The inventory has to be exact and cheap. "What do I already have?" must be
//     answerable from a directory listing; opening and hashing ten thousand
//     archives on every run would make the client unusable on a Pi.
//   - The filename is not what a player sees. YARG reads title and artist from
//     inside the archive, so nothing about the browse experience depends on it.
//   - It makes the sync idempotent. The same library synced twice is a no-op,
//     with no state file to get out of step with the folder.
//
// Anything in the destination folder that is NOT "<40 hex>.sng" is treated as
// the player's own and is never touched, never counted, and never pruned. A
// sync tool that eats songs a person put there by hand is worse than no sync
// tool.
package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// managedName matches exactly the files this client considers its own.
var managedName = regexp.MustCompile(`^[0-9a-f]{40}\.sng$`)

// Options configures one sync run.
type Options struct {
	// ServerURL is the base URL of a yarg-song-server, e.g. http://pi.local:8080
	ServerURL string
	// Dest is the local songs folder. It is created if missing.
	Dest string
	// DryRun reports what would happen and writes nothing.
	DryRun bool
	// Prune deletes managed files the server no longer has. Off by default:
	// deleting a player's songs because a server went away is not a default
	// anyone should get by accident.
	Prune bool

	HTTP *http.Client
	Log  *slog.Logger
}

// Failure is one song that could not be synced. Collected rather than returned,
// because one unreachable song must not abandon a sync of ten thousand.
type Failure struct {
	ChartHash string
	Err       error
}

// Result is what a run did.
type Result struct {
	LocalBefore  int
	ServerTotal  int
	Downloaded   []string
	AlreadyHad   int
	Pruned       []string
	Unmanaged    int
	Failures     []Failure
	BytesFetched int64
	Elapsed      time.Duration
}

type haveRequest struct {
	ChartHashes []string `json:"chart_hashes"`
}

type haveResponse struct {
	LibraryTotal int      `json:"library_total"`
	Missing      []string `json:"missing"`
	MissingCount int      `json:"missing_count"`
}

type choice struct {
	PackageHash string `json:"package_hash"`
	Name        string `json:"name"`
	Artist      string `json:"artist"`
	Charter     string `json:"charter"`
}

type choices struct {
	ChartHash string   `json:"chart_hash"`
	Packages  []choice `json:"packages"`
}

// Run performs one sync.
func Run(ctx context.Context, opt Options) (*Result, error) {
	started := time.Now()

	if opt.Log == nil {
		opt.Log = slog.New(slog.NewTextHandler(io.Discard, nil))
	}
	if opt.HTTP == nil {
		// No overall timeout: a large song on a slow home link is normal, and a
		// client that gives up mid-library is worse than a slow one. The
		// per-request deadlines that matter are the caller's context.
		opt.HTTP = &http.Client{}
	}
	base := strings.TrimRight(opt.ServerURL, "/")
	if base == "" {
		return nil, fmt.Errorf("syncclient: no server URL")
	}

	if err := os.MkdirAll(opt.Dest, 0o755); err != nil {
		return nil, fmt.Errorf("syncclient: destination: %w", err)
	}

	local, unmanaged, err := inventory(opt.Dest)
	if err != nil {
		return nil, err
	}
	res := &Result{LocalBefore: len(local), Unmanaged: unmanaged}

	have := make([]string, 0, len(local))
	for h := range local {
		have = append(have, h)
	}
	sort.Strings(have)

	missing, total, err := postHave(ctx, opt, base, have)
	if err != nil {
		return nil, err
	}
	res.ServerTotal = total
	res.AlreadyHad = len(local)

	opt.Log.Info("sync starting",
		"server", base, "dest", opt.Dest,
		"local", len(local), "unmanaged", unmanaged,
		"server_total", total, "missing", len(missing), "dry_run", opt.DryRun)

	for _, hash := range missing {
		select {
		case <-ctx.Done():
			return res, ctx.Err()
		default:
		}
		if opt.DryRun {
			res.Downloaded = append(res.Downloaded, hash)
			continue
		}
		n, err := fetchOne(ctx, opt, base, hash)
		if err != nil {
			opt.Log.Warn("could not sync song", "chart_hash", hash, "err", err)
			res.Failures = append(res.Failures, Failure{ChartHash: hash, Err: err})
			continue
		}
		res.BytesFetched += n
		res.Downloaded = append(res.Downloaded, hash)
		opt.Log.Info("synced", "chart_hash", hash, "bytes", n)
	}

	if opt.Prune {
		pruned, err := prune(ctx, opt, base, local)
		if err != nil {
			return res, err
		}
		res.Pruned = pruned
	}

	res.Elapsed = time.Since(started)
	return res, nil
}

// inventory reads what is already here. Only "<40 hex>.sng" counts as ours.
func inventory(dest string) (map[string]struct{}, int, error) {
	entries, err := os.ReadDir(dest)
	if err != nil {
		return nil, 0, fmt.Errorf("syncclient: read destination: %w", err)
	}
	local := make(map[string]struct{})
	unmanaged := 0
	for _, e := range entries {
		if e.IsDir() {
			unmanaged++
			continue
		}
		name := e.Name()
		if !managedName.MatchString(name) {
			// Leftover .part files are ours but incomplete; they are neither
			// inventory nor a stranger's file.
			if !strings.HasSuffix(name, ".part") {
				unmanaged++
			}
			continue
		}
		local[strings.TrimSuffix(name, ".sng")] = struct{}{}
	}
	return local, unmanaged, nil
}

func postHave(ctx context.Context, opt Options, base string, have []string) ([]string, int, error) {
	body, err := json.Marshal(haveRequest{ChartHashes: have})
	if err != nil {
		return nil, 0, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/api/v1/have", bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := opt.HTTP.Do(req)
	if err != nil {
		return nil, 0, fmt.Errorf("syncclient: asking the server what we are missing: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, 0, fmt.Errorf("syncclient: POST /api/v1/have returned %s", resp.Status)
	}
	var out haveResponse
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, 0, fmt.Errorf("syncclient: malformed /have response: %w", err)
	}
	return out.Missing, out.LibraryTotal, nil
}

// fetchOne downloads, VERIFIES and installs one song.
//
// The verification is the point. The file is written to a .part, opened as a
// .sng, scanned, and its chart hash compared to the hash we asked for. Only then
// is it renamed into place. A truncated download, a proxy error page, or a
// server that handed back the wrong song therefore never becomes a file YARG
// will try to read - and because identity is SHA1 of the chart bytes, this is
// the same question the server answered, asked again independently.
func fetchOne(ctx context.Context, opt Options, base, hash string) (int64, error) {
	url := base + "/song/" + hash + ".sng"

	resp, err := get(ctx, opt, url)
	if err != nil {
		return 0, err
	}
	if resp.StatusCode == http.StatusMultipleChoices {
		pkg, err := choosePackage(resp)
		resp.Body.Close()
		if err != nil {
			return 0, err
		}
		opt.Log.Info("chart hash is shared by several packages; choosing deterministically",
			"chart_hash", hash, "package_hash", pkg)
		resp, err = get(ctx, opt, url+"?package="+pkg)
		if err != nil {
			return 0, err
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return 0, fmt.Errorf("GET %s returned %s", url, resp.Status)
	}

	part := filepath.Join(opt.Dest, hash+".sng.part")
	final := filepath.Join(opt.Dest, hash+".sng")

	f, err := os.Create(part)
	if err != nil {
		return 0, err
	}
	n, err := io.Copy(f, resp.Body)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(part)
		return 0, err
	}

	if err := verify(part, hash); err != nil {
		os.Remove(part)
		return 0, err
	}
	if err := os.Rename(part, final); err != nil {
		os.Remove(part)
		return 0, err
	}
	return n, nil
}

// verify opens a downloaded archive and asserts it really is the song we asked
// for, by the client's own reading rather than the server's word.
func verify(path, wantHash string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		return err
	}
	a, err := sng.Open(f, st.Size())
	if err != nil {
		return fmt.Errorf("downloaded file is not a readable .sng: %w", err)
	}
	song, err := scan.ScanArchive(a)
	if err != nil {
		return fmt.Errorf("downloaded .sng could not be scanned: %w", err)
	}
	if song.ChartHash != wantHash {
		return fmt.Errorf("identity mismatch: asked for %s, got %s", wantHash, song.ChartHash)
	}
	return nil
}

// choosePackage picks one package when a chart hash is shared by several.
//
// The server deliberately refuses to choose, because choosing would hand
// different clients different audio for the same request. Somebody has to, so
// the client does it by the lowest package hash: arbitrary, but STABLE, so two
// machines syncing the same library end up with the same file rather than
// disagreeing forever.
func choosePackage(resp *http.Response) (string, error) {
	var c choices
	if err := json.NewDecoder(resp.Body).Decode(&c); err != nil {
		return "", fmt.Errorf("malformed 300 response: %w", err)
	}
	if len(c.Packages) == 0 {
		return "", fmt.Errorf("server reported several packages but listed none")
	}
	best := c.Packages[0].PackageHash
	for _, p := range c.Packages[1:] {
		if p.PackageHash < best {
			best = p.PackageHash
		}
	}
	return best, nil
}

// prune removes managed files the server no longer has.
//
// It costs a second, larger /have call: asking with an EMPTY list returns the
// server's whole set, which is the only way to learn what is NOT there. The
// ordinary path does not pay that, which is why prune is opt-in rather than
// merely off by default.
func prune(ctx context.Context, opt Options, base string, local map[string]struct{}) ([]string, error) {
	all, _, err := postHave(ctx, opt, base, nil)
	if err != nil {
		return nil, fmt.Errorf("syncclient: prune needs the server's full list: %w", err)
	}
	onServer := make(map[string]struct{}, len(all))
	for _, h := range all {
		onServer[h] = struct{}{}
	}

	var pruned []string
	for h := range local {
		if _, ok := onServer[h]; ok {
			continue
		}
		pruned = append(pruned, h)
		if opt.DryRun {
			continue
		}
		if err := os.Remove(filepath.Join(opt.Dest, h+".sng")); err != nil {
			opt.Log.Warn("could not prune", "chart_hash", h, "err", err)
		}
	}
	sort.Strings(pruned)
	return pruned, nil
}

func get(ctx context.Context, opt Options, url string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	return opt.HTTP.Do(req)
}

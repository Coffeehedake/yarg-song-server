// Package e2e wires the real server handler to the real sync client and runs a
// whole sync against a real song library on disk.
//
// The unit tests in internal/syncclient talk to hand-written HTTP stubs, which
// prove the client's own logic but cannot catch the class of bug that matters
// most here: the server and the client disagreeing about the wire. A stub says
// whatever the test author believed the server says. This file uses
// httpapi.Server, library.Build and packcache -- the same code the binary runs
// -- so a change to either side that breaks the pair fails here.
//
// It deliberately does NOT shell out to the built binaries. Exercising the
// client in-process keeps the suite independent of whatever a virus scanner
// makes of a freshly linked executable on any given day -- on 2026-09-05 a
// Defender machine-learning verdict quarantined cmd/yarg-sync builds for about
// a minute before the classifier revised itself, which would have made a
// shell-out test red for a reason having nothing to do with this code.
// See docs/SYNC-CLIENT.md.
package e2e

import (
	"context"
	"fmt"
	"io"
	"log/slog"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/httpapi"
	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/packcache"
	"github.com/coffeehedake/yarg-song-server/internal/scan"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
	"github.com/coffeehedake/yarg-song-server/internal/syncclient"
)

type song struct {
	dir    string
	seed   string // distinct seed => distinct chart => distinct identity
	name   string
	artist string
}

// corpus covers the cases that have actually bitten this project: a plain
// ASCII song, a title needing diacritic folding, an article-leading title, and
// a folder name with spaces and punctuation that has to survive being indexed,
// packed on demand, and named by hash on the client.
func corpus() []song {
	return []song{
		{dir: "blur", seed: "a", name: "Song 2", artist: "Blur"},
		{dir: "bjork", seed: "b", name: "Jóga", artist: "Björk"},
		{dir: "the beatles - yesterday", seed: "c", name: "Yesterday", artist: "The Beatles"},
		{dir: "AC_DC (live!)", seed: "d", name: "T.N.T.", artist: "AC/DC"},
	}
}

func chartFor(seed string) string {
	return fmt.Sprintf("[Song]\n{\n  Resolution = 192\n  Name = \"%s\"\n}\n[ExpertSingle]\n{\n  768 = N 0 0\n  864 = N 1 0\n}\n", seed)
}

func writeCorpus(t *testing.T, root string, songs []song) {
	t.Helper()
	for _, s := range songs {
		dir := filepath.Join(root, s.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		ini := fmt.Sprintf("[Song]\nname = %s\nartist = %s\ncharter = e2e\n", s.name, s.artist)
		write(t, filepath.Join(dir, "song.ini"), ini)
		write(t, filepath.Join(dir, "notes.chart"), chartFor(s.seed))
		write(t, filepath.Join(dir, "song.ogg"), "audio-"+s.seed)
	}
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// serve indexes root and stands up the real API handler over it.
func serve(t *testing.T, root string) (*httptest.Server, *library.Store) {
	t.Helper()
	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	packs, err := packcache.New(filepath.Join(t.TempDir(), "packs"))
	if err != nil {
		t.Fatal(err)
	}
	store := library.NewStore(ix)
	api := &httpapi.Server{Store: store, Packs: packs, Version: "e2e"}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, store
}

func run(t *testing.T, srv *httptest.Server, dest string, opts ...func(*syncclient.Options)) *syncclient.Result {
	t.Helper()
	o := syncclient.Options{
		ServerURL: srv.URL,
		Dest:      dest,
		HTTP:      srv.Client(),
		Log:       slog.New(slog.NewTextHandler(io.Discard, nil)),
	}
	for _, f := range opts {
		f(&o)
	}
	res, err := syncclient.Run(context.Background(), o)
	if err != nil {
		t.Fatalf("sync failed: %v", err)
	}
	if len(res.Failures) > 0 {
		t.Fatalf("sync reported failures: %v", res.Failures)
	}
	return res
}

// managedName reports whether a directory entry is one of ours: 40 hex
// characters plus ".sng" is 44.
func managed(name string) bool {
	return len(name) == 44 && strings.HasSuffix(name, ".sng")
}

// sngNames lists the managed archives in dir, sorted.
func sngNames(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() && managed(e.Name()) {
			out = append(out, e.Name())
		}
	}
	sort.Strings(out)
	return out
}

func identityOf(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	st, err := f.Stat()
	if err != nil {
		t.Fatal(err)
	}
	a, err := sng.Open(f, st.Size())
	if err != nil {
		t.Fatalf("%s is not a readable .sng: %v", filepath.Base(path), err)
	}
	s, err := scan.ScanArchive(a)
	if err != nil {
		t.Fatalf("%s could not be scanned: %v", filepath.Base(path), err)
	}
	return s.ChartHash
}

// TestWholeSyncRoundTrip is the headline: a fresh client ends up holding every
// song the server has, each one readable and each one the song it is named
// after. Identity is checked by re-deriving it from the downloaded bytes, not
// by trusting the file name the server chose.
func TestWholeSyncRoundTrip(t *testing.T) {
	root := t.TempDir()
	songs := corpus()
	writeCorpus(t, root, songs)
	srv, _ := serve(t, root)

	dest := t.TempDir()
	res := run(t, srv, dest)

	if res.ServerTotal != len(songs) {
		t.Fatalf("server reported %d songs, corpus has %d", res.ServerTotal, len(songs))
	}
	if len(res.Downloaded) != len(songs) {
		t.Fatalf("downloaded %d songs, expected %d", len(res.Downloaded), len(songs))
	}
	if res.BytesFetched <= 0 {
		t.Fatal("reported zero bytes fetched after downloading songs")
	}

	names := sngNames(t, dest)
	if len(names) != len(songs) {
		t.Fatalf("dest holds %d archives, expected %d: %v", len(names), len(songs), names)
	}

	seen := make(map[string]bool)
	for _, n := range names {
		want := strings.TrimSuffix(n, ".sng")
		got := identityOf(t, filepath.Join(dest, n))
		if got != want {
			t.Errorf("%s: file claims identity %s but scans as %s", n, want, got)
		}
		seen[got] = true
	}
	if len(seen) != len(songs) {
		t.Fatalf("expected %d distinct identities, got %d", len(songs), len(seen))
	}
}

// TestSecondRunDownloadsNothing is the property that makes this safe to run on
// a schedule: sync is idempotent, and a client that is up to date transfers no
// song bytes at all.
func TestSecondRunDownloadsNothing(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, corpus())
	srv, _ := serve(t, root)

	dest := t.TempDir()
	first := run(t, srv, dest)
	second := run(t, srv, dest)

	if len(second.Downloaded) != 0 {
		t.Errorf("second run downloaded %d songs, expected none", len(second.Downloaded))
	}
	if second.BytesFetched != 0 {
		t.Errorf("second run fetched %d song bytes, expected 0", second.BytesFetched)
	}
	if second.AlreadyHad != len(first.Downloaded) {
		t.Errorf("second run recognised %d local songs, expected %d", second.AlreadyHad, len(first.Downloaded))
	}
	if names := sngNames(t, dest); len(names) != len(first.Downloaded) {
		t.Errorf("second run changed the folder: %d archives, expected %d", len(names), len(first.Downloaded))
	}
}

// snapshot records every path under dir that the client does not manage, with
// its contents, so a later comparison can prove none of it moved.
func snapshot(t *testing.T, dir string) map[string]string {
	t.Helper()
	out := map[string]string{}
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() || managed(d.Name()) {
			return nil
		}
		rel, err := filepath.Rel(dir, path)
		if err != nil {
			return err
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return out
}

// TestThePlayersOwnSongsAreNeverTouched is the guarantee the client makes to
// anyone who points it at a folder they already use. Nothing that is not a
// 40-hex .sng of ours is moved, rewritten or deleted -- including on a prune,
// which is the run with the most licence to destroy things.
func TestThePlayersOwnSongsAreNeverTouched(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, corpus())
	srv, store := serve(t, root)

	dest := t.TempDir()

	// A player's existing folder: a loose song folder, a hand-named archive, a
	// stray note, and a name that is nearly ours but not quite.
	if err := os.MkdirAll(filepath.Join(dest, "My Own Chart"), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dest, "My Own Chart", "song.ini"), "[Song]\nname = Mine\n")
	write(t, filepath.Join(dest, "Favourite Song.sng"), "not really an sng")
	write(t, filepath.Join(dest, "notes.txt"), "remember to practise")
	write(t, filepath.Join(dest, "0123456789abcdef.sng"), "too short to be ours")

	before := snapshot(t, dest)

	first := run(t, srv, dest)
	if first.Unmanaged == 0 {
		t.Error("client reported no unmanaged items in a folder that has four")
	}

	// Drop a song from the server, then prune: the destructive path.
	if err := os.RemoveAll(filepath.Join(root, "blur")); err != nil {
		t.Fatal(err)
	}
	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	store.Replace(ix)

	res := run(t, srv, dest, func(o *syncclient.Options) { o.Prune = true })
	if len(res.Pruned) != 1 {
		t.Fatalf("prune removed %d songs, expected exactly 1: %v", len(res.Pruned), res.Pruned)
	}
	if names := sngNames(t, dest); len(names) != len(corpus())-1 {
		t.Errorf("after prune the folder holds %d managed archives, expected %d", len(names), len(corpus())-1)
	}

	after := snapshot(t, dest)
	for path, body := range before {
		got, ok := after[path]
		if !ok {
			t.Errorf("%s was deleted; it is not ours to delete", path)
			continue
		}
		if got != body {
			t.Errorf("%s was modified; it is not ours to modify", path)
		}
	}
	for path := range after {
		if _, ok := before[path]; !ok {
			t.Errorf("%s appeared in the folder and is not a managed archive", path)
		}
	}
}

// TestDryRunWritesNothing: the flag a cautious player reaches for first has to
// be worth reaching for.
func TestDryRunWritesNothing(t *testing.T) {
	root := t.TempDir()
	songs := corpus()
	writeCorpus(t, root, songs)
	srv, _ := serve(t, root)

	dest := t.TempDir()
	res := run(t, srv, dest, func(o *syncclient.Options) { o.DryRun = true })

	if len(res.Downloaded) != len(songs) {
		t.Errorf("dry run named %d songs, expected all %d", len(res.Downloaded), len(songs))
	}
	entries, err := os.ReadDir(dest)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("dry run wrote %d entries into the destination", len(entries))
	}
	if res.BytesFetched != 0 {
		t.Errorf("dry run fetched %d bytes", res.BytesFetched)
	}
}

// TestSharedChartHashSyncsOnce: two folders can hold the same chart under
// different packaging. Identity is the chart, so the client wants one copy and
// the server must not make it guess. The unit tests prove the choice is
// deterministic; this proves the pair actually completes the exchange, which is
// the part a stub cannot tell you -- the server answers 300 Multiple Choices
// here, and the client has to understand it.
func TestSharedChartHashSyncsOnce(t *testing.T) {
	root := t.TempDir()
	writeCorpus(t, root, corpus()[:1]) // just "blur"

	// A second packaging of the same chart: same notes.chart, different extras.
	dup := filepath.Join(root, "blur (deluxe)")
	if err := os.MkdirAll(dup, 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, filepath.Join(dup, "song.ini"), "[Song]\nname = Song 2\nartist = Blur\ncharter = someone else\n")
	write(t, filepath.Join(dup, "notes.chart"), chartFor("a"))
	write(t, filepath.Join(dup, "song.ogg"), "audio-a")
	write(t, filepath.Join(dup, "album.png"), "art")

	srv, _ := serve(t, root)
	dest := t.TempDir()
	res := run(t, srv, dest)

	if res.ServerTotal != 1 {
		t.Fatalf("server offered %d songs, expected 1 distinct chart", res.ServerTotal)
	}
	names := sngNames(t, dest)
	if len(names) != 1 {
		t.Fatalf("client holds %d archives, expected 1: %v", len(names), names)
	}
	if got := identityOf(t, filepath.Join(dest, names[0])); got != strings.TrimSuffix(names[0], ".sng") {
		t.Errorf("archive claims identity %s but scans as %s", strings.TrimSuffix(names[0], ".sng"), got)
	}
}

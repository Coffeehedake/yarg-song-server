package syncclient

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/scan"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// buildSong packs a real .sng with our own writer, so the client's verification
// step is exercised against genuine archives rather than a stub. Its chart
// bytes vary with seed, which is what makes each one a different song.
func buildSong(t *testing.T, seed string) (hash string, data []byte) {
	t.Helper()
	dir := t.TempDir()
	ini := "[Song]\nname = Song " + seed + "\nartist = Tester\n"
	chart := fmt.Sprintf("[Song]\n{\n  Resolution = 192\n  Name = \"%s\"\n}\n[ExpertSingle]\n{\n  768 = N 0 0\n}\n", seed)
	write(t, filepath.Join(dir, "song.ini"), ini)
	write(t, filepath.Join(dir, "notes.chart"), chart)
	write(t, filepath.Join(dir, "song.ogg"), "audio-"+seed)

	var buf bytes.Buffer
	if err := scan.PackDir(dir, &buf); err != nil {
		t.Fatal(err)
	}
	data = buf.Bytes()

	a, err := sng.Open(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatal(err)
	}
	song, err := scan.ScanArchive(a)
	if err != nil {
		t.Fatal(err)
	}
	return song.ChartHash, data
}

func write(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

// fakeServer speaks just enough of the real API. Handlers can be overridden per
// test to simulate the failures that matter.
type fakeServer struct {
	songs       map[string][]byte // chart hash -> .sng bytes
	songHandler func(w http.ResponseWriter, r *http.Request, hash string) bool
	haveCalls   int
}

func newFakeServer(t *testing.T, seeds ...string) (*httptest.Server, *fakeServer) {
	t.Helper()
	fs := &fakeServer{songs: map[string][]byte{}}
	for _, s := range seeds {
		h, d := buildSong(t, s)
		fs.songs[h] = d
	}

	mux := http.NewServeMux()
	mux.HandleFunc("POST /api/v1/have", func(w http.ResponseWriter, r *http.Request) {
		fs.haveCalls++
		var req haveRequest
		_ = json.NewDecoder(r.Body).Decode(&req)
		has := map[string]struct{}{}
		for _, h := range req.ChartHashes {
			has[strings.ToLower(h)] = struct{}{}
		}
		missing := []string{}
		for h := range fs.songs {
			if _, ok := has[h]; !ok {
				missing = append(missing, h)
			}
		}
		sort.Strings(missing)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(haveResponse{
			LibraryTotal: len(fs.songs), Missing: missing, MissingCount: len(missing),
		})
	})
	mux.HandleFunc("GET /song/{file}", func(w http.ResponseWriter, r *http.Request) {
		hash := strings.TrimSuffix(r.PathValue("file"), ".sng")
		if fs.songHandler != nil && fs.songHandler(w, r, hash) {
			return
		}
		data, ok := fs.songs[hash]
		if !ok {
			http.Error(w, "no such song", http.StatusNotFound)
			return
		}
		_, _ = w.Write(data)
	})

	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv, fs
}

func run(t *testing.T, srv *httptest.Server, dest string, tweak func(*Options)) *Result {
	t.Helper()
	opt := Options{ServerURL: srv.URL, Dest: dest}
	if tweak != nil {
		tweak(&opt)
	}
	res, err := Run(context.Background(), opt)
	if err != nil {
		t.Fatalf("run: %v", err)
	}
	return res
}

func listing(t *testing.T, dir string) []string {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	var names []string
	for _, e := range entries {
		names = append(names, e.Name())
	}
	sort.Strings(names)
	return names
}

func TestSyncsEverythingIntoAnEmptyFolder(t *testing.T) {
	srv, fs := newFakeServer(t, "a", "b", "c")
	dest := t.TempDir()

	res := run(t, srv, dest, nil)
	if len(res.Downloaded) != 3 || res.ServerTotal != 3 {
		t.Fatalf("downloaded %d of %d", len(res.Downloaded), res.ServerTotal)
	}
	if len(res.Failures) != 0 {
		t.Fatalf("failures: %+v", res.Failures)
	}

	got := listing(t, dest)
	if len(got) != 3 {
		t.Fatalf("folder holds %v", got)
	}
	for hash, want := range fs.songs {
		data, err := os.ReadFile(filepath.Join(dest, hash+".sng"))
		if err != nil {
			t.Fatalf("%s: %v", hash, err)
		}
		if !bytes.Equal(data, want) {
			t.Errorf("%s: bytes differ from what the server served", hash)
		}
	}
}

// The whole point of naming by chart hash: running twice must do nothing the
// second time, with no state file to fall out of step with the folder.
func TestSecondRunIsANoOp(t *testing.T) {
	srv, _ := newFakeServer(t, "a", "b")
	dest := t.TempDir()

	run(t, srv, dest, nil)
	before := listing(t, dest)

	res := run(t, srv, dest, nil)
	if len(res.Downloaded) != 0 {
		t.Fatalf("second run downloaded %d songs", len(res.Downloaded))
	}
	if res.AlreadyHad != 2 {
		t.Errorf("AlreadyHad = %d, want 2", res.AlreadyHad)
	}
	if strings.Join(listing(t, dest), "|") != strings.Join(before, "|") {
		t.Error("second run changed the folder")
	}
}

// A player's own songs must survive. Anything that is not <40 hex>.sng is not
// ours, is never counted as inventory, and is never removed.
func TestLeavesEverythingElseAlone(t *testing.T) {
	srv, _ := newFakeServer(t, "a")
	dest := t.TempDir()
	write(t, filepath.Join(dest, "My Favourite Song.sng"), "not ours")
	write(t, filepath.Join(dest, "notes.txt"), "also not ours")
	if err := os.Mkdir(filepath.Join(dest, "A Loose Folder"), 0o755); err != nil {
		t.Fatal(err)
	}

	res := run(t, srv, dest, func(o *Options) { o.Prune = true })
	if res.Unmanaged != 3 {
		t.Errorf("Unmanaged = %d, want 3", res.Unmanaged)
	}
	if len(res.Pruned) != 0 {
		t.Errorf("pruned %v - nothing of ours was stale", res.Pruned)
	}
	for _, name := range []string{"My Favourite Song.sng", "notes.txt", "A Loose Folder"} {
		if _, err := os.Stat(filepath.Join(dest, name)); err != nil {
			t.Errorf("%s was disturbed: %v", name, err)
		}
	}
}

// The verification step, and the reason it exists. A server that hands back
// something that is not a .sng must not leave a file YARG will try to read.
func TestCorruptDownloadIsRefused(t *testing.T) {
	srv, fs := newFakeServer(t, "a")
	fs.songHandler = func(w http.ResponseWriter, r *http.Request, hash string) bool {
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
		return true
	}
	dest := t.TempDir()

	res := run(t, srv, dest, nil)
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %+v, want exactly one", res.Failures)
	}
	if !strings.Contains(res.Failures[0].Err.Error(), "not a readable .sng") {
		t.Errorf("unhelpful error: %v", res.Failures[0].Err)
	}
	if got := listing(t, dest); len(got) != 0 {
		t.Fatalf("a bad download was left behind: %v", got)
	}
}

// The stronger case: a VALID .sng, but not the song we asked for. Only an
// independent identity check catches this, and identity is SHA1 of the chart
// bytes - the same question the server answered, asked again by the client.
func TestWrongSongIsRefused(t *testing.T) {
	srv, fs := newFakeServer(t, "a")
	_, otherData := buildSong(t, "completely-different")
	fs.songHandler = func(w http.ResponseWriter, r *http.Request, hash string) bool {
		_, _ = w.Write(otherData)
		return true
	}
	dest := t.TempDir()

	res := run(t, srv, dest, nil)
	if len(res.Failures) != 1 {
		t.Fatalf("failures = %+v, want exactly one", res.Failures)
	}
	if !strings.Contains(res.Failures[0].Err.Error(), "identity mismatch") {
		t.Errorf("error should name the mismatch: %v", res.Failures[0].Err)
	}
	if got := listing(t, dest); len(got) != 0 {
		t.Fatalf("the wrong song was installed: %v", got)
	}
}

// One unreachable song must not abandon a sync of the rest.
func TestOneFailureDoesNotStopTheRest(t *testing.T) {
	srv, fs := newFakeServer(t, "a", "b", "c")
	var broken string
	for h := range fs.songs {
		broken = h
		break
	}
	fs.songHandler = func(w http.ResponseWriter, r *http.Request, hash string) bool {
		if hash == broken {
			http.Error(w, "boom", http.StatusInternalServerError)
			return true
		}
		return false
	}
	dest := t.TempDir()

	res := run(t, srv, dest, nil)
	if len(res.Downloaded) != 2 || len(res.Failures) != 1 {
		t.Fatalf("downloaded %d, failed %d - want 2 and 1", len(res.Downloaded), len(res.Failures))
	}
	if _, err := os.Stat(filepath.Join(dest, broken+".sng")); !os.IsNotExist(err) {
		t.Error("the broken song should not be on disk")
	}
}

// The server refuses to choose between packages sharing a chart hash, so the
// client must - and must choose the SAME one every time, or two machines
// syncing the same library disagree forever.
func TestAmbiguousChartHashIsResolvedDeterministically(t *testing.T) {
	srv, fs := newFakeServer(t, "a")
	var hash string
	for h := range fs.songs {
		hash = h
	}
	offered := []choice{{PackageHash: "ffff"}, {PackageHash: "0001"}, {PackageHash: "aaaa"}}

	var chosen []string
	fs.songHandler = func(w http.ResponseWriter, r *http.Request, h string) bool {
		if pkg := r.URL.Query().Get("package"); pkg != "" {
			chosen = append(chosen, pkg)
			return false // fall through and serve the real bytes
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusMultipleChoices)
		_ = json.NewEncoder(w).Encode(choices{ChartHash: h, Packages: offered})
		return true
	}

	for i := 0; i < 2; i++ {
		dest := t.TempDir()
		res := run(t, srv, dest, nil)
		if len(res.Downloaded) != 1 || len(res.Failures) != 0 {
			t.Fatalf("run %d: downloaded %d, failures %+v", i, len(res.Downloaded), res.Failures)
		}
		if _, err := os.Stat(filepath.Join(dest, hash+".sng")); err != nil {
			t.Fatalf("run %d: song not installed: %v", i, err)
		}
	}
	if len(chosen) != 2 || chosen[0] != "0001" || chosen[1] != "0001" {
		t.Fatalf("package choices were %v, want the lowest hash both times", chosen)
	}
}

func TestDryRunWritesNothing(t *testing.T) {
	srv, _ := newFakeServer(t, "a", "b")
	dest := t.TempDir()

	res := run(t, srv, dest, func(o *Options) { o.DryRun = true })
	if len(res.Downloaded) != 2 {
		t.Fatalf("dry run reported %d songs, want 2", len(res.Downloaded))
	}
	if got := listing(t, dest); len(got) != 0 {
		t.Fatalf("dry run wrote %v", got)
	}
}

// Prune is opt-in, and it costs a second /have call because asking with an
// empty list is the only way to learn the server's whole set.
func TestPruneIsOptInAndCostsASecondCall(t *testing.T) {
	srv, fs := newFakeServer(t, "a")
	dest := t.TempDir()
	stale := strings.Repeat("d", 40)
	write(t, filepath.Join(dest, stale+".sng"), "stale but ours")

	before := fs.haveCalls
	res := run(t, srv, dest, nil)
	if len(res.Pruned) != 0 {
		t.Errorf("pruned without being asked: %v", res.Pruned)
	}
	if _, err := os.Stat(filepath.Join(dest, stale+".sng")); err != nil {
		t.Error("a stale file was removed without -prune")
	}
	if fs.haveCalls-before != 1 {
		t.Errorf("ordinary run made %d /have calls, want 1", fs.haveCalls-before)
	}

	before = fs.haveCalls
	res = run(t, srv, dest, func(o *Options) { o.Prune = true })
	if len(res.Pruned) != 1 || res.Pruned[0] != stale {
		t.Fatalf("pruned %v, want [%s]", res.Pruned, stale)
	}
	if _, err := os.Stat(filepath.Join(dest, stale+".sng")); !os.IsNotExist(err) {
		t.Error("prune did not remove the stale file")
	}
	if fs.haveCalls-before != 2 {
		t.Errorf("prune run made %d /have calls, want 2", fs.haveCalls-before)
	}
}

func TestNoServerURLIsAnError(t *testing.T) {
	if _, err := Run(context.Background(), Options{Dest: t.TempDir()}); err == nil {
		t.Fatal("an empty server URL should be refused")
	}
}

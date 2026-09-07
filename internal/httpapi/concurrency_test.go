package httpapi

// Every measurement this project took before 2026-09-07 was serial: one client,
// one request at a time. "A Pi on the LAN serves a shared library" is inherently
// concurrent, and the first probe under load found a real defect - so these
// tests exist to keep it found.
//
// As with the hostile-archive tests, the behaviour here was MEASURED with a
// throwaway probe that printed what happened, and only then asserted. The probe
// also got its own instrument wrong first: it compared every response's length
// to song 0's, and reported 480 of 640 "wrong size" for a library whose songs
// are deliberately different sizes. A failing measurement is not automatically
// a failing system, and the check below compares each song against its own
// serially-established hash for that reason.

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/packcache"
)

// concurrentServer builds a library of n distinct songs and serves it with the
// pack cache bounded to maxBytes (0 = unbounded). It returns the chart hashes so
// a test can request every song.
func concurrentServer(t *testing.T, n int, maxBytes int64) (*httptest.Server, string, []string, *serverLog) {
	t.Helper()
	root := t.TempDir()
	for i := 0; i < n; i++ {
		dir := filepath.Join(root, fmt.Sprintf("song-%03d", i))
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		mustWrite(t, filepath.Join(dir, "song.ini"), fmt.Sprintf("[Song]\nname = Song %d\nartist = Concurrency\n", i))
		mustWrite(t, filepath.Join(dir, "notes.chart"), chartFor(fmt.Sprintf("seed-%d", i)))
		// Large enough that a pack is not instantaneous - a pack that finishes
		// before the next request starts would test nothing about concurrency.
		mustWrite(t, filepath.Join(dir, "song.ogg"), strings.Repeat("audio", 16000))
	}
	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	packDir := filepath.Join(t.TempDir(), "packs")
	var opts []packcache.Option
	if maxBytes > 0 {
		opts = append(opts, packcache.WithMaxBytes(maxBytes))
	}
	packs, err := packcache.New(packDir, opts...)
	if err != nil {
		t.Fatal(err)
	}
	errs := &serverLog{}
	api := &Server{
		Store:   library.NewStore(ix),
		Packs:   packs,
		Version: "test",
		Log:     slog.New(slog.NewTextHandler(errs, &slog.HandlerOptions{Level: slog.LevelError})),
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/songs?limit=500")
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var page struct {
		Songs []struct {
			ChartHash string `json:"chart_hash"`
		} `json:"songs"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&page); err != nil {
		t.Fatal(err)
	}
	hashes := make([]string, 0, len(page.Songs))
	for _, s := range page.Songs {
		hashes = append(hashes, s.ChartHash)
	}
	if len(hashes) != n {
		t.Fatalf("indexed %d songs, want %d", len(hashes), n)
	}
	return srv, packDir, hashes, errs
}

// serverLog captures what the server logged, so a failing request can be
// reported with the error the HANDLER saw rather than only its status code.
//
// This exists because of a specific wasted hour. CI failed with "status=500
// bytes=37" and nothing else, and 500 has two causes on that path - the pack
// itself failing, and the packed archive being evicted before it could be
// opened. Both produce the same body, so which one it was had to be inferred,
// and inference is exactly what this project keeps getting wrong. The server
// already knew the answer and was throwing it away, because the test never
// gave it a logger.
type serverLog struct {
	mu    sync.Mutex
	lines []string
}

func (l *serverLog) Write(p []byte) (int, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.lines = append(l.lines, strings.TrimRight(string(p), "\n"))
	return len(p), nil
}

// sample returns up to n captured lines and how many there were in total.
func (l *serverLog) sample(n int) ([]string, int) {
	l.mu.Lock()
	defer l.mu.Unlock()
	total := len(l.lines)
	if total > n {
		return append([]string(nil), l.lines[:n]...), total
	}
	return append([]string(nil), l.lines...), total
}

// stressClient reuses connections on purpose.
//
// The first version of these tests used http.Get, which meant tens of thousands
// of separate connections; roughly one run in six then failed with a handful of
// requests whose status was 0 - a TRANSPORT error, not a server one, almost
// certainly ephemeral-port pressure on Windows. That is a flaky test blaming
// the wrong component, which is worse than no test. Pooling the connections
// removes the cause rather than tolerating a threshold of failures.
var stressClient = &http.Client{
	Transport: &http.Transport{
		MaxIdleConns:        256,
		MaxIdleConnsPerHost: 256,
		IdleConnTimeout:     30 * time.Second,
	},
}

// fetch returns the transport error separately from the status code. Collapsing
// them - returning 0 for "could not connect" - is what made the first failure
// here unreadable: a client-side problem looked identical to a server that had
// answered with nothing.
func fetch(t *testing.T, url string) (int, []byte, error) {
	t.Helper()
	r, err := stressClient.Get(url)
	if err != nil {
		return 0, nil, err
	}
	defer r.Body.Close()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		return r.StatusCode, b, err
	}
	return r.StatusCode, b, nil
}

// Many clients asking for the same UNCACHED song must pack it once and hand
// every one of them the same complete archive.
func TestManyClientsAskingForOneColdSongAllGetTheSameArchive(t *testing.T) {
	srv, packDir, hashes, _ := concurrentServer(t, 1, 0)
	const clients = 64

	var wg sync.WaitGroup
	sums := make([]string, clients)
	codes := make([]int, clients)
	for i := 0; i < clients; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			code, body, err := fetch(t, srv.URL+"/song/"+hashes[0]+".sng")
			if err != nil {
				t.Errorf("client %d: transport error: %v", i, err)
				return
			}
			codes[i] = code
			h := sha256.Sum256(body)
			sums[i] = hex.EncodeToString(h[:])
		}(i)
	}
	wg.Wait()

	distinct := map[string]int{}
	for i, s := range sums {
		if codes[i] != 200 {
			t.Errorf("client %d got %d, want 200", i, codes[i])
		}
		distinct[s]++
	}
	if len(distinct) != 1 {
		t.Errorf("got %d distinct archives for one song, want 1", len(distinct))
	}
	// One song, one cached archive - proof the shard lock collapsed the herd
	// rather than every client packing its own copy.
	entries, err := os.ReadDir(packDir)
	if err != nil {
		t.Fatal(err)
	}
	packs := 0
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".sng") {
			packs++
		}
	}
	if packs != 1 {
		t.Errorf("cache holds %d archives after %d concurrent requests for ONE song, want 1", packs, clients)
	}
}

// THE DEFECT THIS FILE EXISTS FOR.
//
// With the cache bounded far below the library, eviction runs constantly while
// songs are being served. Before 2026-09-07 the handler asked the cache for a
// PATH and then opened it as a separate step; an evicting goroutine could remove
// the archive in between, and the server answered 404 "this song is no longer
// where the index says it is; rescan" for a song that was perfectly present.
//
// Measured on Windows at 64 clients over a 40-song library bounded to two
// archives: 2, 1 and 0 spurious 404s across three runs of 2,560 requests. Rare,
// intermittent, and impossible to hit by hand - so it would have shipped.
//
// packcache.Open now returns an open handle, which closes that window. This test
// is the regression: every request must succeed AND return the same bytes the
// song packs to serially.
//
// Two things about this test are worth more than the test itself.
//
// It was TUNED against the defect rather than written and hoped over. At two
// archives and one pass it passed on the broken code - a regression test that
// catches the bug one run in three is decoration. One archive and three rounds
// makes it fail every time the caller-side window is reopened.
//
// And its first version blamed the wrong component. Using http.Get meant a
// fresh connection per request; roughly one run in six then failed with a few
// requests whose status was 0, which is a socket problem on the test machine
// and not a server losing a song. That noise was briefly mistaken for a second
// defect inside packcache - see the note there. fetch now reports transport
// errors separately for exactly that reason, and the client pools connections
// so they should not happen at all.
func TestEvictionUnderLoadNeverLosesASongOrChangesItsBytes(t *testing.T) {
	if testing.Short() {
		t.Skip("concurrency stress; skipped under -short")
	}
	const songs = 40
	const clients = 64

	// Serial baseline: what each song's bytes SHOULD be, with no concurrency
	// and no eviction pressure.
	base, _, baseHashes, _ := concurrentServer(t, songs, 0)
	want := map[string]string{}
	var oneSize int64
	for _, h := range baseHashes {
		code, body, err := fetch(t, base.URL+"/song/"+h+".sng")
		if err != nil {
			t.Fatalf("baseline %s: transport error: %v", h, err)
		}
		if code != 200 {
			t.Fatalf("baseline %s: got %d", h, code)
		}
		sum := sha256.Sum256(body)
		want[h] = hex.EncodeToString(sum[:])
		if oneSize == 0 {
			oneSize = int64(len(body))
		}
	}

	// Bound the cache to ONE archive against a forty-archive library, so almost
	// every request evicts something, and loop each client over the whole
	// library several times. Both numbers were tuned against the defect rather
	// than picked: at two archives and one pass this test passed on the BROKEN
	// code, which would have made it decoration. See the note above the func.
	srv, _, hashes, srvErrs := concurrentServer(t, songs, oneSize)
	const rounds = 3

	var wg sync.WaitGroup
	var mu sync.Mutex
	var failures []string
	total, transport := 0, 0

	for c := 0; c < clients; c++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for r := 0; r < rounds; r++ {
				for _, h := range hashes {
					code, body, err := fetch(t, srv.URL+"/song/"+h+".sng")
					if err == nil {
						sum := sha256.Sum256(body)
						if code == 200 && hex.EncodeToString(sum[:]) == want[h] {
							continue
						}
					}
					mu.Lock()
					total++
					switch {
					case err != nil:
						transport++
						if len(failures) < 10 {
							failures = append(failures, fmt.Sprintf("%s: TRANSPORT %v", h[:8], err))
						}
					default:
						if len(failures) < 10 {
							failures = append(failures, fmt.Sprintf("%s: status=%d bytes=%d", h[:8], code, len(body)))
						}
					}
					mu.Unlock()
				}
			}
		}()
	}
	wg.Wait()

	if len(failures) > 0 {
		// Name the category. A transport error here is the harness or the OS
		// running out of sockets, NOT the server losing a song, and reporting
		// the two the same way is how a flaky test gets blamed on the code it
		// was meant to protect.
		logged, nLogged := srvErrs.sample(5)
		t.Errorf("%d of %d requests failed under eviction (%d of them transport-level); first few: %v\nserver logged %d error(s); first few: %v",
			total, clients*songs*rounds, transport, failures, nLogged, logged)
	}
}

// browseServer is newTestServerWithPacks with the browse page switched on. It
// lives here beside the other helpers rather than in web_test.go so there is one
// place that knows how to stand a Server up.
func browseServer(t *testing.T, on bool) *httptest.Server {
	t.Helper()
	root := t.TempDir()
	dir := filepath.Join(root, "song")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	mustWrite(t, filepath.Join(dir, "song.ini"), "[Song]\nname = One\nartist = A\n")
	mustWrite(t, filepath.Join(dir, "notes.chart"), chartFor("browse"))
	mustWrite(t, filepath.Join(dir, "song.ogg"), "audio")
	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	packs, err := packcache.New(filepath.Join(t.TempDir(), "packs"))
	if err != nil {
		t.Fatal(err)
	}
	api := &Server{Store: library.NewStore(ix), Packs: packs, Version: "test", BrowseUI: on}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv
}

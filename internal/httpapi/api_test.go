package httpapi

import (
	"bytes"
	"crypto/sha1"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/packcache"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

func chartFor(seed string) string {
	return fmt.Sprintf(`[Song]
{
  Resolution = 192
  Name = "%s"
}
[ExpertSingle]
{
  768 = N 0 0
  864 = N 1 0
}
`, seed)
}

type spec struct {
	dir, seed string
	ini       map[string]string
	extra     map[string]string
}

func newTestServer(t *testing.T, specs []spec) (*httptest.Server, *library.Index) {
	t.Helper()

	root := t.TempDir()
	for _, sp := range specs {
		dir := filepath.Join(root, sp.dir)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		var b strings.Builder
		b.WriteString("[Song]\n")
		for k, v := range sp.ini {
			fmt.Fprintf(&b, "%s = %s\n", k, v)
		}
		mustWrite(t, filepath.Join(dir, "song.ini"), b.String())
		mustWrite(t, filepath.Join(dir, "notes.chart"), chartFor(sp.seed))
		mustWrite(t, filepath.Join(dir, "song.ogg"), "audio-"+sp.seed)
		for name, body := range sp.extra {
			mustWrite(t, filepath.Join(dir, name), body)
		}
	}

	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	packs, err := packcache.New(filepath.Join(t.TempDir(), "packs"))
	if err != nil {
		t.Fatal(err)
	}
	api := &Server{Store: library.NewStore(ix), Packs: packs, Version: "test"}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, ix
}

func mustWrite(t *testing.T, path, body string) {
	t.Helper()
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
}

func defaultSpecs() []spec {
	return []spec{
		{dir: "beatles", seed: "a", ini: map[string]string{
			"name": "Yesterday", "artist": "The Beatles", "album": "Help!", "year": "1965"}},
		{dir: "bjork", seed: "b", ini: map[string]string{
			"name": "Jóga", "artist": "Björk", "album": "Homogenic", "year": "1997"}},
		{dir: "blur", seed: "c", ini: map[string]string{
			"name": "Song 2", "artist": "Blur", "album": "Blur", "year": "1997"}},
	}
}

func get(t *testing.T, srv *httptest.Server, path string) (*http.Response, []byte) {
	t.Helper()
	resp, err := srv.Client().Get(srv.URL + path)
	if err != nil {
		t.Fatal(err)
	}
	body, err := io.ReadAll(resp.Body)
	resp.Body.Close()
	if err != nil {
		t.Fatal(err)
	}
	return resp, body
}

func TestSongsBrowse(t *testing.T) {
	srv, _ := newTestServer(t, defaultSpecs())

	resp, body := get(t, srv, "/api/v1/songs?sort=artist")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got songsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 3 || len(got.Songs) != 3 {
		t.Fatalf("total=%d songs=%d, want 3 and 3", got.Total, len(got.Songs))
	}
	if got.Sort != "artist" || got.Order != "asc" {
		t.Errorf("echoed sort/order = %q/%q", got.Sort, got.Order)
	}
	// "The Beatles" files under B, so it leads - the client's own ordering.
	if got.Songs[0].Artist != "The Beatles" {
		t.Errorf("first artist = %q, want The Beatles (article dropped for sorting)", got.Songs[0].Artist)
	}

	// A response must not carry a path from the server's own filesystem.
	if bytes.Contains(bytes.ToLower(body), []byte(strings.ToLower(os.TempDir()))) {
		t.Error("the response leaked a server-side filesystem path")
	}
}

func TestSongsSearch(t *testing.T) {
	srv, _ := newTestServer(t, defaultSpecs())
	_, body := get(t, srv, "/api/v1/songs?q=bjork")
	var got songsResponse
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Total != 1 || got.Songs[0].Name != "Jóga" {
		t.Fatalf("q=bjork returned %d songs (%+v)", got.Total, got.Songs)
	}
}

// An order the server cannot honour is refused, not quietly replaced with the
// default. A client that asked for something and got a different list without
// being told has no way to notice.
func TestUnknownSortIsRefused(t *testing.T) {
	srv, _ := newTestServer(t, defaultSpecs())
	resp, body := get(t, srv, "/api/v1/songs?sort=popularity")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status %d, want 400: %s", resp.StatusCode, body)
	}
	if !strings.Contains(string(body), "date_added") {
		t.Errorf("the error should name the valid values; got %s", body)
	}

	resp, _ = get(t, srv, "/api/v1/songs?order=sideways")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("bad order gave status %d, want 400", resp.StatusCode)
	}
	resp, _ = get(t, srv, "/api/v1/songs?limit=lots")
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-numeric limit gave status %d, want 400", resp.StatusCode)
	}
}

func TestHave(t *testing.T) {
	srv, ix := newTestServer(t, defaultSpecs())

	post := func(payload string) haveResponse {
		t.Helper()
		resp, err := srv.Client().Post(srv.URL+"/api/v1/have", "application/json", strings.NewReader(payload))
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(resp.Body)
			t.Fatalf("status %d: %s", resp.StatusCode, b)
		}
		var out haveResponse
		if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
			t.Fatal(err)
		}
		return out
	}

	empty := post(`{"chart_hashes":[]}`)
	if empty.MissingCount != 3 || empty.LibraryTotal != 3 {
		t.Fatalf("a client with nothing: missing=%d total=%d", empty.MissingCount, empty.LibraryTotal)
	}

	all, _ := json.Marshal(haveRequest{ChartHashes: ix.Missing(nil)})
	full := post(string(all))
	if full.MissingCount != 0 {
		t.Fatalf("a client with everything is still missing %v", full.Missing)
	}

	// A body with a field we do not understand is refused rather than half
	// applied: a client sending "chart_hash" for "chart_hashes" would otherwise
	// be told it is missing the entire library.
	resp, err := srv.Client().Post(srv.URL+"/api/v1/have", "application/json",
		strings.NewReader(`{"chart_hash":["deadbeef"]}`))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("an unknown field gave status %d, want 400", resp.StatusCode)
	}
}

// The end-to-end claim of this whole phase: what the server hands a client is a
// real .sng, and packing it did not change the song's identity.
func TestSongFileIsAReadableSNGWithTheSameIdentity(t *testing.T) {
	srv, ix := newTestServer(t, defaultSpecs())

	entry := ix.ByChartHash(ix.Missing(nil)[0])[0]
	hash := entry.Song.ChartHash

	resp, body := get(t, srv, "/song/"+hash+".sng")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	if got := resp.Header.Get("ETag"); got != `"`+entry.Song.PackageHash+`"` {
		t.Errorf("ETag = %s, want the package hash", got)
	}

	a, err := sng.Open(bytes.NewReader(body), int64(len(body)))
	if err != nil {
		t.Fatalf("the server served something that is not a readable .sng: %v", err)
	}
	chart, err := a.ReadFile("notes.chart")
	if err != nil {
		t.Fatalf("no chart in the served archive: %v", err)
	}
	sum := sha1.Sum(chart)
	if got := hex.EncodeToString(sum[:]); got != hash {
		t.Fatalf("identity changed in transit: served chart hashes to %s, asked for %s", got, hash)
	}

	// Second request must be byte-identical: the pack cache is a cache, not a
	// re-render that might differ.
	_, again := get(t, srv, "/song/"+hash+".sng")
	if !bytes.Equal(body, again) {
		t.Fatal("two requests for the same song returned different bytes")
	}
}

func TestSongFileRangeRequestsWork(t *testing.T) {
	srv, ix := newTestServer(t, defaultSpecs())
	hash := ix.Missing(nil)[0]

	_, whole := get(t, srv, "/song/"+hash+".sng")

	req, _ := http.NewRequest(http.MethodGet, srv.URL+"/song/"+hash+".sng", nil)
	req.Header.Set("Range", "bytes=0-9")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	part, _ := io.ReadAll(resp.Body)
	resp.Body.Close()

	// Without Range a sync client cannot resume an interrupted download, which
	// is the whole reason packing goes to a file rather than straight to the
	// response.
	if resp.StatusCode != http.StatusPartialContent {
		t.Fatalf("status %d, want 206", resp.StatusCode)
	}
	if len(part) != 10 || !bytes.Equal(part, whole[:10]) {
		t.Fatalf("range request returned %d bytes and they do not match the head of the file", len(part))
	}
}

// A chart hash shared by two packages is ambiguous, and the server says so
// instead of choosing. Choosing would hand different clients different audio
// for the same request.
func TestAmbiguousChartHashOffersTheChoice(t *testing.T) {
	srv, ix := newTestServer(t, []spec{
		{dir: "with-art", seed: "shared", ini: map[string]string{"name": "Twin", "artist": "X"},
			extra: map[string]string{"album.png": "\x89PNG\r\n\x1a\nfake"}},
		{dir: "no-art", seed: "shared", ini: map[string]string{"name": "Twin", "artist": "X"}},
	})

	hash := ix.Missing(nil)[0]
	resp, body := get(t, srv, "/song/"+hash+".sng")
	if resp.StatusCode != http.StatusMultipleChoices {
		t.Fatalf("status %d, want 300: %s", resp.StatusCode, body)
	}

	var choice struct {
		Packages []struct {
			PackageHash string `json:"package_hash"`
		} `json:"packages"`
	}
	if err := json.Unmarshal(body, &choice); err != nil {
		t.Fatal(err)
	}
	if len(choice.Packages) != 2 {
		t.Fatalf("offered %d packages, want 2", len(choice.Packages))
	}

	// Naming one resolves it.
	resp, body = get(t, srv, "/song/"+hash+".sng?package="+choice.Packages[0].PackageHash)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("naming a package still gave %d: %s", resp.StatusCode, body)
	}
	if _, err := sng.Open(bytes.NewReader(body), int64(len(body))); err != nil {
		t.Fatalf("the chosen package is not a readable .sng: %v", err)
	}

	// A package hash that belongs to a different chart must not resolve here.
	resp, _ = get(t, srv, "/song/"+strings.Repeat("a", 40)+".sng?package="+choice.Packages[0].PackageHash)
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("a mismatched chart/package pair gave %d, want 404", resp.StatusCode)
	}
}

func TestUnknownSongIs404(t *testing.T) {
	srv, _ := newTestServer(t, defaultSpecs())
	for _, path := range []string{
		"/song/" + strings.Repeat("0", 40) + ".sng",
		"/api/v1/songs/" + strings.Repeat("0", 40),
		"/song/notevenahash",
	} {
		resp, _ := get(t, srv, path)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("GET %s gave %d, want 404", path, resp.StatusCode)
		}
	}
}

func TestLibraryInfo(t *testing.T) {
	srv, _ := newTestServer(t, defaultSpecs())
	resp, body := get(t, srv, "/api/v1/library")
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d", resp.StatusCode)
	}
	var info struct {
		Songs          int      `json:"songs"`
		DistinctCharts int      `json:"distinct_charts"`
		SortAttributes []string `json:"sort_attributes"`
	}
	if err := json.Unmarshal(body, &info); err != nil {
		t.Fatal(err)
	}
	if info.Songs != 3 || info.DistinctCharts != 3 {
		t.Fatalf("songs=%d charts=%d", info.Songs, info.DistinctCharts)
	}
	if len(info.SortAttributes) != 12 {
		t.Fatalf("advertised %d sort attributes, want 12", len(info.SortAttributes))
	}
}

func TestSongByChartHashReturnsEveryPackage(t *testing.T) {
	srv, ix := newTestServer(t, []spec{
		{dir: "with-art", seed: "shared", ini: map[string]string{"name": "Twin", "artist": "X"},
			extra: map[string]string{"album.png": "\x89PNG\r\n\x1a\nfake"}},
		{dir: "no-art", seed: "shared", ini: map[string]string{"name": "Twin", "artist": "X"}},
	})
	hash := ix.Missing(nil)[0]
	resp, body := get(t, srv, "/api/v1/songs/"+hash)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, body)
	}
	var got struct {
		Count int `json:"count"`
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Count != 2 {
		t.Fatalf("count = %d, want both packages", got.Count)
	}
}

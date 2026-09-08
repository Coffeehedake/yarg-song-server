package httpapi

import (
	"bytes"
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
	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// writeSongs lays out song folders on disk from the same specs the rest of this
// package's tests use, so an uploaded song and a library song are built by one
// recipe rather than two that can drift.
func writeSongs(t *testing.T, root string, specs []spec) {
	t.Helper()
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
}

// checkServer builds a server with the check endpoint ON, plus the staging
// directory it would have been given at start.
func checkServer(t *testing.T, specs []spec, maxBytes int64) (*httptest.Server, *library.Index, string) {
	t.Helper()
	root := t.TempDir()
	writeSongs(t, root, specs)

	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}
	packs, err := packcache.New(filepath.Join(t.TempDir(), "packs"))
	if err != nil {
		t.Fatal(err)
	}
	stage := t.TempDir()
	api := &Server{
		Store: library.NewStore(ix), Packs: packs, Version: "test",
		CheckUploads: true, CheckMaxBytes: maxBytes, CheckDir: stage,
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, ix, stage
}

func postCheck(t *testing.T, srv *httptest.Server, name string, body []byte) (*http.Response, checkResponse, []byte) {
	t.Helper()
	resp, err := srv.Client().Post(
		srv.URL+"/api/v1/check?name="+name, "application/octet-stream", bytes.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	var out checkResponse
	_ = json.Unmarshal(raw, &out)
	return resp, out, raw
}

// packOneSong builds a real .sng from a spec, the same way a client would get
// one out of this server.
func packOneSong(t *testing.T, sp spec) []byte {
	t.Helper()
	dir := t.TempDir()
	writeSongs(t, dir, []spec{sp})
	var buf bytes.Buffer
	if err := scan.PackDir(filepath.Join(dir, sp.dir), &buf); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

func TestCheckIsNotThereUnlessItIsTurnedOn(t *testing.T) {
	// Absent, not forbidden: the route is never registered, so there is nothing
	// to be 403 about. A server that answers 403 has told you the feature
	// exists, which is a different fact than this one.
	srv, _ := newTestServer(t, defaultSpecs())
	resp, err := srv.Client().Post(srv.URL+"/api/v1/check?name=x.sng",
		"application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusNotFound {
		t.Fatalf("disabled server answered %d, want 404", resp.StatusCode)
	}
}

func TestCheckAgreesWithALibraryScan(t *testing.T) {
	// The whole reason the handler calls scan.ScanFile rather than repeating the
	// dispatch: the verdict on an uploaded file has to be the verdict the same
	// file would get sitting in the library. Both sides are measured here.
	srv, _, _ := checkServer(t, defaultSpecs(), 8<<20)
	sp := spec{dir: "uploaded", seed: "z", ini: map[string]string{
		"name": "Uploaded", "artist": "Tester"}}
	body := packOneSong(t, sp)

	resp, out, raw := postCheck(t, srv, "whatever.sng", body)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if !out.Accepted {
		t.Fatalf("a real .sng was refused: %s", out.Reason)
	}
	if out.Song == nil || out.Song.Name != "Uploaded" {
		t.Fatalf("metadata did not come back: %s", raw)
	}
	if out.Known {
		t.Fatal("a song the library does not have was reported as known")
	}

	// The independent reading: put the same bytes in a library and scan it.
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "song.sng"), body, 0o644); err != nil {
		t.Fatal(err)
	}
	ix, err := library.Build(dir)
	if err != nil {
		t.Fatal(err)
	}
	if ix.Len() != 1 {
		t.Fatalf("library scan found %d songs, want 1", ix.Len())
	}
	page := ix.Query(library.Query{Limit: 1})
	if len(page.Entries) != 1 {
		t.Fatalf("library query returned %d songs, want 1", len(page.Entries))
	}
	if got := page.Entries[0].Song.ChartHash; got != out.ChartHash {
		t.Fatalf("check said %s, a library scan said %s", out.ChartHash, got)
	}
}

func TestCheckReportsASongTheLibraryAlreadyHas(t *testing.T) {
	specs := defaultSpecs()
	srv, ix, _ := checkServer(t, specs, 8<<20)
	body := packOneSong(t, specs[0])

	_, out, raw := postCheck(t, srv, "already-here.sng", body)
	if !out.Accepted {
		t.Fatalf("refused: %s", out.Reason)
	}
	if !out.Known {
		t.Fatalf("a chart the library holds was not reported as known: %s", raw)
	}
	if len(ix.ByChartHash(out.ChartHash)) == 0 {
		t.Fatal("the test's own premise is wrong: the library does not hold that chart")
	}
}

func TestCheckRefusesWithoutBecomingAnError(t *testing.T) {
	// A song this server will not take is a 200 with a reason, not a 4xx. The
	// request succeeded; the answer was no. Anything else makes a client unable
	// to tell "your file is no good" from "the server is broken".
	srv, _, _ := checkServer(t, defaultSpecs(), 8<<20)

	for _, tc := range []struct{ name, body, wants string }{
		{"notasong.sng", "this is not an archive", "sng"},
		{"empty.zip", "", "zip"},
	} {
		resp, out, raw := postCheck(t, srv, tc.name, []byte(tc.body))
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("%s: status %d, want 200: %s", tc.name, resp.StatusCode, raw)
		}
		if out.Accepted {
			t.Fatalf("%s: garbage was accepted", tc.name)
		}
		if out.Reason == "" {
			t.Fatalf("%s: refused with no reason", tc.name)
		}
	}
}

func TestCheckRefusesAConsolePackageWithoutReadingIt(t *testing.T) {
	// Refused from the NAME. A person with a 2 GB _rb3con should not have to
	// upload it to be told this server will never read one.
	srv, _, _ := checkServer(t, defaultSpecs(), 8<<20)
	resp, out, raw := postCheck(t, srv, "song_rb3con", nil)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d: %s", resp.StatusCode, raw)
	}
	if out.Accepted {
		t.Fatal("a console package was accepted")
	}
	if !strings.Contains(out.Reason, "Rock Band") {
		t.Fatalf("reason does not name the problem: %q", out.Reason)
	}
}

func TestCheckRefusesShapesItDoesNotRead(t *testing.T) {
	srv, _, _ := checkServer(t, defaultSpecs(), 8<<20)
	for _, name := range []string{"song.rar", "song", "notes.chart"} {
		resp, _, _ := postCheck(t, srv, name, []byte("x"))
		if resp.StatusCode != http.StatusUnsupportedMediaType {
			t.Fatalf("%s: status %d, want 415", name, resp.StatusCode)
		}
	}
	resp, err := srv.Client().Post(srv.URL+"/api/v1/check",
		"application/octet-stream", strings.NewReader("x"))
	if err != nil {
		t.Fatal(err)
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("a missing ?name= answered %d, want 400", resp.StatusCode)
	}
}

func TestCheckBoundsTheBody(t *testing.T) {
	srv, _, _ := checkServer(t, defaultSpecs(), 1024)
	resp, _, _ := postCheck(t, srv, "big.sng", bytes.Repeat([]byte("x"), 4096))
	if resp.StatusCode != http.StatusRequestEntityTooLarge {
		t.Fatalf("an oversized body answered %d, want 413", resp.StatusCode)
	}
}

func TestCheckKeepsNothing(t *testing.T) {
	// The promise this endpoint makes, asserted rather than trusted to a defer
	// nobody exercises. Every path is driven: accepted, refused, oversized.
	srv, _, stage := checkServer(t, defaultSpecs(), 4096)

	postCheck(t, srv, "good.sng", packOneSong(t, spec{
		dir: "u", seed: "q", ini: map[string]string{"name": "U", "artist": "T"}}))
	postCheck(t, srv, "garbage.sng", []byte("not an archive"))
	postCheck(t, srv, "big.zip", bytes.Repeat([]byte("x"), 16384))

	left, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		var names []string
		for _, e := range left {
			names = append(names, e.Name())
		}
		t.Fatalf("the staging directory kept %d file(s): %s",
			len(left), strings.Join(names, ", "))
	}
}

func TestCheckDoesNotLetTheNameBecomeAPath(t *testing.T) {
	// The lesson the sync clients paid for, pointed at this server's own front
	// door. ?name= picks the reader and nothing else.
	//
	// The assertion is on a SENTINEL file's contents, not on a directory
	// listing, and that took two tries to get right. Listing the parent
	// directory before and after proved nothing: an implementation that joined
	// the name onto the staging path really did create the file outside, and
	// then its own cleanup deleted it again before the test looked. The listing
	// was identical and the test passed on a server that had just written where
	// it was told to.
	//
	// What actually gets damaged is a file that was already there, because
	// os.Create TRUNCATES. So the test writes something worth keeping where a
	// traversal would land, and asserts it still says what it said.
	srv, _, stage := checkServer(t, defaultSpecs(), 8<<20)
	outside := filepath.Dir(stage)

	const sentinel = "the operator's file, not the uploader's"
	targets := map[string]string{
		"../evil.sng":      filepath.Join(outside, "evil.sng"),
		"..\\evil2.sng":    filepath.Join(outside, "evil2.sng"),
		"absolute.sng":     filepath.Join(outside, "absolute.sng"),
		"../../deeper.sng": filepath.Join(filepath.Dir(outside), "deeper.sng"),
	}
	for _, path := range targets {
		mustWrite(t, path, sentinel)
	}

	for name, path := range targets {
		if name == "absolute.sng" {
			name = path // an ABSOLUTE name, the case .NET's Path.Combine gets wrong
		}
		postCheck(t, srv, name, []byte("this should never reach a file"))

		body, err := os.ReadFile(path)
		if err != nil {
			t.Fatalf("%s: the sentinel is gone entirely: %v", name, err)
		}
		if string(body) != sentinel {
			t.Fatalf("%s: an upload overwrote %s; it now holds %q",
				name, filepath.Base(path), string(body))
		}
	}

	left, err := os.ReadDir(stage)
	if err != nil {
		t.Fatal(err)
	}
	if len(left) != 0 {
		t.Fatalf("%d file(s) left in the staging directory", len(left))
	}
}

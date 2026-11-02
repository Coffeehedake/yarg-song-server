package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/packcache"
)

// pendingServer is a server whose first index has NOT been published yet - the
// state every server is in for the first ten to seventeen minutes of a large
// library, now that the listener is bound before the walk (ADR-008).
func pendingServer(t *testing.T) (*httptest.Server, *library.Store) {
	t.Helper()
	packs, err := packcache.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	store := library.NewPendingStore()
	api := &Server{
		Store:   store,
		Packs:   packs,
		Version: "test",
		// Both ON, so that a 503 can never be confused with a disabled
		// feature's 404 - those are deliberately indistinguishable from
		// absence, and that would hide the thing under test.
		BrowseUI:      true,
		CheckUploads:  true,
		CheckMaxBytes: 1 << 20,
		CheckDir:      t.TempDir(),
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, store
}

// indexDependent is every route whose answer is a claim about the library.
//
// Listed explicitly rather than derived, so that adding a route and forgetting
// to gate it is a test failure someone has to look at.
var indexDependent = []struct {
	name, method, path, body string
}{
	{"library info", "GET", "/api/v1/library", ""},
	{"songs", "GET", "/api/v1/songs", ""},
	{"one song", "GET", "/api/v1/songs/" + strings.Repeat("a", 40), ""},
	{"have", "POST", "/api/v1/have", `{"chart_hashes":[]}`},
	{"song file", "GET", "/song/" + strings.Repeat("a", 40) + ".sng", ""},
	{"upload check", "POST", "/api/v1/check?name=x.sng", "not-a-song"},
}

func TestEveryIndexDependentEndpointRefusesWhileIndexing(t *testing.T) {
	srv, _ := pendingServer(t)

	for _, tc := range indexDependent {
		t.Run(tc.name, func(t *testing.T) {
			resp := request(t, srv, tc.method, tc.path, tc.body)
			defer resp.Body.Close()
			if resp.StatusCode != http.StatusServiceUnavailable {
				t.Fatalf("%s %s while indexing: status %d, want 503",
					tc.method, tc.path, resp.StatusCode)
			}
			if got := resp.Header.Get("Retry-After"); got == "" {
				t.Errorf("%s %s: no Retry-After on the refusal", tc.method, tc.path)
			}
		})
	}
}

// TestHaveNeverAnswers200WhileIndexing is THE test. Everything else in ADR-008
// is a consequence of this one sentence.
//
// syncclient's prune asks for the server's full set and deletes every local
// .sng missing from the answer, so a 200 with an under-reported library deletes
// a player's songs. The shipped client fails closed on any non-200, which is
// what makes a server-only fix safe - but only while the server never says 200.
func TestHaveNeverAnswers200WhileIndexing(t *testing.T) {
	srv, store := pendingServer(t)

	bodies := []struct{ name, body string }{
		{"empty list", `{"chart_hashes":[]}`},
		{"a hash", `{"chart_hashes":["` + strings.Repeat("a", 40) + `"]}`},
		{"null", `{"chart_hashes":null}`},
		{"no field", `{}`},
		{"malformed JSON", `{"chart_hashes":`},
		{"not JSON at all", `absolutely not json`},
		{"empty body", ``},
	}
	for _, b := range bodies {
		resp := request(t, srv, "POST", "/api/v1/have", b.body)
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			t.Fatalf("have(%s) answered 200 while indexing: %s", b.name, body)
		}
		// A malformed body must not reach a 400 before the readiness refusal:
		// "your JSON is wrong" invites a retry with better JSON against a
		// server that still cannot answer.
		if resp.StatusCode != http.StatusServiceUnavailable {
			t.Errorf("have(%s): status %d, want 503", b.name, resp.StatusCode)
		}
	}

	// And once an index exists, the same call is answered.
	store.Replace(emptyIndex(t))
	resp := request(t, srv, "POST", "/api/v1/have", `{"chart_hashes":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("have after the index published: status %d, want 200", resp.StatusCode)
	}
}

// TestAGenuinelyEmptyLibraryStillAnswers200 is the pair to the test above, and
// the reason the refusal is a STATUS rather than an empty payload.
//
// A server that really has no songs must say so with 200 and an empty list, and
// a pruning client is RIGHT to delete everything on that answer. If "empty" and
// "not ready" were both 503 this endpoint could never report an empty library;
// if both were 200 the not-ready case would delete songs. The distinction has
// to live in the status code, and this test is what stops someone collapsing it.
func TestAGenuinelyEmptyLibraryStillAnswers200(t *testing.T) {
	srv, store := pendingServer(t)
	store.Replace(emptyIndex(t))

	resp := request(t, srv, "POST", "/api/v1/have", `{"chart_hashes":[]}`)
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("empty but INDEXED library: status %d, want 200", resp.StatusCode)
	}
	var out struct {
		LibraryTotal int      `json:"library_total"`
		Missing      []string `json:"missing"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		t.Fatal(err)
	}
	if out.LibraryTotal != 0 || len(out.Missing) != 0 {
		t.Fatalf("empty library reported total=%d missing=%d, want 0 and 0",
			out.LibraryTotal, len(out.Missing))
	}
}

func TestHealthzSaysIndexingThenOK(t *testing.T) {
	srv, store := pendingServer(t)

	resp := request(t, srv, "GET", "/healthz", "")
	body, _ := io.ReadAll(resp.Body)
	resp.Body.Close()
	// 503 and not 200-with-a-different-body: a deploy gate, a container
	// healthcheck and a load balancer all read the status, and all of them
	// should wait. A 200 here would turn a visible outage into an invisible one.
	if resp.StatusCode != http.StatusServiceUnavailable {
		t.Fatalf("healthz while indexing: status %d, want 503", resp.StatusCode)
	}
	if !strings.Contains(string(body), "indexing") {
		t.Errorf("healthz while indexing said %q, want it to mention indexing", body)
	}
	if resp.Header.Get("Retry-After") == "" {
		t.Error("healthz while indexing: no Retry-After")
	}

	store.Replace(emptyIndex(t))

	resp = request(t, srv, "GET", "/healthz", "")
	body, _ = io.ReadAll(resp.Body)
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("healthz after the index published: status %d, want 200", resp.StatusCode)
	}
	if strings.TrimSpace(string(body)) != "ok" {
		t.Errorf("healthz after the index published said %q, want \"ok\"", body)
	}
}

// TestWhatStaysAnswerableWhileIndexing pins the other half of the decision.
//
// During a seventeen-minute index these are exactly the questions an operator
// has - "what is this, and is it the build I deployed" - and refusing them
// would make the server less diagnosable than the outage it replaced.
func TestWhatStaysAnswerableWhileIndexing(t *testing.T) {
	srv, _ := pendingServer(t)

	for _, path := range []string{"/version", "/api/v1/features"} {
		resp := request(t, srv, "GET", path, "")
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Errorf("GET %s while indexing: status %d, want 200", path, resp.StatusCode)
		}
	}

	// The browse page is static HTML that fetches its data from the API, so it
	// carries no claim about the library and is served as usual; its fetches
	// get the 503. ADR-008's first draft listed it as index-dependent, which
	// reading web.go disproved.
	resp := request(t, srv, "GET", "/", "")
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Errorf("GET / while indexing: status %d, want 200 (it is static)", resp.StatusCode)
	}
}

func emptyIndex(t *testing.T) *library.Index {
	t.Helper()
	ix, err := library.Build(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	return ix
}

func request(t *testing.T, srv *httptest.Server, method, path, body string) *http.Response {
	t.Helper()
	var rdr io.Reader
	if body != "" {
		rdr = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, srv.URL+path, rdr)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return resp
}

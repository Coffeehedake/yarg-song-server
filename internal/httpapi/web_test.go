package httpapi

import (
	"net/http"
	"strings"
	"testing"
)

func TestTheBrowsePageIsServedAtTheRootWhenEnabled(t *testing.T) {
	srv, _, _ := newTestServerWithPacks(t, defaultSpecs())
	// newTestServerWithPacks builds a Server without BrowseUI, so this asserts
	// the OFF state; the ON state gets its own server below.
	code, _, err := fetch(t, srv.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusNotFound {
		t.Errorf("with the page disabled, GET / gave %d, want 404", code)
	}
}

func TestBrowsePageOnAndOff(t *testing.T) {
	on := browseServer(t, true)
	code, body, err := fetch(t, on.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	if code != http.StatusOK {
		t.Fatalf("GET / gave %d, want 200", code)
	}
	if !strings.Contains(string(body), `id="q"`) {
		t.Error("the served page has no search box; it is not the browse page")
	}

	// THE REGRESSION THAT MATTERS. Registering "GET /" instead of "GET /{$}"
	// makes the root a CATCH-ALL in Go's ServeMux: every unmatched path would
	// return this page with a 200, and every 404 the API documents would quietly
	// become an HTML page. A client asking for a song that does not exist would
	// receive a web page and try to parse it as a .sng.
	for _, p := range []string{"/nope", "/api/v1/nope", "/song/", "/favicon.ico"} {
		code, _, err := fetch(t, on.URL+p)
		if err != nil {
			t.Fatal(err)
		}
		if code != http.StatusNotFound {
			t.Errorf("GET %s gave %d, want 404 — the root handler is swallowing unmatched paths", p, code)
		}
	}
}

// The page must not reach the internet. The deployment this project is aimed at
// is a Raspberry Pi on a LAN at a party, where a stylesheet or a script from a
// CDN is a page that fails exactly when it is wanted.
func TestTheBrowsePageLoadsNothingExternal(t *testing.T) {
	on := browseServer(t, true)
	_, body, err := fetch(t, on.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, bad := range []string{`src="http`, `src='http`, `href="http`, `href='http`, "//cdn", "googleapis", "unpkg", "jsdelivr"} {
		if strings.Contains(page, bad) {
			t.Errorf("the page references %q; it must be entirely self-contained", bad)
		}
	}
}

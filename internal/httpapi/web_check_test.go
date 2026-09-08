package httpapi

import (
	"encoding/json"
	"strings"
	"testing"
)

// The drop zone is the browse page's half of "will this song work?". These
// tests are about the two ways it can be wrong that a person would not notice
// until it mattered: offering itself against a server that cannot answer, and
// rendering an uploaded file's own metadata without escaping it.

func TestTheBrowsePageAsksTheServerWhetherItCanCheck(t *testing.T) {
	// A page that assumed the endpoint exists would show a drop zone on a server
	// that answers 404. It reads the capability for the same reason it reads
	// sort_attributes: so it cannot drift from the server that served it.
	on := browseServer(t, true)
	_, body, err := fetch(t, on.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)
	for _, want := range []string{`id="checkbox"`, "lib.check_uploads", "enableChecking"} {
		if !strings.Contains(page, want) {
			t.Errorf("the page no longer contains %q", want)
		}
	}
	if !strings.Contains(page, `id="checkbox" hidden`) {
		t.Error("the drop zone is not hidden by default; a server without the endpoint would show it")
	}
}

func TestLibraryInfoReportsWhetherUploadsAreChecked(t *testing.T) {
	read := func(url string) bool {
		t.Helper()
		_, body, err := fetch(t, url+"/api/v1/library")
		if err != nil {
			t.Fatal(err)
		}
		var lib struct {
			CheckUploads bool `json:"check_uploads"`
		}
		if err := json.Unmarshal(body, &lib); err != nil {
			t.Fatal(err)
		}
		return lib.CheckUploads
	}

	off, _ := newTestServer(t, defaultSpecs())
	if read(off.URL) {
		t.Error("a server without the endpoint claims check_uploads")
	}
	on, _, _ := checkServer(t, defaultSpecs(), 8<<20)
	if !read(on.URL) {
		t.Error("a server WITH the endpoint does not say so, so the page will not offer it")
	}
}

func TestTheDropZoneEscapesWhatCameOutOfTheArchive(t *testing.T) {
	// This is the least trustworthy content anywhere on the page: a song's name,
	// artist and issue text come from a song.ini inside a file a stranger handed
	// over. The browse listing was audited for this on 2026-09-08 and came back
	// clean; the drop zone is new surface for the same data.
	on := browseServer(t, true)
	_, body, err := fetch(t, on.URL+"/")
	if err != nil {
		t.Fatal(err)
	}
	page := string(body)

	// Everything the verdict renders, named explicitly. A test that just counted
	// esc( calls would pass while a new unescaped field was added beside them.
	for _, want := range []string{
		"esc(f.name)",
		"esc(s.name || file.name)",
		"esc(who)",
		"esc(verdict.chart_hash",
		"esc(i.code)",
		"esc(i.detail)",
		"esc(verdict.error",
		"encodeURIComponent(file.name)",
	} {
		if !strings.Contains(page, want) {
			t.Errorf("the drop zone no longer contains %q, so that field is rendered raw", want)
		}
	}

	// textContent, not innerHTML, for the refusal reason - it is a Go error
	// string and can carry anything the archive put in a filename.
	if !strings.Contains(page, `li.querySelector(".why").textContent = verdict.reason`) {
		t.Error("the refusal reason is no longer assigned as text")
	}
}

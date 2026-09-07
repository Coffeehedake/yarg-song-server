package httpapi

// The browse page: a phone-friendly view of the catalog, served by the server
// itself.
//
// This is the half of upstream's open request #860 - "search and queue from an
// external device while YARG is running" - that this project can build alone.
// The QUEUE half cannot be built here: it needs something inside a running YARG
// to read a queue, and no server can reach into the game. That is the same work
// as Phase 3 seen from the other end. Deliberately not started, and deliberately
// not hinted at in the UI, because a button that cannot work is worse than no
// button.
//
// # No CDN, no build step, one file
//
// Everything is inline in party.html - no external stylesheet, no framework, no
// bundler. That is not minimalism for its own sake: the target deployment is a
// Raspberry Pi on a LAN at a party, and a page that needs the internet to render
// is a page that fails exactly when it is wanted. Embedding it in the binary
// also means the server stays a single file to deploy, which ADR-001 committed
// to.

import (
	_ "embed"
	"net/http"
	"strconv"
	"time"
)

//go:embed web/party.html
var partyHTML []byte

// partyModified is the page's Last-Modified time. It is the process start rather
// than a build timestamp, which is honest about what it can know: the binary
// does not carry the file's mtime, and inventing one would let a browser cache a
// stale page across an upgrade.
var partyModified = time.Now()

// browse serves the page.
//
// Registered as "GET /{$}" rather than "GET /", because in Go's ServeMux "/" is
// a catch-all: it would turn every unmatched path into this page and quietly
// destroy the 404s the API relies on. "{$}" matches the root and nothing else.
func (s *Server) browse(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Header().Set("Content-Length", strconv.Itoa(len(partyHTML)))
	// The page is static for the life of the process, so a conditional request
	// costs nothing to answer and saves re-sending it on every phone that opens
	// the page at a party.
	http.ServeContent(w, r, "party.html", partyModified, newBytesReader(partyHTML))
}

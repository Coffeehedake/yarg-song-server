package httpapi

// This file is the whole of "check an uploaded archive". It is deliberately
// separate from api.go: everything else in this package answers questions about
// a library the operator assembled, and this one takes bytes from whoever can
// reach the port. Keeping the trust boundary on its own page makes it harder to
// widen by accident.

import (
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/scan"
)

// checkExtensions are the file shapes this endpoint will look at.
//
// The list is not "everything scan.ScanFile understands". A console package is
// understood - it is refused by name, with a reason - and answering that
// question does not require a two-gigabyte upload, so it is refused from the
// NAME before a single byte of the body is read.
var checkExtensions = map[string]bool{".sng": true, ".zip": true, ".7z": true}

// check scans one uploaded archive and answers with the verdict, keeping
// nothing.
//
// The name comes from ?name=, and only its EXTENSION is used - to pick the
// reader, exactly as a library walk picks it from the filename on disk. The
// name itself never reaches the filesystem: the body is written to a temp file
// whose name os.CreateTemp chooses. That is the same lesson the sync clients
// learned the expensive way, pointed at this server's own front door: a string
// somebody else supplied may be a lookup key and must not be a filename.
func (s *Server) check(w http.ResponseWriter, r *http.Request) {
	name := r.URL.Query().Get("name")
	if name == "" {
		writeError(w, http.StatusBadRequest,
			"?name= is required; the extension decides how the file is read")
		return
	}

	// Refused by name, before the body. An operator who uploads a _rb3con gets
	// the same reason the library scan gives, and pays nothing to hear it.
	if scan.IsRockBandPackage(name) {
		writeJSON(w, http.StatusOK, checkResponse{
			Name:     name,
			Accepted: false,
			Reason:   scan.ErrRockBandPackage.Error(),
		})
		return
	}

	ext := strings.ToLower(filepath.Ext(name))
	if !checkExtensions[ext] {
		writeError(w, http.StatusUnsupportedMediaType,
			fmt.Sprintf("%q is not a shape this server reads; send a .sng, .zip or .7z", ext))
		return
	}

	// MaxBytesReader answers 413 itself when the body runs over, so a client
	// that lies in Content-Length is bounded by what it actually sends rather
	// than by what it claims.
	if s.CheckMaxBytes > 0 {
		if r.ContentLength > s.CheckMaxBytes {
			writeError(w, http.StatusRequestEntityTooLarge,
				fmt.Sprintf("body is %d bytes; this server accepts %d", r.ContentLength, s.CheckMaxBytes))
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, s.CheckMaxBytes)
	}

	// The extension has to survive onto disk, because scan.ScanFile dispatches
	// on it - that is the point of routing through the same function a library
	// walk uses rather than writing a second dispatch here.
	tmp, err := os.CreateTemp(s.CheckDir, "check-*"+ext)
	if err != nil {
		s.logError("check: create temp file", err)
		writeError(w, http.StatusInternalServerError, "could not stage the upload")
		return
	}
	path := tmp.Name()
	defer func() {
		// Deleted whatever happened. "Keeps nothing" is a promise this endpoint
		// makes in its documentation, and a promise nobody enforces is a leak
		// that fills a Pi's SD card one refused upload at a time.
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			s.logError("check: remove staged upload", err, "path", path)
		}
	}()

	written, err := io.Copy(tmp, r.Body)
	closeErr := tmp.Close()
	if err != nil {
		// A body that ran over the cap arrives here; MaxBytesReader has already
		// written the 413, so this must not write a second status.
		var tooBig *http.MaxBytesError
		if errors.As(err, &tooBig) {
			return
		}
		writeError(w, http.StatusBadRequest, "the upload ended early: "+err.Error())
		return
	}
	if closeErr != nil {
		s.logError("check: close staged upload", closeErr)
		writeError(w, http.StatusInternalServerError, "could not stage the upload")
		return
	}

	song, serr := scan.ScanFile(path)
	resp := checkResponse{Name: name, Bytes: written}
	switch {
	case serr != nil:
		resp.Accepted = false
		resp.Reason = serr.Error()
	case song == nil:
		// Not reachable today, and asserted rather than assumed: a nil song with
		// a nil error would otherwise be reported as an accepted song with no
		// content.
		resp.Accepted = false
		resp.Reason = "the scanner returned no song and no error"
	default:
		resp.Accepted = true
		resp.Song = song
		resp.ChartHash = song.ChartHash
		resp.Known = len(s.Store.Get().ByChartHash(song.ChartHash)) > 0
	}
	writeJSON(w, http.StatusOK, resp)
}

// checkResponse is the verdict on one uploaded file.
//
// A refusal is a 200 with Accepted false, not a 4xx. The request succeeded -
// the server was asked a question and answered it. Reserving the error statuses
// for "this server could not do the job" keeps a client able to tell the two
// apart, which matters most for the case a person actually cares about: a song
// that YARG would reject is not a failed upload.
type checkResponse struct {
	Name      string `json:"name"`
	Bytes     int64  `json:"bytes,omitempty"`
	Accepted  bool   `json:"accepted"`
	Reason    string `json:"reason,omitempty"`
	ChartHash string `json:"chart_hash,omitempty"`
	// Known reports whether this chart is ALREADY in the library. A person
	// checking a download before adding it wants that answered in the same
	// breath as "is it any good", and identity is the chart, so a different
	// package of the same chart still counts as known.
	Known bool          `json:"known"`
	Song  *catalog.Song `json:"song,omitempty"`
}

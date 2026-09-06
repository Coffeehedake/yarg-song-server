// Package httpapi is the server's HTTP surface.
//
// Three things a client needs and nothing else yet:
//
//	GET  /api/v1/songs             browse and search, ordered the way YARG orders
//	GET  /api/v1/songs/{hash}      every package sharing a chart hash
//	POST /api/v1/have              bulk "what am I missing"
//	GET  /song/{hash}.sng          the bytes, packed on demand for loose folders
//
// The wire format is .sng for everything, whatever shape the song was found in,
// because an unmodified YARG reads a .sng natively. That is the whole design
// commitment in ADR-001: the sync client writes ordinary files into an ordinary
// songs folder and the game needs no change at all.
package httpapi

import (
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"strconv"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/packcache"
)

// Server holds what the handlers need.
type Server struct {
	Store   *library.Store
	Packs   *packcache.Cache
	Version string
	Log     *slog.Logger
}

// Handler builds the router.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("GET /healthz", s.health)
	mux.HandleFunc("GET /version", s.versionInfo)
	mux.HandleFunc("GET /api/v1/library", s.libraryInfo)
	mux.HandleFunc("GET /api/v1/songs", s.songs)
	mux.HandleFunc("GET /api/v1/songs/{hash}", s.song)
	mux.HandleFunc("POST /api/v1/have", s.have)
	mux.HandleFunc("GET /song/{file}", s.songFile)

	return mux
}

func (s *Server) health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}

func (s *Server) versionInfo(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]string{"version": s.Version})
}

// libraryInfo reports what was indexed, including what could NOT be.
//
// Problems are surfaced rather than logged and forgotten: a library that
// quietly indexes 9,000 of 10,000 songs is indistinguishable from one that has
// 9,000 songs, and the operator has no way to find out which.
func (s *Server) libraryInfo(w http.ResponseWriter, r *http.Request) {
	ix := s.Store.Get()
	writeJSON(w, http.StatusOK, map[string]any{
		"songs":              ix.Len(),
		"distinct_charts":    ix.DistinctCharts(),
		"duplicate_packages": ix.DuplicatePackages,
		"built_at":           ix.BuiltAt,
		"problems":           ix.Problems,
		"sort_attributes":    library.Attributes,
	})
}

// songsResponse is one page of a browse.
type songsResponse struct {
	Total  int             `json:"total"`
	Offset int             `json:"offset"`
	Limit  int             `json:"limit"`
	Sort   string          `json:"sort"`
	Order  string          `json:"order"`
	Query  string          `json:"query,omitempty"`
	Songs  []*catalog.Song `json:"songs"`
}

func (s *Server) songs(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()

	sortBy := library.ByName
	if raw := q.Get("sort"); raw != "" {
		parsed, ok := library.ParseAttribute(raw)
		if !ok {
			// An unrecognised sort is refused rather than silently replaced
			// with the default. A client asking for an order it will not get
			// should be told, not quietly handed a different list.
			writeError(w, http.StatusBadRequest, fmt.Sprintf(
				"unknown sort %q; valid values are %s", raw, joinAttributes()))
			return
		}
		sortBy = parsed
	}

	order := strings.ToLower(q.Get("order"))
	switch order {
	case "", "asc":
		order = "asc"
	case "desc":
	default:
		writeError(w, http.StatusBadRequest, fmt.Sprintf("unknown order %q; want asc or desc", order))
		return
	}

	limit, err := intParam(q.Get("limit"), library.DefaultLimit)
	if err != nil {
		writeError(w, http.StatusBadRequest, "limit: "+err.Error())
		return
	}
	offset, err := intParam(q.Get("offset"), 0)
	if err != nil {
		writeError(w, http.StatusBadRequest, "offset: "+err.Error())
		return
	}

	page := s.Store.Get().Query(library.Query{
		Text:       q.Get("q"),
		SortBy:     sortBy,
		Descending: order == "desc",
		Limit:      limit,
		Offset:     offset,
	})

	songs := make([]*catalog.Song, 0, len(page.Entries))
	for _, e := range page.Entries {
		songs = append(songs, e.Song)
	}
	writeJSON(w, http.StatusOK, songsResponse{
		Total:  page.Total,
		Offset: page.Offset,
		Limit:  limit,
		Sort:   string(sortBy),
		Order:  order,
		Query:  q.Get("q"),
		Songs:  songs,
	})
}

// song returns every package that shares a chart hash.
//
// A list, not an object, because the identity is deliberately many-to-one: two
// packages with the same chart and different audio are the same song to YARG.
// Returning the first would hide the other from every client that asked.
func (s *Server) song(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("hash"))
	entries := s.Store.Get().ByChartHash(hash)
	if len(entries) == 0 {
		writeError(w, http.StatusNotFound, "no song with that chart hash")
		return
	}
	songs := make([]*catalog.Song, 0, len(entries))
	for _, e := range entries {
		songs = append(songs, e.Song)
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"chart_hash": hash,
		"count":      len(songs),
		"songs":      songs,
	})
}

// haveRequest is what a client already holds.
type haveRequest struct {
	ChartHashes []string `json:"chart_hashes"`
}

// haveResponse is what it is missing.
type haveResponse struct {
	LibraryTotal int      `json:"library_total"`
	Missing      []string `json:"missing"`
	MissingCount int      `json:"missing_count"`
}

// have answers "what do I not have" in one round trip.
//
// The client sends the chart hashes it holds; the answer is every chart hash in
// the library that was not in that list. Sorted, so two calls with the same
// library and the same input give the same bytes.
func (s *Server) have(w http.ResponseWriter, r *http.Request) {
	// A library of 10,000 songs is ~400 KB of hashes. A megabyte is generous
	// and still refuses a body that could exhaust memory.
	const maxBody = 8 << 20
	r.Body = http.MaxBytesReader(w, r.Body, maxBody)

	var req haveRequest
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, "request body too large")
			return
		}
		writeError(w, http.StatusBadRequest, "malformed JSON body: "+err.Error())
		return
	}

	ix := s.Store.Get()
	missing := ix.Missing(req.ChartHashes)
	writeJSON(w, http.StatusOK, haveResponse{
		LibraryTotal: ix.DistinctCharts(),
		Missing:      missing,
		MissingCount: len(missing),
	})
}

// songFile serves the package bytes as a .sng.
//
// The path is /song/{chart_hash}.sng. When two packages share a chart hash the
// request is ambiguous, and rather than pick one it answers 300 with the
// package hashes to choose from; ?package=<hash> then names one.
func (s *Server) songFile(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("file")
	hash, ok := strings.CutSuffix(strings.ToLower(name), ".sng")
	if !ok {
		writeError(w, http.StatusNotFound, "songs are served as /song/{chart_hash}.sng")
		return
	}

	ix := s.Store.Get()

	var entry *library.Entry
	if pkg := r.URL.Query().Get("package"); pkg != "" {
		entry = ix.ByPackageHash(pkg)
		if entry == nil || entry.Song.ChartHash != hash {
			writeError(w, http.StatusNotFound, "no package with that package hash for this chart")
			return
		}
	} else {
		entries := ix.ByChartHash(hash)
		switch len(entries) {
		case 0:
			writeError(w, http.StatusNotFound, "no song with that chart hash")
			return
		case 1:
			entry = entries[0]
		default:
			choices := make([]map[string]string, 0, len(entries))
			for _, e := range entries {
				choices = append(choices, map[string]string{
					"package_hash": e.Song.PackageHash,
					"name":         e.Song.Name,
					"artist":       e.Song.Artist,
					"charter":      e.Song.Charter,
				})
			}
			writeJSON(w, http.StatusMultipleChoices, map[string]any{
				"error":      "this chart hash is shared by several packages; pass ?package=<package_hash>",
				"chart_hash": hash,
				"packages":   choices,
			})
			return
		}
	}

	path := entry.Path
	if entry.Kind.NeedsPacking() {
		packed, err := s.Packs.Path(entry.Song.PackageHash, entry.Path)
		if err != nil {
			s.logError("pack song", err, "chart_hash", hash)
			writeError(w, http.StatusInternalServerError, "could not pack this song")
			return
		}
		path = packed
	}

	f, err := os.Open(path)
	if err != nil {
		// The library moved under us. Say so rather than serving a 500 with no
		// explanation - the fix is a rescan and the operator needs to know.
		s.logError("open song", err, "chart_hash", hash)
		writeError(w, http.StatusNotFound, "this song is no longer where the index says it is; rescan")
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		s.logError("stat song", err, "chart_hash", hash)
		writeError(w, http.StatusInternalServerError, "could not read this song")
		return
	}

	// A strong ETag from the package hash: it is a hash OF THE CONTENT, so it
	// is the same on every server holding the same package and survives a
	// rescan, a restart and a cache wipe.
	w.Header().Set("ETag", `"`+entry.Song.PackageHash+`"`)
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", "attachment; filename=\""+hash+".sng\"")
	http.ServeContent(w, r, hash+".sng", st.ModTime(), f)
}

func (s *Server) logError(msg string, err error, args ...any) {
	if s.Log == nil {
		return
	}
	s.Log.Error(msg, append([]any{"err", err}, args...)...)
}

func intParam(raw string, def int) (int, error) {
	if strings.TrimSpace(raw) == "" {
		return def, nil
	}
	n, err := strconv.Atoi(raw)
	if err != nil {
		return 0, fmt.Errorf("%q is not a number", raw)
	}
	if n < 0 {
		return 0, fmt.Errorf("must not be negative")
	}
	return n, nil
}

func joinAttributes() string {
	parts := make([]string, 0, len(library.Attributes))
	for _, a := range library.Attributes {
		parts = append(parts, string(a))
	}
	return strings.Join(parts, ", ")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, status int, msg string) {
	writeJSON(w, status, map[string]string{"error": msg})
}

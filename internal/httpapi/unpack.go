package httpapi

import (
	"crypto/subtle"
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/unpack"
)

// Unpacking is slow - minutes, not milliseconds. Reading one song out of a 2 GB solid .7z was
// measured at about three minutes, and a whole archive at 13m38s over a share. No client can
// hold a request open for that, and a phone certainly cannot: it would time out, the operator
// would learn nothing about whether it worked, and the obvious response - pressing the button
// again - would start a second extraction over the first.
//
// So POST starts a job and returns immediately, and GET reports where it got to.

// UnpackJob is the one unpack this server will run at a time.
//
// One at a time, deliberately. Two extractions at once on the target hardware - a Pi with the
// library on external storage - would contend for the same disk and finish later than running
// them in turn, and the second would be competing with the first for the space neither checked.
type UnpackJob struct {
	mu       sync.Mutex
	running  bool
	archive  string
	started  time.Time
	finished time.Time
	result   *unpack.Result
	failure  string
}

type unpackStatus struct {
	Running  bool           `json:"running"`
	Archive  string         `json:"archive,omitempty"`
	Started  string         `json:"started,omitempty"`
	Finished string         `json:"finished,omitempty"`
	Result   *unpack.Result `json:"result,omitempty"`
	Error    string         `json:"error,omitempty"`
}

func (j *UnpackJob) status() unpackStatus {
	j.mu.Lock()
	defer j.mu.Unlock()

	s := unpackStatus{Running: j.running, Archive: j.archive, Error: j.failure, Result: j.result}
	if !j.started.IsZero() {
		s.Started = j.started.UTC().Format(time.RFC3339)
	}
	if !j.finished.IsZero() {
		s.Finished = j.finished.UTC().Format(time.RFC3339)
	}
	return s
}

// start claims the job, or reports that it is already busy.
func (j *UnpackJob) start(archive string) bool {
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.running {
		return false
	}
	j.running = true
	j.archive = archive
	j.started = time.Now()
	j.finished = time.Time{}
	j.result = nil
	j.failure = ""
	return true
}

func (j *UnpackJob) done(res *unpack.Result, err error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	j.running = false
	j.finished = time.Now()
	j.result = res
	if err != nil {
		j.failure = err.Error()
	}
}

// mayUnpack reports whether this caller may unpack, and why not when it may not.
//
// Two gates, and they answer different questions. Locality says which machines may ask at all;
// the secret says whether this caller is the operator. Neither is sufficient alone: "lan" is
// every device on the network, and a secret on a server that speaks plain HTTP crosses the
// wire in clear. Together they are a cover on a switch - enough that unpacking cannot happen
// by accident or by idle poking, and not something to describe as security.
func (s *Server) mayUnpack(r *http.Request) (bool, string) {
	switch s.UnpackOps {
	case config.WritesLAN:
		// fine, check the secret below
	case config.WritesLocal:
		if !isLoopback(r.RemoteAddr) {
			return false, "unpack_ops is set to local, so archives can only be unpacked from the server itself"
		}
	default:
		return false, "unpack_ops is off"
	}

	// Constant time, so that a caller cannot learn the secret one byte at a time by measuring
	// how long the comparison took. Cheap here and impossible to retrofit convincingly.
	given := r.Header.Get("X-Admin-Secret")
	if s.AdminSecret == "" ||
		subtle.ConstantTimeCompare([]byte(given), []byte(s.AdminSecret)) != 1 {
		return false, "the admin secret is missing or wrong"
	}
	return true, ""
}

// unpackStatusHandler handles GET /api/v1/unpack.
func (s *Server) unpackStatusHandler(w http.ResponseWriter, r *http.Request) {
	if !s.UnpackOps.Enabled() || s.Unpack == nil {
		notThere(w, r)
		return
	}
	if ok, why := s.mayUnpack(r); !ok {
		writeError(w, http.StatusForbidden, why)
		return
	}
	writeJSON(w, http.StatusOK, s.Unpack.status())
}

type unpackRequest struct {
	Archive string `json:"archive"`
	// Check asks what unpacking would do without doing it. The two are one endpoint because
	// they take the same input and the same permission, and because a client that can run one
	// should not have to be told a second URL to run the other.
	Check bool `json:"check"`
}

// unpackHandler handles POST /api/v1/unpack.
func (s *Server) unpackHandler(w http.ResponseWriter, r *http.Request) {
	if !s.UnpackOps.Enabled() || s.Unpack == nil {
		notThere(w, r)
		return
	}
	if ok, why := s.mayUnpack(r); !ok {
		writeError(w, http.StatusForbidden, why)
		return
	}

	var body unpackRequest
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "the body must be JSON: "+err.Error())
		return
	}
	if body.Archive == "" {
		writeError(w, http.StatusBadRequest, "name the archive to unpack")
		return
	}

	opts := unpack.Options{LibraryRoot: s.Store.Get().Root, Dir: s.UnpackDir}

	if body.Check {
		plan, err := opts.Inspect(body.Archive)
		if err != nil {
			writeError(w, http.StatusBadRequest, err.Error())
			return
		}
		writeJSON(w, http.StatusOK, plan)
		return
	}

	if !s.Unpack.start(body.Archive) {
		// 409 rather than queueing. A queue would accept a second archive and then run it
		// much later, by which time whoever asked has forgotten; saying "busy" lets them ask
		// again when it matters.
		writeError(w, http.StatusConflict, "an unpack is already running")
		return
	}

	go func() {
		res, err := opts.Run(body.Archive)
		s.Unpack.done(res, err)
		if err == nil && s.OnUnpacked != nil {
			// The library on disk changed, and nothing else would notice: the index is built
			// at startup. Without this the songs are extracted, correct, and invisible until
			// somebody restarts the server.
			s.OnUnpacked()
		}
	}()

	writeJSON(w, http.StatusAccepted, s.Unpack.status())
}

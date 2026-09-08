package httpapi

// The write half of the feature registry: turning a capability on or off from
// the browse page instead of by editing a file and restarting. That last clause
// is the part of goal 1 this closes - "only the features the user wants are
// enabled via a configuration menu in the server app".
//
// Two properties had to survive it, and both are asserted rather than assumed.
//
// FIRST: a disabled feature is still ABSENT, not forbidden. Until now that was
// true by construction - the route was never registered - and a runtime toggle
// cannot work that way, because a route that does not exist cannot be switched
// on. So the routes are always registered and the handlers answer with the
// mux's own 404, byte for byte, when the feature is off. The observable
// contract is unchanged; only the mechanism moved. A 403 would have told a
// caller the feature exists, which is a different fact.
//
// SECOND: the write surface cannot widen itself. `config_writes` is not
// writable, so "only from this machine" cannot be turned into "anyone on the
// network" by anyone who is already inside it.

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/config"
)

// enabled reports the LIVE state of a feature, which is the startup value until
// somebody changes it.
func (s *Server) enabled(name string) bool {
	s.liveOnce.Do(s.initLive)
	s.liveMu.RLock()
	defer s.liveMu.RUnlock()
	return s.live[name]
}

func (s *Server) setEnabled(name string, on bool) {
	s.liveOnce.Do(s.initLive)
	s.liveMu.Lock()
	defer s.liveMu.Unlock()
	s.live[name] = on
}

func (s *Server) initLive() {
	s.live = map[string]bool{
		"browse_ui":     s.BrowseUI,
		"check_uploads": s.CheckUploads,
	}
}

// notThere answers exactly as Go's ServeMux answers for a path it does not
// know. Not writeError: a JSON body would make a disabled feature
// distinguishable from an absent one, which is the whole property.
func notThere(w http.ResponseWriter, r *http.Request) { http.NotFound(w, r) }

// mayWrite reports whether this caller is allowed to change a feature, and why
// not when it is not.
func (s *Server) mayWrite(r *http.Request) (bool, string) {
	switch s.Writes {
	case config.WritesLAN:
		return true, ""
	case config.WritesLocal:
		if isLoopback(r.RemoteAddr) {
			return true, ""
		}
		return false, "config_writes is set to local, so features can only be changed from the server itself"
	default:
		return false, "config_writes is off"
	}
}

// isLoopback is deliberately strict about what counts as "this machine".
//
// It reads RemoteAddr, which is the socket's own peer address, and NOT any
// X-Forwarded-For header. A header is supplied by the caller; trusting one here
// would let anybody on the network claim to be local by typing it, which would
// turn the "local" setting into a suggestion. This server is documented as not
// belonging behind a reverse proxy, so there is no legitimate case where the
// socket lies.
func isLoopback(remoteAddr string) bool {
	host, _, err := net.SplitHostPort(remoteAddr)
	if err != nil {
		host = remoteAddr
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

type featureUpdate struct {
	Enabled *bool `json:"enabled"`
}

// setFeature handles PUT /api/v1/features/{name}.
func (s *Server) setFeature(w http.ResponseWriter, r *http.Request) {
	// Off means the route is not here at all, matching every other optional
	// capability in this server.
	if !s.Writes.Enabled() {
		notThere(w, r)
		return
	}

	if ok, why := s.mayWrite(r); !ok {
		// 403 rather than 404 here, and the difference from the line above is
		// deliberate: a caller reaching this point has been told by
		// GET /api/v1/features that writing exists and that they may not do
		// it. Pretending the route is absent after advertising it would just
		// read as a broken server.
		writeError(w, http.StatusForbidden, why)
		return
	}

	name := r.PathValue("name")
	if !config.Writable(name) {
		if name == "config_writes" {
			writeError(w, http.StatusForbidden,
				"config_writes cannot be changed here; a write surface that can widen its own access has no bound. Set it in the config file.")
			return
		}
		// Unknown, or a setting rather than a capability. Either way there is
		// nothing at this path to change.
		notThere(w, r)
		return
	}

	var body featureUpdate
	if err := json.NewDecoder(http.MaxBytesReader(w, r.Body, 4<<10)).Decode(&body); err != nil {
		writeError(w, http.StatusBadRequest, "body must be JSON like {\"enabled\": true}")
		return
	}
	if body.Enabled == nil {
		// Absent and false are different intents, and a missing field must not
		// silently mean "off".
		writeError(w, http.StatusBadRequest, "body must set \"enabled\" to true or false")
		return
	}

	was := s.enabled(name)
	s.setEnabled(name, *body.Enabled)
	if s.Log != nil && was != *body.Enabled {
		s.Log.Info("feature changed over HTTP",
			"feature", name, "enabled", *body.Enabled, "from", r.RemoteAddr)
	}

	f := s.feature(name)
	writeJSON(w, http.StatusOK, map[string]any{
		"feature": f,
		// Says so plainly rather than letting somebody find out after a
		// restart. A settings menu that silently forgets is a trap.
		"persisted": false,
		"make_permanent": fmt.Sprintf("%s = %s", name,
			map[bool]string{true: "yes", false: "no"}[*body.Enabled]),
	})
}

// feature returns one registry entry with its LIVE enabled value.
func (s *Server) feature(name string) *config.Feature {
	for _, f := range s.liveFeatures() {
		if f.Name == name {
			return &f
		}
	}
	return nil
}

// liveFeatures is the registry with current values rather than startup ones.
//
// The descriptions, defaults, endpoints and sources still come from the
// registry the server was built with - there is one place that knows what a
// feature IS - and only `enabled` is overlaid. Source stays as resolved at
// startup and is therefore about where the value CAME FROM, which is still true
// of a value somebody has since changed; the response says separately that a
// runtime change is not persisted.
func (s *Server) liveFeatures() []config.Feature {
	out := make([]config.Feature, 0, len(s.Features))
	for _, f := range s.Features {
		f.Enabled = s.enabled(f.Name)
		out = append(out, f)
	}
	return out
}

package httpapi

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/library"
)

func writeServer(t *testing.T, access config.WriteAccess) *Server {
	t.Helper()
	res := config.Resolved{Config: config.Defaults(), Provenance: map[string]config.Source{}}
	return &Server{
		Store:        library.NewStore(&library.Index{}),
		Version:      "test",
		BrowseUI:     res.BrowseUI,
		CheckUploads: res.CheckUploads,
		CheckDir:     t.TempDir(),
		Features:     res.Features(),
		Writes:       access,
	}
}

// do sends a request straight at the handler with a chosen RemoteAddr, which a
// httptest server cannot give us: every connection to one is from loopback, so
// the "only from this machine" rule would be untestable through it.
func do(t *testing.T, s *Server, method, target, remote, body string) *http.Response {
	t.Helper()
	req := httptest.NewRequest(method, target, strings.NewReader(body))
	req.RemoteAddr = remote
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)
	return rec.Result()
}

func readAll(t *testing.T, r *http.Response) string {
	t.Helper()
	b, err := io.ReadAll(r.Body)
	if err != nil {
		t.Fatal(err)
	}
	r.Body.Close()
	return string(b)
}

// THE property this rework had to preserve. A disabled feature used to be
// absent because its route was never registered; a runtime toggle cannot work
// that way, so the handler now answers instead. Asserting the STATUS alone
// would miss the thing that matters — a JSON error body would tell a caller the
// feature exists — so this compares the whole response to a path that genuinely
// is not there.
func TestADisabledFeatureIsIndistinguishableFromAnAbsentOne(t *testing.T) {
	s := writeServer(t, config.WritesOff) // check_uploads off by default

	disabled := do(t, s, "POST", "/api/v1/check?name=x.sng", "127.0.0.1:1", "x")
	absent := do(t, s, "POST", "/api/v1/no-such-route-at-all", "127.0.0.1:1", "x")

	if disabled.StatusCode != absent.StatusCode {
		t.Errorf("status %d for a disabled feature, %d for an absent route",
			disabled.StatusCode, absent.StatusCode)
	}
	db, ab := readAll(t, disabled), readAll(t, absent)
	if db != ab {
		t.Errorf("bodies differ:\n  disabled: %q\n  absent:   %q", db, ab)
	}
	if got, want := disabled.Header.Get("Content-Type"), absent.Header.Get("Content-Type"); got != want {
		t.Errorf("content-type %q for disabled, %q for absent", got, want)
	}
}

// The same, for the browse page, whose route also became unconditional.
func TestADisabledBrowsePageIsAlsoJustAbsent(t *testing.T) {
	s := writeServer(t, config.WritesOff)
	s.BrowseUI = false

	disabled := do(t, s, "GET", "/", "127.0.0.1:1", "")
	absent := do(t, s, "GET", "/nope", "127.0.0.1:1", "")
	if disabled.StatusCode != http.StatusNotFound {
		t.Fatalf("root answered %d with browse_ui off, want 404", disabled.StatusCode)
	}
	if a, b := readAll(t, disabled), readAll(t, absent); a != b {
		t.Errorf("disabled root body %q, absent path body %q", a, b)
	}
}

// Off is the default and means the route is not there at all.
func TestWritingIsOffUnlessTurnedOn(t *testing.T) {
	s := writeServer(t, config.WritesOff)
	resp := do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`)
	if resp.StatusCode != http.StatusNotFound {
		t.Errorf("PUT answered %d with config_writes off, want 404", resp.StatusCode)
	}
	if s.enabled("check_uploads") {
		t.Error("the feature was changed by a request that should not have reached the handler")
	}
}

// The whole point: a toggle takes effect on the live server, with no restart.
func TestTogglingAFeatureTakesEffectImmediately(t *testing.T) {
	s := writeServer(t, config.WritesLocal)

	if got := do(t, s, "POST", "/api/v1/check?name=x.sng", "127.0.0.1:1", "x").StatusCode; got != http.StatusNotFound {
		t.Fatalf("check answered %d before being enabled, want 404", got)
	}

	on := do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`)
	if on.StatusCode != http.StatusOK {
		t.Fatalf("enabling answered %d: %s", on.StatusCode, readAll(t, on))
	}

	// Not 404 any more. It will be a 400 for a body this bogus, and that is
	// the point - the route exists now.
	if got := do(t, s, "POST", "/api/v1/check?name=x.sng", "127.0.0.1:1", "x").StatusCode; got == http.StatusNotFound {
		t.Error("check still answers 404 after being enabled")
	}

	off := do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":false}`)
	if off.StatusCode != http.StatusOK {
		t.Fatalf("disabling answered %d", off.StatusCode)
	}
	if got := do(t, s, "POST", "/api/v1/check?name=x.sng", "127.0.0.1:1", "x").StatusCode; got != http.StatusNotFound {
		t.Errorf("check answered %d after being switched off again, want 404", got)
	}
}

// "local" has to mean the machine, not the network.
func TestLocalMeansThisMachineOnly(t *testing.T) {
	s := writeServer(t, config.WritesLocal)

	for _, remote := range []string{"127.0.0.1:5", "[::1]:5"} {
		resp := do(t, s, "PUT", "/api/v1/features/browse_ui", remote, `{"enabled":false}`)
		if resp.StatusCode != http.StatusOK {
			t.Errorf("loopback %s was refused with %d", remote, resp.StatusCode)
		}
	}
	for _, remote := range []string{"192.168.2.50:5", "10.0.0.9:5", "[fe80::1]:5"} {
		resp := do(t, s, "PUT", "/api/v1/features/browse_ui", remote, `{"enabled":false}`)
		if resp.StatusCode != http.StatusForbidden {
			t.Errorf("non-local %s answered %d, want 403", remote, resp.StatusCode)
		}
	}
}

// A header the caller controls must not be able to claim locality. Without
// this, "local" is a suggestion: anybody on the LAN types one line and is
// treated as sitting at the machine.
func TestAForwardedHeaderCannotClaimToBeLocal(t *testing.T) {
	s := writeServer(t, config.WritesLocal)

	req := httptest.NewRequest("PUT", "/api/v1/features/browse_ui", strings.NewReader(`{"enabled":false}`))
	req.RemoteAddr = "192.168.2.50:5"
	req.Header.Set("X-Forwarded-For", "127.0.0.1")
	req.Header.Set("X-Real-IP", "127.0.0.1")
	rec := httptest.NewRecorder()
	s.Handler().ServeHTTP(rec, req)

	if rec.Code != http.StatusForbidden {
		t.Errorf("a spoofed forwarded header got %d, want 403", rec.Code)
	}
	if !s.enabled("browse_ui") {
		t.Error("a spoofed forwarded header changed a feature")
	}
}

// lan is the operator's explicit choice and does what it says.
func TestLanAllowsAnyCaller(t *testing.T) {
	s := writeServer(t, config.WritesLAN)
	resp := do(t, s, "PUT", "/api/v1/features/browse_ui", "192.168.2.50:5", `{"enabled":false}`)
	if resp.StatusCode != http.StatusOK {
		t.Errorf("lan refused a LAN caller with %d", resp.StatusCode)
	}
	if s.enabled("browse_ui") {
		t.Error("the change did not take effect")
	}
}

// A write surface that can widen its own access has no bound at all.
func TestTheWriteSurfaceCannotWidenItself(t *testing.T) {
	s := writeServer(t, config.WritesLocal)
	resp := do(t, s, "PUT", "/api/v1/features/config_writes", "127.0.0.1:1", `{"enabled":true}`)
	if resp.StatusCode != http.StatusForbidden {
		t.Errorf("config_writes answered %d, want 403", resp.StatusCode)
	}
	if s.Writes != config.WritesLocal {
		t.Errorf("access became %q", s.Writes)
	}
}

// Settings are not capabilities, and there is nothing at their path to change.
func TestSettingsAreNotWritableAsFeatures(t *testing.T) {
	s := writeServer(t, config.WritesLAN)
	for _, name := range []string{"songs", "data", "listen", "pack_cache_max", "not_a_thing"} {
		resp := do(t, s, "PUT", "/api/v1/features/"+name, "127.0.0.1:1", `{"enabled":true}`)
		if resp.StatusCode != http.StatusNotFound {
			t.Errorf("%s answered %d, want 404", name, resp.StatusCode)
		}
	}
}

// Absent and false are different intents; a missing field must not quietly mean
// "off" and silently disable something.
func TestAMissingEnabledFieldIsRefusedRatherThanGuessed(t *testing.T) {
	s := writeServer(t, config.WritesLocal)
	before := s.enabled("browse_ui")

	resp := do(t, s, "PUT", "/api/v1/features/browse_ui", "127.0.0.1:1", `{}`)
	if resp.StatusCode != http.StatusBadRequest {
		t.Errorf("an empty body answered %d, want 400", resp.StatusCode)
	}
	if s.enabled("browse_ui") != before {
		t.Error("an empty body changed the feature")
	}
}

type putBody struct {
	Feature       config.Feature `json:"feature"`
	Persisted     bool           `json:"persisted"`
	MakePermanent string         `json:"make_permanent"`
	PersistError  string         `json:"persist_error"`
	ConfigFile    string         `json:"config_file"`
}

func decodePut(t *testing.T, resp *http.Response) putBody {
	t.Helper()
	var b putBody
	if err := json.NewDecoder(resp.Body).Decode(&b); err != nil {
		t.Fatal(err)
	}
	return b
}

// With a config file to write to, a change survives a restart — and the file it
// went into is the one the server actually read.
func TestAChangeIsWrittenBackToTheConfigFile(t *testing.T) {
	dir := t.TempDir()
	conf := filepath.Join(dir, "yarg-song-server.conf")
	if err := os.WriteFile(conf, []byte(config.Example), 0o644); err != nil {
		t.Fatal(err)
	}

	s := writeServer(t, config.WritesLocal)
	s.ConfigPath = conf

	body := decodePut(t, do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`))
	if !body.Persisted {
		t.Fatalf("not persisted: %s", body.PersistError)
	}
	if body.ConfigFile != "yarg-song-server.conf" {
		t.Errorf("config_file = %q", body.ConfigFile)
	}

	// The measurement that matters is not "we wrote a line" but "a server
	// starting from this file now behaves differently".
	reloaded := config.Defaults()
	if err := config.LoadFile(&reloaded, conf); err != nil {
		t.Fatal(err)
	}
	if !reloaded.CheckUploads {
		t.Error("a server restarted from this file would still have check_uploads off")
	}
}

// A save that fails must not be reported as a toggle that failed. The feature
// really is on; only the saving went wrong, and conflating the two sends
// somebody looking for a bug that is not there.
func TestAFailedSaveStillLeavesTheFeatureOn(t *testing.T) {
	s := writeServer(t, config.WritesLocal)
	s.ConfigPath = filepath.Join(t.TempDir(), "does-not-exist.conf")

	resp := do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`)
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status %d; a save failure is not a request failure", resp.StatusCode)
	}
	body := decodePut(t, resp)
	if body.Persisted {
		t.Error("claimed to have saved into a file that does not exist")
	}
	if body.PersistError == "" {
		t.Error("no reason given for not saving")
	}
	if !body.Feature.Enabled || !s.enabled("check_uploads") {
		t.Error("the runtime change was lost because the save failed")
	}
	// And the route really is live, which is the whole point of the toggle.
	if got := do(t, s, "POST", "/api/v1/check?name=x.sng", "127.0.0.1:1", "x").StatusCode; got == http.StatusNotFound {
		t.Error("check is still absent after a toggle whose save failed")
	}
}

// The server must not create a config file out of nowhere. Settings written
// into whatever directory it happens to be running from would land somewhere
// nobody would think to look.
func TestNoConfigFileMeansNothingIsCreated(t *testing.T) {
	dir := t.TempDir()
	s := writeServer(t, config.WritesLocal)
	s.ConfigPath = "" // the server read no config file

	body := decodePut(t, do(t, s, "PUT", "/api/v1/features/browse_ui", "127.0.0.1:1", `{"enabled":false}`))
	if body.Persisted {
		t.Error("claimed to persist with no config file")
	}
	if body.MakePermanent != "browse_ui = no" {
		t.Errorf("make_permanent = %q", body.MakePermanent)
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 0 {
		t.Errorf("files appeared: %v", entries)
	}
}

// Toggling twice must leave the file saying the second thing, once.
func TestTogglingTwiceLeavesOneCorrectLine(t *testing.T) {
	conf := filepath.Join(t.TempDir(), "yarg-song-server.conf")
	if err := os.WriteFile(conf, []byte(config.Example), 0o644); err != nil {
		t.Fatal(err)
	}
	s := writeServer(t, config.WritesLocal)
	s.ConfigPath = conf

	do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`)
	do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":false}`)

	reloaded := config.Defaults()
	if err := config.LoadFile(&reloaded, conf); err != nil {
		t.Fatal(err)
	}
	if reloaded.CheckUploads {
		t.Error("the file still says the first value")
	}
	raw, err := os.ReadFile(conf)
	if err != nil {
		t.Fatal(err)
	}
	if n := strings.Count(string(raw), "\ncheck_uploads = "); n != 1 {
		t.Errorf("%d written check_uploads lines, want 1", n)
	}
}

// The page must ask whether IT may write, not merely whether writing is on.
// Offering switches to a phone that will get a 403 is worse than offering none.
func TestFeaturesReportsWritabilityForThisCaller(t *testing.T) {
	cases := []struct {
		access config.WriteAccess
		remote string
		want   bool
	}{
		{config.WritesOff, "127.0.0.1:1", false},
		{config.WritesLocal, "127.0.0.1:1", true},
		{config.WritesLocal, "192.168.2.50:1", false},
		{config.WritesLAN, "192.168.2.50:1", true},
	}
	for _, c := range cases {
		s := writeServer(t, c.access)
		resp := do(t, s, "GET", "/api/v1/features", c.remote, "")
		var body struct {
			Writable bool   `json:"writable"`
			Reason   string `json:"writable_reason"`
		}
		if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
			t.Fatal(err)
		}
		if body.Writable != c.want {
			t.Errorf("access=%s from %s: writable=%v, want %v", c.access, c.remote, body.Writable, c.want)
		}
		if !body.Writable && body.Reason == "" {
			t.Errorf("access=%s from %s: refused with no reason", c.access, c.remote)
		}
	}
}

// GET /api/v1/features has to show the CURRENT value, not the one the server
// started with. A page that toggled a switch and then re-read stale state would
// look like the toggle had failed.
func TestFeaturesReflectsARuntimeChange(t *testing.T) {
	s := writeServer(t, config.WritesLocal)
	do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`)

	resp := do(t, s, "GET", "/api/v1/features", "127.0.0.1:1", "")
	var body struct {
		Features []config.Feature `json:"features"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	for _, f := range body.Features {
		if f.Name == "check_uploads" {
			if !f.Enabled {
				t.Error("features still reports check_uploads off after it was turned on")
			}
			if f.Default {
				t.Error("default moved; it describes the built-in, not the live value")
			}
			return
		}
	}
	t.Error("check_uploads missing from the registry")
}

// /api/v1/library carries check_uploads so the page knows whether to offer a
// drop zone. It has to move with the toggle, or the page offers an upload to a
// server that will 404 it.
func TestLibraryInfoFollowsTheLiveValue(t *testing.T) {
	s := writeServer(t, config.WritesLocal)
	do(t, s, "PUT", "/api/v1/features/check_uploads", "127.0.0.1:1", `{"enabled":true}`)

	resp := do(t, s, "GET", "/api/v1/library", "127.0.0.1:1", "")
	var body map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatal(err)
	}
	if body["check_uploads"] != true {
		t.Errorf("library reports check_uploads=%v after it was enabled", body["check_uploads"])
	}
}

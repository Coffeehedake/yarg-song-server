package httpapi

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/library"
)

type featuresBody struct {
	Features []config.Feature `json:"features"`
}

func getFeatures(t *testing.T, srv *httptest.Server) featuresBody {
	t.Helper()
	resp, err := http.Get(srv.URL + "/api/v1/features")
	if err != nil {
		t.Fatalf("GET /api/v1/features: %v", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	var body featuresBody
	if err := json.NewDecoder(resp.Body).Decode(&body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	return body
}

func featureServer(t *testing.T, checkUploads bool) *httptest.Server {
	t.Helper()
	res := config.Resolved{Config: config.Defaults(), Provenance: map[string]config.Source{}}
	res.CheckUploads = checkUploads
	if checkUploads {
		res.Provenance["check_uploads"] = config.FromFlag
	}

	ix := &library.Index{}
	api := &Server{
		Store:        library.NewStore(ix),
		Version:      "test",
		BrowseUI:     res.BrowseUI,
		CheckUploads: res.CheckUploads,
		CheckDir:     t.TempDir(),
		Features:     res.Features(),
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv
}

// The registry is only worth serving if it agrees with what the routes actually
// do. A page that reads "check_uploads: enabled" and then gets a 404 from the
// endpoint is worse than a page that never asked - it turns a configuration
// mistake into an apparent server bug.
//
// Both directions, because a description that is right when the feature is on
// and wrong when it is off is exactly the shape of the reporting bug this
// project keeps finding: silence for "off" reads the same as silence for
// "nobody looked".
func TestFeaturesAgreeWithTheRoutesThatAreActuallyRegistered(t *testing.T) {
	for _, on := range []bool{true, false} {
		srv := featureServer(t, on)

		var described *config.Feature
		for i, f := range getFeatures(t, srv).Features {
			if f.Name == "check_uploads" {
				described = &getFeatures(t, srv).Features[i]
			}
		}
		if described == nil {
			t.Fatalf("check_uploads=%v: the registry does not mention check_uploads at all", on)
		}
		if described.Enabled != on {
			t.Errorf("check_uploads=%v: registry says enabled=%v", on, described.Enabled)
		}

		// What the route actually does. POST with no body is enough: an
		// unregistered route is 404, and a registered one is anything else.
		resp, err := http.Post(srv.URL+"/api/v1/check?name=x.sng", "application/octet-stream", http.NoBody)
		if err != nil {
			t.Fatalf("POST /api/v1/check: %v", err)
		}
		resp.Body.Close()

		routeExists := resp.StatusCode != http.StatusNotFound
		if routeExists != described.Enabled {
			t.Errorf("check_uploads=%v: registry says enabled=%v but the route %s (status %d)",
				on, described.Enabled,
				map[bool]string{true: "exists", false: "does not exist"}[routeExists],
				resp.StatusCode)
		}
	}
}

// A server given no registry must report an empty list, not null. "features":
// null reads as a broken endpoint to anything parsing it, and a page that
// iterates the result would throw rather than render nothing.
func TestFeaturesWithNoRegistryIsAnEmptyListNotNull(t *testing.T) {
	api := &Server{Store: library.NewStore(&library.Index{}), Version: "test"}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)

	resp, err := http.Get(srv.URL + "/api/v1/features")
	if err != nil {
		t.Fatalf("GET: %v", err)
	}
	defer resp.Body.Close()

	var raw map[string]json.RawMessage
	if err := json.NewDecoder(resp.Body).Decode(&raw); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if string(raw["features"]) != "[]" {
		t.Errorf("features = %s, want []", raw["features"])
	}
}

// The registry says why, not just what. An operator staring at a disabled
// feature needs the source to tell "I never configured this" from "something I
// configured turned it off".
func TestFeaturesCarryTheirSource(t *testing.T) {
	body := getFeatures(t, featureServer(t, true))
	for _, f := range body.Features {
		switch f.Source {
		case config.FromDefault, config.FromFile, config.FromFlag:
		default:
			t.Errorf("%s: source = %q, which is not one of the three sources", f.Name, f.Source)
		}
	}
}

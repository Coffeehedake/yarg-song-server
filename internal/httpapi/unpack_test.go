package httpapi

import (
	"archive/zip"
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/config"
	"github.com/coffeehedake/yarg-song-server/internal/library"
)

const theSecret = "a-secret-nobody-guessed"

// unpackServer builds a server over a library holding one two-song archive.
func unpackServer(t *testing.T, ops config.WriteAccess, secret string) (*httptest.Server, string) {
	t.Helper()
	root := t.TempDir()

	f, err := os.Create(filepath.Join(root, "pack.zip"))
	if err != nil {
		t.Fatal(err)
	}
	zw := zip.NewWriter(f)
	for _, folder := range []string{"Alpha Song", "Beta Song"} {
		for name, body := range map[string]string{
			"song.ini":  "[song]\nname = " + folder + "\nartist = Someone\n",
			"notes.mid": folder + " chart bytes",
			"song.ogg":  folder + " audio",
		} {
			w, err := zw.Create(folder + "/" + name)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := w.Write([]byte(body)); err != nil {
				t.Fatal(err)
			}
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	f.Close()

	ix, err := library.Build(root)
	if err != nil {
		t.Fatal(err)
	}

	api := &Server{
		Store:       library.NewStore(ix),
		Version:     "test",
		UnpackOps:   ops,
		AdminSecret: secret,
		Unpack:      &UnpackJob{},
	}
	srv := httptest.NewServer(api.Handler())
	t.Cleanup(srv.Close)
	return srv, root
}

func post(t *testing.T, srv *httptest.Server, secret, body string) *http.Response {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+"/api/v1/unpack", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req.Header.Set("Content-Type", "application/json")
	if secret != "" {
		req.Header.Set("X-Admin-Secret", secret)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	return res
}

func TestUnpackIsAbsentRatherThanForbiddenWhenOff(t *testing.T) {
	// The same property every other optional capability has: a disabled feature answers 404,
	// byte-identical to an unknown path, because a 403 would confirm the feature exists.
	srv, _ := unpackServer(t, config.WritesOff, theSecret)

	res := post(t, srv, theSecret, `{"archive":"pack.zip"}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusNotFound {
		t.Errorf("POST with unpack_ops off = %d, want 404", res.StatusCode)
	}

	sres, err := http.Get(srv.URL + "/api/v1/unpack")
	if err != nil {
		t.Fatal(err)
	}
	defer sres.Body.Close()
	if sres.StatusCode != http.StatusNotFound {
		t.Errorf("GET with unpack_ops off = %d, want 404", sres.StatusCode)
	}
}

func TestUnpackRefusesAWrongOrMissingSecret(t *testing.T) {
	srv, root := unpackServer(t, config.WritesLAN, theSecret)

	for _, given := range []string{"", "wrong", theSecret + "x"} {
		res := post(t, srv, given, `{"archive":"pack.zip"}`)
		if res.StatusCode != http.StatusForbidden {
			t.Errorf("secret %q = %d, want 403", given, res.StatusCode)
		}
		res.Body.Close()
	}

	// And nothing was unpacked while it was being refused.
	if _, err := os.Stat(filepath.Join(root, "pack")); !os.IsNotExist(err) {
		t.Error("a refused request still unpacked something")
	}
}

func TestCheckingReportsThePlanAndWritesNothing(t *testing.T) {
	srv, root := unpackServer(t, config.WritesLAN, theSecret)

	res := post(t, srv, theSecret, `{"archive":"pack.zip","check":true}`)
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("check = %d, want 200", res.StatusCode)
	}

	var plan struct {
		Dest  string `json:"dest"`
		Songs int    `json:"songs"`
		Bytes int64  `json:"bytes"`
	}
	if err := json.NewDecoder(res.Body).Decode(&plan); err != nil {
		t.Fatal(err)
	}
	if plan.Songs != 2 {
		t.Errorf("songs = %d, want 2", plan.Songs)
	}
	if plan.Bytes == 0 {
		t.Error("bytes = 0, so an operator cannot judge whether the disk will take it")
	}
	if _, err := os.Stat(filepath.Join(root, plan.Dest)); !os.IsNotExist(err) {
		t.Error("checking created the destination")
	}
}

func TestUnpackingRunsAsAJobAndReportsWhenItIsDone(t *testing.T) {
	srv, root := unpackServer(t, config.WritesLAN, theSecret)

	res := post(t, srv, theSecret, `{"archive":"pack.zip"}`)
	if res.StatusCode != http.StatusAccepted {
		t.Fatalf("unpack = %d, want 202 - it must not block the caller", res.StatusCode)
	}
	res.Body.Close()

	deadline := time.Now().Add(30 * time.Second)
	var status struct {
		Running bool   `json:"running"`
		Error   string `json:"error"`
		Result  struct {
			SongsFound int `json:"songs_found"`
		} `json:"result"`
	}
	for time.Now().Before(deadline) {
		req, _ := http.NewRequest(http.MethodGet, srv.URL+"/api/v1/unpack", nil)
		req.Header.Set("X-Admin-Secret", theSecret)
		sres, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		var buf bytes.Buffer
		buf.ReadFrom(sres.Body)
		sres.Body.Close()
		if err := json.Unmarshal(buf.Bytes(), &status); err != nil {
			t.Fatalf("status: %v (%s)", err, buf.String())
		}
		if !status.Running {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if status.Running {
		t.Fatal("the job never finished")
	}
	if status.Error != "" {
		t.Fatalf("the job failed: %s", status.Error)
	}
	if status.Result.SongsFound != 2 {
		t.Errorf("songs_found = %d, want 2", status.Result.SongsFound)
	}
	for _, folder := range []string{"Alpha Song", "Beta Song"} {
		if _, err := os.Stat(filepath.Join(root, "pack", folder, "notes.mid")); err != nil {
			t.Errorf("%s was not unpacked: %v", folder, err)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "pack.zip")); err != nil {
		t.Errorf("the archive was not left alone: %v", err)
	}
}

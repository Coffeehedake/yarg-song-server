package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/config"
)

func writeConf(t *testing.T, dir, body string) string {
	t.Helper()
	p := filepath.Join(dir, "yss.conf")
	if err := os.WriteFile(p, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	return p
}

// Defaults < config file < flags actually typed. Getting this wrong is silent:
// the server runs, and runs with settings the operator did not choose.
func TestResolvePrecedence(t *testing.T) {
	dir := t.TempDir()
	conf := writeConf(t, dir, "listen = :7777\nsongs = /from/file\n")

	// A flag left alone carries its default value, and that value must NOT
	// overwrite the file. This is the case a naive implementation gets wrong.
	flagged := config.Config{Listen: ":8080", Songs: "./songs", Data: "/from/flag"}

	got, err := resolve(conf, flagged, map[string]bool{"data": true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":7777" {
		t.Errorf("listen = %q, want the file's value - the flag was not given", got.Listen)
	}
	if got.Songs != "/from/file" {
		t.Errorf("songs = %q, want the file's value", got.Songs)
	}
	if got.Data != "/from/flag" {
		t.Errorf("data = %q, want the flag's value - it was given explicitly", got.Data)
	}
}

func TestResolveFlagBeatsFile(t *testing.T) {
	dir := t.TempDir()
	conf := writeConf(t, dir, "listen = :7777\n")
	got, err := resolve(conf, config.Config{Listen: ":9999"}, map[string]bool{"listen": true})
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":9999" {
		t.Fatalf("listen = %q, want the flag to win", got.Listen)
	}
}

func TestResolveNoFileGivesDefaults(t *testing.T) {
	// Run from a directory with no conventional config file, which is what a
	// first run looks like.
	t.Chdir(t.TempDir())

	got, err := resolve("", config.Defaults(), nil)
	if err != nil {
		t.Fatalf("a first run with no config file must not be an error: %v", err)
	}
	if got != config.Defaults() {
		t.Fatalf("got %+v, want the defaults", got)
	}
}

// A file the operator NAMED and that is not there is a mistake, not a default.
// The conventional file simply being absent is the normal first run. The two
// look alike and must not behave alike.
func TestResolveNamedButMissingFileIsFatal(t *testing.T) {
	t.Chdir(t.TempDir())
	if _, err := resolve(filepath.Join(t.TempDir(), "nope.conf"), config.Defaults(), nil); err == nil {
		t.Fatal("a config file named on the command line and missing was ignored")
	}
}

// A malformed conventional file is fatal even though its absence is not: a
// settings file the server could not read is not one it should guess around.
func TestResolveMalformedConventionalFileIsFatal(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(config.DefaultPath, []byte("listen :9999\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := resolve("", config.Defaults(), nil); err == nil {
		t.Fatal("a malformed config file in the conventional location was ignored")
	}
}

// The conventional file IS picked up without being named.
func TestResolveConventionalFileIsFound(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir)
	if err := os.WriteFile(config.DefaultPath, []byte("listen = :6543\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	got, err := resolve("", config.Defaults(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if got.Listen != ":6543" {
		t.Fatalf("listen = %q, want the conventional file to be read", got.Listen)
	}
}

package main

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/coffeehedake/yarg-song-server/internal/config"
)

// Each of the three sources must be reported as itself. Without this the
// server can say what a setting is and not why, and "why" is the question an
// operator actually has when a feature they configured is off.
func TestResolveAllRecordsWhereEachValueCameFrom(t *testing.T) {
	dir := t.TempDir()
	conf := writeConf(t, dir, "check_uploads = yes\n")

	flagged := config.Config{BrowseUI: false}
	res, err := resolveAll(conf, flagged, map[string]bool{"browse-ui": true})
	if err != nil {
		t.Fatal(err)
	}

	if got := res.Provenance["check_uploads"]; got != config.FromFile {
		t.Errorf("check_uploads came from %q, want %q", got, config.FromFile)
	}
	if got := res.Provenance["browse_ui"]; got != config.FromFlag {
		t.Errorf("browse_ui came from %q, want %q", got, config.FromFlag)
	}
	if _, ok := res.Provenance["pack_cache_max"]; ok {
		t.Errorf("pack_cache_max was recorded as configured, but nothing set it")
	}
	if res.File != conf {
		t.Errorf("File = %q, want %q", res.File, conf)
	}
}

// The provenance map is keyed by the CONFIG-FILE spelling even when a flag set
// the value. Two spellings for one setting is how the registry ends up looking
// up "browse_ui" and finding nothing because a flag wrote "browse-ui".
func TestFlagProvenanceUsesTheConfigFileSpelling(t *testing.T) {
	res, err := resolveAll("", config.Config{CheckUploads: true},
		map[string]bool{"check-uploads": true})
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := res.Provenance["check-uploads"]; ok {
		t.Error("provenance recorded the hyphenated flag name; the registry looks up underscores")
	}
	if got := res.Provenance["check_uploads"]; got != config.FromFlag {
		t.Errorf("check_uploads came from %q, want %q", got, config.FromFlag)
	}
	if f := findFeature(t, res, "check_uploads"); f.Source != config.FromFlag || !f.Enabled {
		t.Errorf("registry: enabled=%v source=%q, want enabled from the flag", f.Enabled, f.Source)
	}
}

// A file that sets a key to the value it already had is STILL the file
// speaking. This is the case a "diff the config before and after" shortcut gets
// wrong, and it is the case an operator asks about: they wrote browse_ui = yes,
// it is on, and they want to know whether their file is being read at all.
func TestAFileSettingADefaultStillCountsAsTheFile(t *testing.T) {
	dir := t.TempDir()
	conf := writeConf(t, dir, "browse_ui = yes\n") // yes IS the default

	res, err := resolveAll(conf, config.Config{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if f := findFeature(t, res, "browse_ui"); f.Source != config.FromFile {
		t.Errorf("browse_ui source = %q, want %q - the file set it, to its own default",
			f.Source, config.FromFile)
	}
}

// No file read means File is empty, and that is what the startup log turns into
// "no config file read". A server that had claimed to load one would be lying
// about the only thing that distinguishes "your file says no" from "your file
// was never opened".
func TestNoFileMeansNoFileIsReported(t *testing.T) {
	dir := t.TempDir()
	t.Chdir(dir) // the conventional file is looked for in the working directory

	res, err := resolveAll("", config.Config{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != "" {
		t.Errorf("File = %q, want empty - there was no config file", res.File)
	}
	for _, f := range res.Features() {
		if f.Source != config.FromDefault {
			t.Errorf("%s: source = %q with nothing configured, want %q",
				f.Name, f.Source, config.FromDefault)
		}
	}
}

// The conventional file in the working directory is read, and reported by name.
func TestTheConventionalFileIsNamedWhenItIsUsed(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, config.DefaultPath),
		[]byte("check_uploads = yes\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Chdir(dir)

	res, err := resolveAll("", config.Config{}, map[string]bool{})
	if err != nil {
		t.Fatal(err)
	}
	if res.File != config.DefaultPath {
		t.Errorf("File = %q, want %q", res.File, config.DefaultPath)
	}
	if !res.CheckUploads {
		t.Error("check_uploads was not applied from the conventional file")
	}
}

func findFeature(t *testing.T, res config.Resolved, name string) config.Feature {
	t.Helper()
	for _, f := range res.Features() {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no feature %q in the registry", name)
	return config.Feature{}
}

package config

import (
	"strings"
	"testing"
)

func find(t *testing.T, fs []Feature, name string) Feature {
	t.Helper()
	for _, f := range fs {
		if f.Name == name {
			return f
		}
	}
	t.Fatalf("no feature %q in registry; got %d features", name, len(fs))
	return Feature{}
}

// A registry built from nothing must still answer honestly rather than with
// empty strings. A zero Resolved is what a caller gets if it forgets to record
// provenance, and "source": "" in an API response is worse than a wrong answer
// because it reads as a bug in the reader.
func TestAZeroResolvedReportsDefaults(t *testing.T) {
	var r Resolved
	r.Config = Defaults()

	for _, f := range r.Features() {
		if f.Source != FromDefault {
			t.Errorf("%s: source = %q, want %q", f.Name, f.Source, FromDefault)
		}
		if f.Enabled != f.Default {
			t.Errorf("%s: enabled = %v but default = %v with nothing configured",
				f.Name, f.Enabled, f.Default)
		}
		if f.EnableWith == "" {
			t.Errorf("%s: no enable_with; the reader who needs it is looking at a feature that is OFF", f.Name)
		}
	}
}

// The registry must describe the config it was built from, not the defaults it
// wishes it had. This is the assertion that fails if Features() ever starts
// reading Defaults() for Enabled.
func TestFeaturesDescribeTheResolvedConfigNotTheDefaults(t *testing.T) {
	r := Resolved{Config: Defaults(), Provenance: map[string]Source{}}
	r.CheckUploads = true // off by default
	r.BrowseUI = false    // on by default
	r.Provenance["check_uploads"] = FromFile
	r.Provenance["browse_ui"] = FromFlag

	check := find(t, r.Features(), "check_uploads")
	if !check.Enabled || check.Default {
		t.Errorf("check_uploads: enabled=%v default=%v, want enabled with a false default",
			check.Enabled, check.Default)
	}
	if check.Source != FromFile {
		t.Errorf("check_uploads: source = %q, want %q", check.Source, FromFile)
	}

	browse := find(t, r.Features(), "browse_ui")
	if browse.Enabled || !browse.Default {
		t.Errorf("browse_ui: enabled=%v default=%v, want disabled with a true default",
			browse.Enabled, browse.Default)
	}
	if browse.Source != FromFlag {
		t.Errorf("browse_ui: source = %q, want %q", browse.Source, FromFlag)
	}
}

// The registry is served unauthenticated. Paths and the listen address are
// settings, not capabilities, and must not ride along in it - a LAN server's
// filesystem layout is nobody's business. Asserted rather than left as a
// comment, because "just return the config" is the obvious future shortcut.
func TestFeaturesCarryNoPaths(t *testing.T) {
	r := Resolved{Config: Defaults(), Provenance: map[string]Source{}}
	r.Songs = "/mnt/secret-library"
	r.Data = "/mnt/secret-data"
	r.Listen = "10.9.8.7:9999"

	for _, f := range r.Features() {
		blob := strings.Join([]string{f.Name, f.Description, f.Endpoint, f.EnableWith}, " ")
		for _, leak := range []string{"secret-library", "secret-data", "10.9.8.7"} {
			if strings.Contains(blob, leak) {
				t.Errorf("feature %q leaks %q: %s", f.Name, leak, blob)
			}
		}
	}
}

// Every setting the file parser accepts as a capability must appear in the
// registry. This is the drift guard: adding a feature flag without adding it
// here would leave a capability the server has and never reports.
func TestEveryBooleanSettingIsInTheRegistry(t *testing.T) {
	r := Resolved{Config: Defaults(), Provenance: map[string]Source{}}
	got := map[string]bool{}
	for _, f := range r.Features() {
		got[f.Name] = true
	}
	for _, want := range []string{"browse_ui", "check_uploads"} {
		if !got[want] {
			t.Errorf("%q is a capability the server has and the registry does not mention", want)
		}
	}
}

// ApplyTracked must report a key the file set even when it set it to the value
// it already had. A diff of the config before and after would call that a
// default, which is the exact case an operator asks about: "I wrote it down and
// nothing happened."
func TestApplyTrackedReportsAKeySetToItsOwnDefault(t *testing.T) {
	c := Defaults()
	keys, err := ApplyTracked(&c, strings.NewReader("browse_ui = yes\n"))
	if err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	if len(keys) != 1 || keys[0] != "browse_ui" {
		t.Fatalf("keys = %v, want [browse_ui]", keys)
	}
}

// Comments and blank lines are not settings.
func TestApplyTrackedIgnoresCommentsAndBlanks(t *testing.T) {
	c := Defaults()
	keys, err := ApplyTracked(&c, strings.NewReader("# a comment\n\n   \n# another\n"))
	if err != nil {
		t.Fatalf("ApplyTracked: %v", err)
	}
	if len(keys) != 0 {
		t.Fatalf("keys = %v, want none", keys)
	}
}

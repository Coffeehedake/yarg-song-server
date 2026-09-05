package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestApply(t *testing.T) {
	c := Defaults()
	in := `
# a comment
listen = 127.0.0.1:9000

songs = /srv/songs
  data   =   /var/lib/yss
`
	if err := Apply(&c, strings.NewReader(in)); err != nil {
		t.Fatal(err)
	}
	if c.Listen != "127.0.0.1:9000" || c.Songs != "/srv/songs" || c.Data != "/var/lib/yss" {
		t.Fatalf("got %+v", c)
	}
}

// Only the keys present are touched, so a file that sets one thing does not
// silently reset the other two to zero values.
func TestApplyLeavesUnmentionedKeysAlone(t *testing.T) {
	c := Defaults()
	if err := Apply(&c, strings.NewReader("listen = :9999\n")); err != nil {
		t.Fatal(err)
	}
	if c.Songs != Defaults().Songs || c.Data != Defaults().Data {
		t.Fatalf("an unmentioned key was overwritten: %+v", c)
	}
}

// The whole point of refusing unknown keys: a typo that is ignored leaves an
// operator certain they changed something they did not.
func TestUnknownKeyIsAnError(t *testing.T) {
	c := Defaults()
	err := Apply(&c, strings.NewReader("song = /srv/songs\n")) // "song", not "songs"
	if err == nil {
		t.Fatal("a misspelled setting was accepted silently")
	}
	if !strings.Contains(err.Error(), "song") || !strings.Contains(err.Error(), "songs") {
		t.Errorf("the error should name both the typo and the valid settings; got %v", err)
	}
}

func TestMalformedLineIsAnError(t *testing.T) {
	c := Defaults()
	if err := Apply(&c, strings.NewReader("listen :9999\n")); err == nil {
		t.Fatal("a line with no = was accepted")
	}
}

// A path with meaningful trailing whitespace can be written unambiguously,
// without quotes being required for ordinary values.
func TestQuotedValues(t *testing.T) {
	cases := map[string]string{
		`songs = "/srv/my songs "`: "/srv/my songs ",
		`songs = '/srv/songs'`:     "/srv/songs",
		`songs = /srv/songs`:       "/srv/songs",
		`songs = "unclosed`:        `"unclosed`,
	}
	for in, want := range cases {
		c := Defaults()
		if err := Apply(&c, strings.NewReader(in)); err != nil {
			t.Fatalf("%s: %v", in, err)
		}
		if c.Songs != want {
			t.Errorf("%s -> %q, want %q", in, c.Songs, want)
		}
	}
}

func TestLoadFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "yss.conf")
	if err := os.WriteFile(path, []byte("listen = :7777\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	c := Defaults()
	if err := LoadFile(&c, path); err != nil {
		t.Fatal(err)
	}
	if c.Listen != ":7777" {
		t.Fatalf("listen = %q", c.Listen)
	}

	if err := LoadFile(&c, filepath.Join(dir, "nope.conf")); err == nil {
		t.Fatal("a missing file must be an error here; whether that is fatal is the caller's call")
	}
}

// The shipped example must actually be valid, and must not change any setting -
// every line in it is commented out, so a new operator who uncomments nothing
// gets the defaults.
func TestExampleIsValidAndInert(t *testing.T) {
	c := Defaults()
	if err := Apply(&c, strings.NewReader(Example)); err != nil {
		t.Fatalf("the shipped example does not parse: %v", err)
	}
	if c != Defaults() {
		t.Fatalf("the shipped example changed a setting: %+v", c)
	}
	for _, key := range []string{"listen", "songs", "data"} {
		if !strings.Contains(Example, key) {
			t.Errorf("the example does not mention %q", key)
		}
	}
}

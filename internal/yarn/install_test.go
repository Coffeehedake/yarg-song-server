package yarn

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSafeTargetRefusesTraversal(t *testing.T) {
	root, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Every one of these is a real thing an archive can be made to contain. The project's Go
	// side already found that a zip with BACKSLASH separators behaves differently from one
	// with slashes, which is why both spellings are here rather than just the obvious one.
	hostile := []string{
		"../evil.yargsong",
		"../../evil.yargsong",
		"a/../../evil.yargsong",
		`..\evil.yargsong`,
		`a\..\..\evil.yargsong`,
	}
	for _, name := range hostile {
		if _, err := safeTarget(root, name); err == nil {
			// On Linux a backslash is a legal filename character, so `..\evil` is not
			// traversal there and is correctly allowed. Only assert the slash forms
			// everywhere; assert the backslash forms where they mean something.
			if strings.Contains(name, `\`) && os.PathSeparator != '\\' {
				continue
			}
			t.Errorf("safeTarget accepted %q, which escapes the destination", name)
		}
	}
}

func TestSafeTargetAcceptsOrdinaryNames(t *testing.T) {
	root, err := filepath.Abs(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	// Real entry names from Children's Keys Pack 3. A guard that refuses these would be
	// useless in a way no traversal test would catch.
	ok := []string{
		"Thomas Arne - A-Hunting We Will Go.yargsong",
		"Percy Montross - Oh My Darling, Clementine.yargsong",
		"nested/dir/song.yargsong",
	}
	for _, name := range ok {
		got, err := safeTarget(root, name)
		if err != nil {
			t.Errorf("safeTarget refused an ordinary entry %q: %v", name, err)
			continue
		}
		if !strings.HasPrefix(got, root) {
			t.Errorf("safeTarget(%q) = %q, which is not under %q", name, got, root)
		}
	}
}

func TestSafeNameStripsPathAndReservedCharacters(t *testing.T) {
	cases := map[string]string{
		"YARG Official Setlist":  "YARG Official Setlist",
		"Children's Keys Pack 3": "Children's Keys Pack 3",
		// Separators become underscores AND the leading dots are trimmed, so this cannot be
		// a relative path or a dotfile. Checked rather than assumed: the first version of
		// this test expected ".._.._etc" and had forgotten the trim.
		"../../etc":         "_.._etc",
		`a\b:c*d?e"f<g>h|i`: "a_b_c_d_e_f_g_h_i",
		"   ":               "pack",
		"":                  "pack",
	}
	for in, want := range cases {
		if got := safeName(in); got != want {
			t.Errorf("safeName(%q) = %q, want %q", in, got, want)
		}
	}
}

func TestReadManifestAbsentIsNotAnError(t *testing.T) {
	// "No manifest" is the normal state of a directory we have never installed into, and
	// treating it as an error would make the first install of every pack a failure.
	m, err := ReadManifest(t.TempDir())
	if err != nil {
		t.Fatalf("ReadManifest on an empty dir: %v", err)
	}
	if m != nil {
		t.Fatalf("ReadManifest on an empty dir returned %+v, want nil", m)
	}
}

func TestArchivePasswordShape(t *testing.T) {
	p := archivePassword()
	if len(p) != 64 {
		t.Errorf("password length = %d, want 64", len(p))
	}
	for i, r := range p {
		if !strings.ContainsRune(letters, r) {
			t.Errorf("password[%d] = %q, which is not in the alphabet", i, r)
		}
	}
}

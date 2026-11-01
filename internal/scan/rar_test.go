package scan

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// There is no RAR writer for Go - rardecode decodes only - and 7-Zip cannot create RAR either,
// so this package cannot synthesise a .rar fixture the way it synthesises zips. The options
// were a hand-rolled RAR5 encoder or no committed fixture, and the encoder is the worse one:
// this repo's own rule is that round-tripping against our own writer "only proves our reader
// and writer agree with each other", and a fixture my encoder produced would test my encoder.
//
// So the split is deliberate. Everything decidable from a NAME or a few header bytes is tested
// here and runs everywhere. The behaviour that needs a real archive is in
// TestRarContainerAgainstARealArchive, gated on YARG_RAR_LIVE, and was run against both
// archives this feature was added for.

func TestRarIsAContainer(t *testing.T) {
	for _, name := range []string{
		"Songs.rar",
		"Songs.RAR",
		"a/b/Marathon Hero 2.rar",
		"Songs.part1.rar",
		"Songs.part01.rar",
		"Songs.part001.rar",
	} {
		if !IsContainer(name) {
			t.Errorf("IsContainer(%q) = false, want true", name)
		}
	}
}

// A continuation part is NOT an entry point. Given part 2 on its own, rardecode either fails -
// one noisy error per part - or finds part 1 beside it and succeeds, which is worse: every song
// in the set then gets indexed once per part, and the library reports the wrong archive as a
// song's source.
func TestSecondaryRarVolumesAreNotContainers(t *testing.T) {
	for _, name := range []string{
		"Songs.part2.rar",
		"Songs.part02.rar",
		"Songs.part002.rar",
		"Songs.PART2.RAR",
		"Songs.part10.rar",
		"a/b/Big Pack.part17.rar",
	} {
		if IsContainer(name) {
			t.Errorf("IsContainer(%q) = true; a continuation part must not be an entry point", name)
		}
		if !IsSecondaryRarVolume(name) {
			t.Errorf("IsSecondaryRarVolume(%q) = false, want true", name)
		}
	}
}

// Padding is a writer's choice - part1, part01 and part001 all occur - so the digits have to be
// compared as a number. Comparing them as text makes "part01" a continuation part and loses the
// whole set.
func TestFirstRarVolumeIsAnEntryPointWhateverThePadding(t *testing.T) {
	for _, name := range []string{"S.part1.rar", "S.part01.rar", "S.part001.rar", "S.part0001.rar"} {
		if IsSecondaryRarVolume(name) {
			t.Errorf("IsSecondaryRarVolume(%q) = true; part 1 opens the set", name)
		}
	}
}

// The old convention is X.rar, X.r00, X.r01. Those continuation parts do not end in .rar, so
// IsContainer never offered them - this pins that, so nobody "helpfully" adds .r00 later.
func TestOldStyleRarContinuationPartsAreNotContainers(t *testing.T) {
	for _, name := range []string{"Songs.r00", "Songs.r01", "Songs.r15"} {
		if IsContainer(name) {
			t.Errorf("IsContainer(%q) = true; .r00-style parts are not entry points", name)
		}
	}
}

// A name ending in .rar is not evidence of a RAR. looksRar is what separates "a real archive we
// could not read", which earns an operator an explanation, from a mis-named file, which does not.
func TestLooksRarChecksTheSignatureNotTheName(t *testing.T) {
	dir := t.TempDir()

	real5 := filepath.Join(dir, "five.rar")
	if err := os.WriteFile(real5, []byte("Rar!\x1a\x07\x01\x00rest"), 0o644); err != nil {
		t.Fatal(err)
	}
	real4 := filepath.Join(dir, "four.rar")
	if err := os.WriteFile(real4, []byte("Rar!\x1a\x07\x00rest"), 0o644); err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(dir, "fake.rar")
	if err := os.WriteFile(fake, []byte("this is just text"), 0o644); err != nil {
		t.Fatal(err)
	}
	// Shorter than the signature: the question is "is this really a RAR", and the answer is no
	// rather than a read error.
	stub := filepath.Join(dir, "stub.rar")
	if err := os.WriteFile(stub, []byte("Rar"), 0o644); err != nil {
		t.Fatal(err)
	}

	if !looksRar(real5) {
		t.Error("a RAR 5.0 signature was not recognised")
	}
	if !looksRar(real4) {
		t.Error("a RAR 1.5-4.x signature was not recognised")
	}
	if looksRar(fake) {
		t.Error("a text file named .rar was reported as a RAR")
	}
	if looksRar(stub) {
		t.Error("a file shorter than the signature was reported as a RAR")
	}
	if looksRar(filepath.Join(dir, "absent.rar")) {
		t.Error("a missing file was reported as a RAR")
	}
}

// looks7z and looksRar now share one implementation, so this keeps the 7z half honest too - a
// refactor that broke it would otherwise only show up as a worse error message.
func TestSharingTheSignatureCheckDidNotBreakThe7zOne(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "a.7z")
	if err := os.WriteFile(p, append(sevenZSignature, 'x'), 0o644); err != nil {
		t.Fatal(err)
	}
	if !looks7z(p) {
		t.Error("looks7z no longer recognises a 7z signature")
	}
	q := filepath.Join(dir, "b.7z")
	if err := os.WriteFile(q, []byte("not an archive"), 0o644); err != nil {
		t.Fatal(err)
	}
	if looks7z(q) {
		t.Error("looks7z accepted a file that is not a 7z")
	}
}

// A mis-named file must not be dressed up as a readable archive. It also must not come back as
// ErrUnopenableRar, because that message tells the operator to go and find the other volumes of
// a set that does not exist.
func TestAFileMerelyNamedRarIsNotReportedAsAnUnopenableArchive(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "notreally.rar")
	if err := os.WriteFile(p, []byte("plain text, no signature"), 0o644); err != nil {
		t.Fatal(err)
	}

	_, _, err := archiveItems(p)
	if err == nil {
		t.Fatal("a text file named .rar opened as an archive")
	}
	if errors.Is(err, ErrUnopenableRar) {
		t.Errorf("err = %v; that message is for a REAL rar we could not read", err)
	}
}

// TestRarContainerAgainstARealArchive is the part that needs a file from the wild.
//
//	YARG_RAR_LIVE="$env:USERPROFILE\Downloads\Guitar Hero On Tour - Modern Hits.rar" go test ./internal/scan/ -run TestRarContainerAgainstARealArchive -v
func TestRarContainerAgainstARealArchive(t *testing.T) {
	p := os.Getenv("YARG_RAR_LIVE")
	if p == "" {
		t.Skip("set YARG_RAR_LIVE to a real .rar to run")
	}
	if !IsContainer(filepath.Base(p)) {
		t.Fatalf("%s is not treated as a container", filepath.Base(p))
	}
	if !looksRar(p) {
		t.Fatalf("%s does not carry a RAR signature", p)
	}

	// The entries have to be enumerable without decompressing anything, because that is what
	// the library walk does to every archive on startup.
	items, closer, err := archiveItems(p)
	if err != nil {
		t.Fatalf("archiveItems: %v", err)
	}
	defer closer.Close()
	if len(items) == 0 {
		t.Fatal("no entries")
	}
	t.Logf("%d entries", len(items))

	// And the songs have to come out through the SAME path a zip takes, which is the only
	// claim that matters: a song does not become a different song because somebody rar'd it.
	songs, err := ScanContainerAll(p)
	if err != nil {
		t.Fatalf("ScanContainerAll: %v", err)
	}
	if len(songs) == 0 {
		t.Fatal("no songs found in a real song archive")
	}
	t.Logf("%d songs", len(songs))
	for i, s := range songs {
		if i >= 3 {
			break
		}
		t.Logf("  %q by %q  inner=%q", s.Song.Name, s.Song.Artist, s.Inner)
	}

	// Every song must be packable, since that is what serving one does. Checking only the
	// first would miss an archive whose later entries are unreachable.
	for _, s := range songs {
		if err := PackPathInner(p, s.Inner, discardWriter{}); err != nil {
			t.Fatalf("pack %q (inner %q): %v", s.Song.Name, s.Inner, err)
		}
	}
}

type discardWriter struct{}

func (discardWriter) Write(p []byte) (int, error) { return len(p), nil }

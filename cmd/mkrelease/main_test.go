package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/config"
)

// fakeDist writes a stand-in binary for every file the release job produces, so
// the packaging can be tested without cross-compiling twelve real binaries.
func fakeDist(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, tg := range targets {
		for _, cmd := range commands {
			p := filepath.Join(dir, binaryName(cmd, tg))
			if err := os.WriteFile(p, []byte("binary:"+binaryName(cmd, tg)), 0o755); err != nil {
				t.Fatal(err)
			}
		}
	}
	return dir
}

func build(t *testing.T) string {
	t.Helper()
	dist := fakeDist(t)
	out := t.TempDir()
	lic := filepath.Join(t.TempDir(), "LICENSE")
	if err := os.WriteFile(lic, []byte("GNU LESSER GENERAL PUBLIC LICENSE"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dist, out, "v1.2.3", lic); err != nil {
		t.Fatalf("run: %v", err)
	}
	return out
}

func zipContents(t *testing.T, p string) map[string][]byte {
	t.Helper()
	r, err := zip.OpenReader(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer r.Close()
	got := map[string][]byte{}
	for _, f := range r.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatal(err)
		}
		got[f.Name] = b
	}
	return got
}

func tarContents(t *testing.T, p string) (map[string][]byte, map[string]int64) {
	t.Helper()
	f, err := os.Open(p)
	if err != nil {
		t.Fatalf("open %s: %v", p, err)
	}
	defer f.Close()
	gz, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	tr := tar.NewReader(gz)
	body := map[string][]byte{}
	mode := map[string]int64{}
	for {
		h, err := tr.Next()
		if err == io.EOF {
			break
		}
		if err != nil {
			t.Fatal(err)
		}
		b, err := io.ReadAll(tr)
		if err != nil {
			t.Fatal(err)
		}
		body[h.Name] = b
		mode[h.Name] = h.Mode
	}
	return body, mode
}

// Every platform gets an archive, and every archive carries BOTH binaries.
// Shipping the server without the sync client is half a release: an unmodified
// YARG cannot use the server on its own.
func TestEveryArchiveCarriesBothBinaries(t *testing.T) {
	out := build(t)

	for _, tg := range targets {
		p := filepath.Join(out, archiveName("v1.2.3", tg))
		if _, err := os.Stat(p); err != nil {
			t.Errorf("%s/%s: no archive: %v", tg.GOOS, tg.GOARCH, err)
			continue
		}

		var names map[string][]byte
		if tg.Windows {
			names = zipContents(t, p)
		} else {
			names, _ = tarContents(t, p)
		}

		root := stem("v1.2.3", tg)
		want := []string{"yarg-song-server", "yarg-sync", "yarg-song-server.conf", "README.txt", "LICENSE"}
		if tg.Windows {
			want = []string{"yarg-song-server.exe", "yarg-sync.exe", "yarg-song-server.conf", "README.txt", "LICENSE", "start-server.cmd"}
		} else {
			want = append(want, "start-server.sh")
		}
		for _, w := range want {
			if _, ok := names[root+"/"+w]; !ok {
				t.Errorf("%s: missing %s", archiveName("v1.2.3", tg), w)
			}
		}
		for name := range names {
			if !strings.HasPrefix(name, root+"/") {
				t.Errorf("%s: %q is outside the archive's own folder; unzipping would scatter it",
					archiveName("v1.2.3", tg), name)
			}
		}
	}
}

// The archive must carry the binary for ITS platform. Shipping the linux/amd64
// server inside the Windows zip is the kind of mistake that only shows up on
// somebody else's machine, and every file in every archive would still be
// present and correctly named.
func TestEachArchiveCarriesItsOwnPlatformsBinary(t *testing.T) {
	out := build(t)

	for _, tg := range targets {
		p := filepath.Join(out, archiveName("v1.2.3", tg))
		var names map[string][]byte
		if tg.Windows {
			names = zipContents(t, p)
		} else {
			names, _ = tarContents(t, p)
		}
		root := stem("v1.2.3", tg)

		name := "yarg-song-server"
		if tg.Windows {
			name += ".exe"
		}
		got := string(names[root+"/"+name])
		want := "binary:" + binaryName("yarg-song-server", tg)
		if got != want {
			t.Errorf("%s: contains %q, want %q", archiveName("v1.2.3", tg), got, want)
		}
	}
}

// The shipped config template is config.Example itself. A second copy would
// agree with the real settings for a while and then quietly stop, and the
// person it misleads is the one who trusted the file in the download.
func TestTheShippedConfigIsTheRealTemplate(t *testing.T) {
	out := build(t)
	tg := targets[0]
	names, _ := tarContents(t, filepath.Join(out, archiveName("v1.2.3", tg)))
	got := string(names[stem("v1.2.3", tg)+"/yarg-song-server.conf"])
	if got != config.Example {
		t.Error("the shipped yarg-song-server.conf is not config.Example")
	}
}

// A downloaded binary that needs a chmod before it runs is a bad first five
// minutes, so the tar must carry the executable bit.
func TestUnixArchivesShipExecutableBinaries(t *testing.T) {
	out := build(t)
	for _, tg := range targets {
		if tg.Windows {
			continue
		}
		_, mode := tarContents(t, filepath.Join(out, archiveName("v1.2.3", tg)))
		root := stem("v1.2.3", tg)
		for _, f := range []string{"yarg-song-server", "yarg-sync", "start-server.sh"} {
			if m := mode[root+"/"+f]; m&0o111 == 0 {
				t.Errorf("%s: %s has mode %o, not executable", archiveName("v1.2.3", tg), f, m)
			}
		}
		if m := mode[root+"/README.txt"]; m&0o111 != 0 {
			t.Errorf("%s: README.txt is executable (%o)", archiveName("v1.2.3", tg), m)
		}
	}
}

// Two runs over the same binaries must produce byte-identical archives.
// Otherwise SHA256SUMS says nothing: every rebuild would differ, and a real
// change would be indistinguishable from a rebuild. This project has already
// paid for one "deterministic" claim that was not.
func TestArchivesAreByteIdenticalAcrossRuns(t *testing.T) {
	dist := fakeDist(t)
	lic := filepath.Join(t.TempDir(), "LICENSE")
	if err := os.WriteFile(lic, []byte("licence"), 0o644); err != nil {
		t.Fatal(err)
	}

	outA, outB := t.TempDir(), t.TempDir()
	if err := run(dist, outA, "v1.2.3", lic); err != nil {
		t.Fatal(err)
	}
	if err := run(dist, outB, "v1.2.3", lic); err != nil {
		t.Fatal(err)
	}

	for _, tg := range targets {
		n := archiveName("v1.2.3", tg)
		a, err := os.ReadFile(filepath.Join(outA, n))
		if err != nil {
			t.Fatal(err)
		}
		b, err := os.ReadFile(filepath.Join(outB, n))
		if err != nil {
			t.Fatal(err)
		}
		if string(a) != string(b) {
			t.Errorf("%s differs between two runs over identical input (%d vs %d bytes)", n, len(a), len(b))
		}
	}
}

// The cross-run test above CANNOT catch a time.Now() epoch, and that is worth
// stating rather than discovering later: `var epoch = time.Now()` is evaluated
// once per process, so both runs in one test share it and the archives match.
// Red-proofing it produced a green, which is the exact failure this project
// keeps meeting - a test that can fail, but not for the reason it exists.
//
// So assert the stored timestamp against a date written HERE, independent of
// whatever the program's epoch happens to be. A real build in 2027 must still
// stamp 2020-01-01, or SHA256SUMS stops meaning anything across releases.
func TestArchivesStampAFixedTimestampNotTheClock(t *testing.T) {
	want := time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)
	out := build(t)

	for _, tg := range targets {
		p := filepath.Join(out, archiveName("v1.2.3", tg))
		if tg.Windows {
			r, err := zip.OpenReader(p)
			if err != nil {
				t.Fatal(err)
			}
			for _, f := range r.File {
				if got := f.Modified.UTC(); !got.Equal(want) {
					t.Errorf("%s: %s stamped %s, want %s", archiveName("v1.2.3", tg), f.Name, got, want)
				}
			}
			r.Close()
			continue
		}

		f, err := os.Open(p)
		if err != nil {
			t.Fatal(err)
		}
		gz, err := gzip.NewReader(f)
		if err != nil {
			t.Fatal(err)
		}
		tr := tar.NewReader(gz)
		for {
			h, err := tr.Next()
			if err == io.EOF {
				break
			}
			if err != nil {
				t.Fatal(err)
			}
			if got := h.ModTime.UTC(); !got.Equal(want) {
				t.Errorf("%s: %s stamped %s, want %s", archiveName("v1.2.3", tg), h.Name, got, want)
			}
		}
		f.Close()
	}
}

// SHA256SUMS must name every archive and nothing else. A checksum file that
// silently omits one file is worse than none: it looks like verification.
func TestChecksumsCoverEveryArchive(t *testing.T) {
	out := build(t)
	body, err := os.ReadFile(filepath.Join(out, "SHA256SUMS"))
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(strings.TrimSpace(string(body)), "\n")
	if len(lines) != len(targets) {
		t.Fatalf("SHA256SUMS has %d lines, want %d", len(lines), len(targets))
	}
	for _, tg := range targets {
		if !strings.Contains(string(body), archiveName("v1.2.3", tg)) {
			t.Errorf("SHA256SUMS does not mention %s", archiveName("v1.2.3", tg))
		}
	}
	for _, l := range lines {
		if len(l) < 66 || l[64:66] != "  " {
			t.Errorf("not a sha256sum line: %q", l)
		}
	}
}

// A missing binary must stop the release rather than produce an archive with a
// hole in it. Half a release that looks whole is the failure worth preventing.
func TestAMissingBinaryIsFatal(t *testing.T) {
	dist := fakeDist(t)
	if err := os.Remove(filepath.Join(dist, binaryName("yarg-sync", targets[0]))); err != nil {
		t.Fatal(err)
	}
	lic := filepath.Join(t.TempDir(), "LICENSE")
	if err := os.WriteFile(lic, []byte("licence"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := run(dist, t.TempDir(), "v1.2.3", lic); err == nil {
		t.Error("a missing binary produced a release instead of an error")
	}
}

// The README has to name the platform's own launcher. A Windows README telling
// somebody to run ./start-server.sh is the single most likely way for this to
// be wrong and still look finished.
func TestTheReadmeNamesTheRightLauncher(t *testing.T) {
	for _, tg := range targets {
		got := readme("v1.2.3", tg)
		if tg.Windows {
			if !strings.Contains(got, "start-server.cmd") || strings.Contains(got, "start-server.sh") {
				t.Errorf("windows README does not point at start-server.cmd")
			}
			if !strings.Contains(got, "yarg-sync.exe") {
				t.Error("windows README does not name yarg-sync.exe")
			}
		} else {
			if !strings.Contains(got, "start-server.sh") || strings.Contains(got, "start-server.cmd") {
				t.Errorf("%s/%s README does not point at start-server.sh", tg.GOOS, tg.GOARCH)
			}
		}
		if !strings.Contains(got, "v1.2.3") {
			t.Errorf("%s/%s README does not carry the version", tg.GOOS, tg.GOARCH)
		}
		// The no-authentication warning is not optional. Somebody will put this
		// on a public IP unless the download says not to.
		if !strings.Contains(got, "NO authentication") {
			t.Errorf("%s/%s README omits the exposure warning", tg.GOOS, tg.GOARCH)
		}
	}
}

// Command mkrelease turns a directory of cross-compiled binaries into the
// archives a person actually downloads.
//
// Until now a "release" was twelve loose binaries in a CI artifact that expired
// after a week. That is a build output, not a release: there is nothing to hand
// anyone, nothing that says how to run it, and in seven days there is nothing
// at all. What a Windows user needs is one file they can unzip and double-click.
//
// It is written in Go rather than as a shell script calling `zip` and `tar`
// because the runner is not guaranteed to have either, and "the packaging step
// failed because the host lacks a tool" is a failure worth designing out rather
// than discovering. archive/zip, archive/tar and compress/gzip are standard
// library, so this works anywhere the project already builds.
package main

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/config"
)

// target is one platform's archive.
type target struct {
	GOOS   string
	GOARCH string
	// Suffix distinguishes the armv7 build, whose binaries are named with a
	// "v7" that GOARCH alone does not carry.
	Suffix string
	// Windows gets a .zip and a .cmd launcher; everything else gets a .tar.gz
	// and a .sh.
	Windows bool
}

var targets = []target{
	{GOOS: "linux", GOARCH: "amd64"},
	{GOOS: "linux", GOARCH: "arm64"},
	{GOOS: "linux", GOARCH: "arm", Suffix: "v7"},
	{GOOS: "darwin", GOARCH: "amd64"},
	{GOOS: "darwin", GOARCH: "arm64"},
	{GOOS: "windows", GOARCH: "amd64", Windows: true},
}

// commands is what every archive carries. A release without the sync client is
// only half of Phase 2 - the server is not usable by an unmodified YARG on its
// own - so both binaries travel together rather than as separate downloads.
var commands = []string{"yarg-song-server", "yarg-sync"}

// epoch is the modification time stamped on every file in every archive.
//
// Not time.Now(). Two runs of this program over the same binaries must produce
// byte-identical archives, for the same reason PackDir must: an archive whose
// bytes depend on when it was built cannot be compared against a previous
// build, and this project has already paid once for a "deterministic" claim
// that was not - the .sng header mask, where two machines received 16 different
// archives out of 22. A fixed timestamp costs nothing and makes SHA256SUMS mean
// something.
var epoch = time.Date(2020, 1, 1, 0, 0, 0, 0, time.UTC)

func main() {
	dist := flag.String("dist", "dist", "directory holding the cross-compiled binaries")
	out := flag.String("out", "release", "directory to write the archives into")
	version := flag.String("version", "", "version string for the archive names (required)")
	license := flag.String("license", "LICENSE", "path to the licence file to include")
	flag.Parse()

	if *version == "" {
		fmt.Fprintln(os.Stderr, "mkrelease: -version is required")
		os.Exit(2)
	}
	if err := run(*dist, *out, *version, *license); err != nil {
		fmt.Fprintf(os.Stderr, "mkrelease: %v\n", err)
		os.Exit(1)
	}
}

func run(dist, out, version, licensePath string) error {
	licence, err := os.ReadFile(licensePath)
	if err != nil {
		return fmt.Errorf("licence: %w", err)
	}
	if err := os.MkdirAll(out, 0o755); err != nil {
		return err
	}

	var sums []string
	for _, t := range targets {
		files, err := collect(dist, version, t, licence)
		if err != nil {
			return err
		}

		name := archiveName(version, t)
		dest := filepath.Join(out, name)
		if t.Windows {
			err = writeZip(dest, files)
		} else {
			err = writeTarGz(dest, files)
		}
		if err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}

		sum, err := sha256File(dest)
		if err != nil {
			return err
		}
		sums = append(sums, fmt.Sprintf("%s  %s", sum, name))
		fmt.Printf("  %s\n", name)
	}

	sort.Strings(sums)
	body := strings.Join(sums, "\n") + "\n"
	if err := os.WriteFile(filepath.Join(out, "SHA256SUMS"), []byte(body), 0o644); err != nil {
		return err
	}
	fmt.Println("  SHA256SUMS")
	return nil
}

// entry is one file inside an archive.
type entry struct {
	// Name is the path inside the archive, always with forward slashes.
	Name string
	Body []byte
	// Exec marks a file that must arrive executable. A downloaded binary that
	// needs a chmod before it runs is a bad first five minutes.
	Exec bool
}

func collect(dist, version string, t target, licence []byte) ([]entry, error) {
	// Everything lives inside a single top-level directory named after the
	// archive, so unzipping in a Downloads folder does not scatter six files
	// across it.
	root := stem(version, t)

	var files []entry
	for _, cmd := range commands {
		src := filepath.Join(dist, binaryName(cmd, t))
		body, err := os.ReadFile(src)
		if err != nil {
			return nil, fmt.Errorf("%s: %w", src, err)
		}
		name := cmd
		if t.Windows {
			name += ".exe"
		}
		files = append(files, entry{Name: path.Join(root, name), Body: body, Exec: true})
	}

	// The config template is config.Example itself rather than a copy kept
	// beside it. A second copy of a settings file agrees with the real one for
	// a while and then quietly stops, and the person it misleads is the one who
	// trusted the file that shipped with the download.
	files = append(files,
		entry{Name: path.Join(root, "yarg-song-server.conf"), Body: []byte(config.Example)},
		entry{Name: path.Join(root, "README.txt"), Body: []byte(readme(version, t))},
		entry{Name: path.Join(root, "LICENSE"), Body: licence},
	)

	if t.Windows {
		files = append(files, entry{Name: path.Join(root, "start-server.cmd"), Body: []byte(launcherCmd)})
	} else {
		files = append(files, entry{Name: path.Join(root, "start-server.sh"), Body: []byte(launcherSh), Exec: true})
	}

	sort.Slice(files, func(i, j int) bool { return files[i].Name < files[j].Name })
	return files, nil
}

// binaryName is what the release CI job names the file in dist/.
func binaryName(cmd string, t target) string {
	name := fmt.Sprintf("%s-%s-%s%s", cmd, t.GOOS, t.GOARCH, t.Suffix)
	if t.Windows {
		name += ".exe"
	}
	return name
}

func stem(version string, t target) string {
	return fmt.Sprintf("yarg-song-server_%s_%s_%s%s", version, t.GOOS, t.GOARCH, t.Suffix)
}

func archiveName(version string, t target) string {
	if t.Windows {
		return stem(version, t) + ".zip"
	}
	return stem(version, t) + ".tar.gz"
}

func writeZip(dest string, files []entry) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	zw := zip.NewWriter(f)
	for _, e := range files {
		hdr := &zip.FileHeader{Name: e.Name, Method: zip.Deflate, Modified: epoch}
		mode := os.FileMode(0o644)
		if e.Exec {
			mode = 0o755
		}
		hdr.SetMode(mode)
		w, err := zw.CreateHeader(hdr)
		if err != nil {
			return err
		}
		if _, err := w.Write(e.Body); err != nil {
			return err
		}
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return f.Close()
}

func writeTarGz(dest string, files []entry) error {
	f, err := os.Create(dest)
	if err != nil {
		return err
	}
	defer f.Close()

	gz := gzip.NewWriter(f)
	tw := tar.NewWriter(gz)
	for _, e := range files {
		mode := int64(0o644)
		if e.Exec {
			mode = 0o755
		}
		if err := tw.WriteHeader(&tar.Header{
			Name:    e.Name,
			Mode:    mode,
			Size:    int64(len(e.Body)),
			ModTime: epoch,
			Format:  tar.FormatUSTAR,
		}); err != nil {
			return err
		}
		if _, err := tw.Write(e.Body); err != nil {
			return err
		}
	}
	if err := tw.Close(); err != nil {
		return err
	}
	if err := gz.Close(); err != nil {
		return err
	}
	return f.Close()
}

func sha256File(p string) (string, error) {
	f, err := os.Open(p)
	if err != nil {
		return "", err
	}
	defer f.Close()
	h := sha256.New()
	if _, err := io.Copy(h, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(h.Sum(nil)), nil
}

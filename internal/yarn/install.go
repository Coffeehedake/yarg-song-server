package yarn

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/bodgit/sevenzip"
)

// manifestName records what is installed in a pack directory and at which published version.
// Without it "is this pack installed" would be guesswork from folder contents, and "is it out
// of date" would be unanswerable.
const manifestName = ".yarn-pack.json"

// Manifest is what we wrote into a pack directory when we installed it.
type Manifest struct {
	UUID        string    `json:"uuid"`
	Name        string    `json:"name"`
	Tag         string    `json:"tag"`
	InstalledAt time.Time `json:"installed_at"`
	Songs       int       `json:"songs"`
	Bytes       int64     `json:"bytes"`
	// ArchiveSHA256 is OUR hash of what we downloaded, not the publisher's. YARN publishes no
	// checksum or signature for setlist archives - a profile carries a url and a fileType and
	// nothing else - so this detects a changed or truncated download between runs and cannot
	// vouch for authenticity. Which of the two it is matters, so it is said here.
	ArchiveSHA256 []string `json:"archive_sha256"`
}

// Result is what one install did.
type Result struct {
	Pack    string
	Tag     string
	Dir     string
	Songs   int
	Bytes   int64
	Skipped bool // already installed at this tag
}

// Installer downloads packs and lays them out in the library.
//
// Packs go in a directory each, named after the pack, because a library is read by people as
// well as by the scanner: a flat dump of several hundred .yargsong files cannot be inspected,
// pruned or uninstalled. YARG scans recursively, so the nesting costs nothing.
type Installer struct {
	Client     *Client
	HTTP       *http.Client
	LibraryDir string
	WorkDir    string
	Log        *slog.Logger
}

func (in *Installer) httpClient() *http.Client {
	if in.HTTP != nil {
		return in.HTTP
	}
	// Deliberately no overall timeout: a pack runs to 1.9 GB and a deadline that suits JSON
	// would kill it mid-download. Cancellation is the context's job.
	return &http.Client{}
}

func (in *Installer) log() *slog.Logger {
	if in.Log != nil {
		return in.Log
	}
	return slog.Default()
}

// safeName turns a pack name into a directory name that cannot escape and cannot collide with
// characters Windows refuses.
func safeName(s string) string {
	const reserved = `/\:*?"<>|`
	var b strings.Builder
	for _, r := range strings.TrimSpace(s) {
		if r < 32 || strings.ContainsRune(reserved, r) {
			b.WriteRune('_')
			continue
		}
		b.WriteRune(r)
	}
	out := strings.Trim(b.String(), " .")
	if out == "" {
		out = "pack"
	}
	return out
}

// ReadManifest returns the manifest in a pack directory, or nil when there is none.
func ReadManifest(dir string) (*Manifest, error) {
	b, err := os.ReadFile(filepath.Join(dir, manifestName))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var m Manifest
	if err := json.Unmarshal(b, &m); err != nil {
		return nil, fmt.Errorf("%s: %w", manifestName, err)
	}
	return &m, nil
}

// Install fetches one pack and extracts it into the library.
//
// Installing the same tag twice is a no-op that reports Skipped, so "install everything we are
// missing" is safe to repeat and safe to interrupt.
func (in *Installer) Install(ctx context.Context, entry Entry) (*Result, error) {
	if in.LibraryDir == "" {
		return nil, errors.New("yarn: LibraryDir is empty")
	}
	if !entry.IsSetlist() {
		return nil, fmt.Errorf("yarn: %q is a %s, not a setlist", entry.Name, entry.Type)
	}

	p, err := in.Client.Profile(ctx, entry.URL)
	if err != nil {
		return nil, fmt.Errorf("profile %s: %w", entry.Name, err)
	}

	dir := filepath.Join(in.LibraryDir, safeName(p.Name()))
	if m, err := ReadManifest(dir); err != nil {
		return nil, err
	} else if m != nil && m.Tag == p.Tag() {
		return &Result{Pack: p.Name(), Tag: p.Tag(), Dir: dir, Songs: m.Songs, Bytes: m.Bytes, Skipped: true}, nil
	}

	work := in.WorkDir
	if work == "" {
		work = os.TempDir()
	}
	if err := os.MkdirAll(work, 0o755); err != nil {
		return nil, err
	}
	stage, err := os.MkdirTemp(work, "yarn-install-")
	if err != nil {
		return nil, err
	}
	defer os.RemoveAll(stage)

	files := p.Files()
	if len(files) == 0 {
		return nil, fmt.Errorf("yarn: %q lists no files", p.Name())
	}

	// Extract into staging, never straight into the library. A half-extracted pack sitting in
	// the library is a set of songs the scanner indexes and a client cheerfully syncs.
	out := filepath.Join(stage, "out")
	if err := os.MkdirAll(out, 0o755); err != nil {
		return nil, err
	}

	var hashes []string
	var songs int
	var total int64

	for i, f := range files {
		archive := filepath.Join(stage, fmt.Sprintf("part-%d.7z", i))
		sum, err := in.download(ctx, f.URL, archive)
		if err != nil {
			return nil, fmt.Errorf("download %s: %w", f.URL, err)
		}
		hashes = append(hashes, sum)

		n, b, err := extractInto(archive, out, f.FileType)
		if err != nil {
			return nil, fmt.Errorf("extract %s: %w", path.Base(f.URL), err)
		}
		songs += n
		total += b

		// The archive is removed as soon as it is unpacked. Keeping all of them would mean
		// peak disk of the whole pack twice over, which for the official setlist is nearly
		// 4 GB of staging for a 1.9 GB download.
		if err := os.Remove(archive); err != nil {
			return nil, err
		}

		in.log().Info("yarn: extracted archive",
			"pack", p.Name(), "part", i+1, "parts", len(files), "entries", n)
	}

	m := Manifest{
		UUID: p.UUID, Name: p.Name(), Tag: p.Tag(),
		InstalledAt: time.Now().UTC(), Songs: songs, Bytes: total, ArchiveSHA256: hashes,
	}
	mb, err := json.MarshalIndent(m, "", "  ")
	if err != nil {
		return nil, err
	}
	if err := os.WriteFile(filepath.Join(out, manifestName), mb, 0o644); err != nil {
		return nil, err
	}

	// Swap into the library only once everything is in hand.
	if err := os.MkdirAll(filepath.Dir(dir), 0o755); err != nil {
		return nil, err
	}
	if err := os.RemoveAll(dir); err != nil {
		return nil, err
	}
	if err := os.Rename(out, dir); err != nil {
		// Rename across devices fails. WorkDir is configurable precisely so staging can live
		// on the library's disk, and the error says so rather than leaving someone guessing.
		return nil, fmt.Errorf("move %s into the library: %w (put WorkDir on the same disk as LibraryDir)", p.Name(), err)
	}

	return &Result{Pack: p.Name(), Tag: p.Tag(), Dir: dir, Songs: songs, Bytes: total}, nil
}

func (in *Installer) download(ctx context.Context, url, dest string) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := in.httpClient().Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("HTTP %d", resp.StatusCode)
	}

	f, err := os.Create(dest)
	if err != nil {
		return "", err
	}
	h := newHasher()
	if _, err := io.Copy(io.MultiWriter(f, h), resp.Body); err != nil {
		f.Close()
		return "", err
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return h.hex(), nil
}

// safeTarget resolves an archive entry name under root and refuses anything that leaves it.
//
// Separated from the extraction loop so it can be tested against hostile names directly: a
// guard that can only be exercised by building a malicious archive is a guard nobody tests.
// The entry name is treated as a SLASH path, because that is what the archive format stores,
// but a backslash is also folded on Windows - an entry named `..\evil` is traversal there and
// an ordinary filename elsewhere, and filepath.FromSlash alone would not catch it.
func safeTarget(root, name string) (string, error) {
	cleaned := filepath.FromSlash(name)
	if os.PathSeparator == '\\' {
		cleaned = strings.ReplaceAll(cleaned, "/", string(os.PathSeparator))
	}

	target, err := filepath.Abs(filepath.Join(root, cleaned))
	if err != nil {
		return "", err
	}
	if target != root && !strings.HasPrefix(target, root+string(os.PathSeparator)) {
		return "", fmt.Errorf("entry escapes the destination: %q", name)
	}
	return target, nil
}

// extractInto unpacks one archive, refusing any entry that would escape the destination.
//
// The guard is not theoretical. This unpacks an archive fetched over the network into a
// directory a file server then exposes, and the Go side of this project already carries
// hostile-archive tests for exactly this shape of input.
func extractInto(archive, dest, fileType string) (int, int64, error) {
	var (
		r   *sevenzip.ReadCloser
		err error
	)
	if fileType == "encrypted" {
		r, err = sevenzip.OpenReaderWithPassword(archive, archivePassword())
	} else {
		r, err = sevenzip.OpenReader(archive)
	}
	if err != nil {
		return 0, 0, err
	}
	defer r.Close()

	root, err := filepath.Abs(dest)
	if err != nil {
		return 0, 0, err
	}

	var count int
	var total int64
	for _, e := range r.File {
		if e.FileInfo().IsDir() {
			continue
		}

		target, err := safeTarget(root, e.Name)
		if err != nil {
			return 0, 0, err
		}

		if err := os.MkdirAll(filepath.Dir(target), 0o755); err != nil {
			return 0, 0, err
		}

		rc, err := e.Open()
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", e.Name, err)
		}
		f, err := os.Create(target)
		if err != nil {
			rc.Close()
			return 0, 0, err
		}
		n, err := io.Copy(f, rc)
		rc.Close()
		cerr := f.Close()
		if err != nil {
			return 0, 0, fmt.Errorf("%s: %w", e.Name, err)
		}
		if cerr != nil {
			return 0, 0, cerr
		}
		count++
		total += n
	}
	return count, total, nil
}

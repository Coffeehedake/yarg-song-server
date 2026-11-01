package scan

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
	"github.com/coffeehedake/yarg-song-server/internal/yargsong"
)

// A song reaches this server in one of five shapes: a loose folder, a .sng, a
// .zip, a .7z or a .rar. Four of those are just a filesystem with a chart in it,
// and this file is what makes them interchangeable.
//
// The archive readers no longer satisfy fs.FS directly - archiveItems and
// archiveFS sit in between, because the stored ORDER of entries turned out to
// matter (a solid .7z read out of order costs a decompression pass per entry)
// and because entry names are not always valid UTF-8. rardecode joined on the
// same terms: List plus a per-entry Open, with no adapter of its own.
// The .sng path stays separate for a real reason rather than an accidental one:
// a .sng carries its metadata in the archive HEADER rather than in a contained
// song.ini, so it is scanned by ScanArchive, not by ScanDir.

// maxNesting bounds how far into an archive we will look for the song folder.
// One level is the common case (Song.zip containing Song/); more than a couple
// is somebody's backup tree, not a song, and an unbounded descent on a hostile
// archive is a denial of service.
const maxNesting = 6

// ErrRockBandPackage means the file is a Rock Band console package. It is
// recognised deliberately so the operator gets a reason rather than silence:
// a file that is simply ignored looks like a broken server.
var ErrRockBandPackage = errors.New("scan: Rock Band console package; its audio is encrypted and decrypting it is a permanent non-goal of this project, so the file is skipped rather than half-read")

// ErrUnopenable7z means the file really is a 7z - it carries the signature - and this server
// could not open it. Password protection is the overwhelmingly likely cause now that YARN
// publishes every setlist as an encrypted .7z, and the message leads with that because the
// person reading it has usually just copied one into the library by hand.
var ErrUnopenable7z = errors.New("scan: this is a 7z archive but it could not be opened; it is probably password-protected, which is how YARN publishes setlists - install those with yarn-install rather than copying the archive in, since the server does not unpack archives it cannot read")

// ErrUnopenableRar means the file really is a RAR - it carries the signature - and this server
// could not open it.
//
// Separate from ErrUnopenable7z because the likely causes differ and so does the advice. A
// password is one of them, but the commoner one here is a MULTI-VOLUME set whose other parts
// are missing: rardecode follows volumes by name, so a lone "X.part3.rar" cannot be read no
// matter what is in it. Encryption is a permanent refusal; a missing volume is a file the
// operator can go and fetch, and those should not read the same.
var ErrUnopenableRar = errors.New("scan: this is a RAR archive but it could not be opened; if it is one part of a multi-volume set the other parts must sit beside it (rardecode follows them by name), and if it is password-protected the server cannot read it at all")

// ErrTooManySongs means an archive holds several song folders. The roadmap
// promises "a .zip of a loose folder", singular, and guessing which one the
// operator meant would silently publish one song and drop the rest.
//
// THE LIBRARY WALK NO LONGER RAISES THIS. WalkLibrary uses ScanContainerAll and publishes
// every song in an archive, which is what the advice to "unpack it" was a workaround for.
// What still raises it is the single-verdict path - ScanFile, and therefore the upload check -
// because that endpoint answers about one file with one song and has nowhere to put twenty.
//
// So the two deliberately disagree now, and ScanFile's doc comment promises they do not. That
// promise is the older of the two and the divergence is real: uploading a multi-song zip is
// refused while copying the same file into the library succeeds. Worth closing by teaching the
// check endpoint to report several verdicts; not worth silently picking one song to keep the
// comment true.
var ErrTooManySongs = errors.New("scan: archive holds more than one song folder; unpack it and add the songs individually")

// ErrUnreadableArchive means an archive plainly contains a song - there is a
// song.ini or a chart file somewhere inside it - but no song folder could be
// resolved from it.
//
// This exists because the alternative is silence. A library legitimately holds
// archives that are not songs, so "no chart here" has to be ignored quietly or
// every holiday-photos zip becomes a problem entry. But an archive that clearly
// DOES hold a song and still cannot be read is a different thing, and swallowing
// it is the same failure as silently ignoring a Rock Band package: the operator
// sees nothing appear and concludes the server is broken.
//
// The known cause is an archive whose entries use BACKSLASH separators, which
// some Windows tools still write. Measured 2026-09-06: Go's zip filesystem
// surfaces a directory for such an entry but no readable file underneath it, so
// the chart is never found. Re-zipping with any modern tool fixes it.
var ErrUnreadableArchive = errors.New("scan: archive contains a song.ini or a chart but no song folder could be read from it; if it was made by an old Windows tool it may use backslash path separators - re-zipping it will fix that")

// rbPackageSuffixes are the console-package shapes seen in the wild. `_rb3con`
// is a SUFFIX, not an extension - those files usually have no extension at all,
// so matching on filepath.Ext alone would miss the most common one.
var rbPackageSuffixes = []string{".con", "_rb3con", ".pkg", ".xex"}

// IsRockBandPackage reports whether a filename is a Rock Band console package.
func IsRockBandPackage(name string) bool {
	l := strings.ToLower(name)
	for _, s := range rbPackageSuffixes {
		if strings.HasSuffix(l, s) {
			return true
		}
	}
	return false
}

// IsContainer reports whether a filename is an archive this server can read as
// a song folder. .sng is deliberately excluded: it is a song, not a container
// holding one, and it is served directly rather than repacked.
//
// A SECONDARY volume of a multi-volume RAR set is excluded too, and that is not tidiness. Given
// "X.part1.rar" rardecode reads the whole set; given "X.part2.rar" it either fails - one noisy
// error per part - or finds part 1 beside it and succeeds, which is worse, because then every
// song in the set is indexed once per part. The chart hash would collapse the duplicates, so
// the symptom would be a library that quietly reports the wrong archive as a song's source.
func IsContainer(name string) bool {
	switch strings.ToLower(filepath.Ext(name)) {
	case ".zip", ".7z":
		return true
	case ".rar":
		return !IsSecondaryRarVolume(name)
	}
	return false
}

// rarPartSuffix matches the ".partN.rar" naming RAR 3.0+ uses for a split set.
//
// The older convention - X.rar, X.r00, X.r01 - needs nothing here: those continuation parts do
// not end in .rar, so IsContainer never offered them in the first place.
var rarPartSuffix = regexp.MustCompile(`(?i)\.part(\d+)\.rar$`)

// IsSecondaryRarVolume reports whether a filename is a continuation part of a multi-volume RAR
// set rather than the part that opens it.
//
// Only the first part is an entry point. Which number that is depends on the writer's padding -
// part1, part01 and part001 all occur - so the digits are compared as a NUMBER, not as text.
func IsSecondaryRarVolume(name string) bool {
	m := rarPartSuffix.FindStringSubmatch(filepath.Base(name))
	if m == nil {
		return false
	}
	n, err := strconv.Atoi(m[1])
	if err != nil {
		return false // unparseable: treat it as an entry point and let the reader object
	}
	return n > 1
}

// rarSignature is the prefix every RAR file starts with. RAR 1.5-4.x follows it with 0x00 and
// RAR 5.0 with 0x01 0x00; the shared six bytes are enough to tell a real archive from a file
// that merely ends in .rar.
var rarSignature = []byte{0x52, 0x61, 0x72, 0x21, 0x1A, 0x07}

// looksRar reports whether the file carries the RAR signature, separating "a real archive we
// cannot read" from "a file that merely ends in .rar". Only the first earns an operator a
// confusing message; the second is obviously junk. Same split as looks7z.
func looksRar(p string) bool { return hasPrefixBytes(p, rarSignature) }

// IsYargsong reports whether a filename is the container YARG setlists ship in.
//
// Kept apart from IsContainer because a .yargsong is not a container holding a song folder -
// it is a song, the way a .sng is. The distinction matters: a container gets searched for a
// song root, and a .yargsong would never produce one.
func IsYargsong(name string) bool {
	return strings.EqualFold(filepath.Ext(name), ".yargsong")
}

// OpenContainer opens a .zip or .7z and returns a filesystem rooted at the song
// inside it, together with the closer the caller must call.
//
// The returned FS is rooted at the song rather than at the archive, so every
// caller downstream - the scanner, the packer, the package hash - sees exactly
// what it would see for a loose folder. That is what makes a zipped song and an
// unzipped one produce the same bytes.
// sevenZSignature is the 6 bytes every 7z file starts with.
var sevenZSignature = []byte{0x37, 0x7A, 0xBC, 0xAF, 0x27, 0x1C}

// looks7z reports whether the file carries the 7z signature, which separates "a real archive
// we cannot read" from "a file that merely ends in .7z". Only the first costs an operator a
// confusing message; the second is already obviously junk.
func looks7z(p string) bool { return hasPrefixBytes(p, sevenZSignature) }

// hasPrefixBytes reports whether a file begins with the given signature.
//
// A file shorter than the signature, or one that cannot be opened at all, is not a match: the
// callers are asking "is this really the format it claims to be", and both answers there are no.
func hasPrefixBytes(p string, sig []byte) bool {
	f, err := os.Open(p)
	if err != nil {
		return false
	}
	defer f.Close()

	head := make([]byte, len(sig))
	if _, err := io.ReadFull(f, head); err != nil {
		return false
	}
	return bytes.Equal(head, sig)
}

func OpenContainer(p string) (fs.FS, io.Closer, error) {
	fsys, raw, closer, err := openArchive(p)
	if err != nil {
		return nil, nil, err
	}

	root, err := songRoot(fsys)
	if err != nil {
		// Before reporting "no chart", ask whether the archive plainly contains a song
		// anyway. If it does, this is an archive we failed to READ, not an archive of
		// something else, and the two deserve different answers.
		if errors.Is(err, ErrNoChart) && looksLikeASong(raw) {
			err = ErrUnreadableArchive
		}
		closer.Close()
		return nil, nil, err
	}
	return root, closer, nil
}

// openArchive opens a .zip or .7z as a filesystem, without deciding what is a song in it.
//
// It also returns the entry names, before any fs view, because the fs view is blind to some of
// them - an archive written with backslash separators shows a directory with nothing readable
// underneath, and a check that walks the fs inherits precisely the blindness it exists to detect.
//
// Those names are decoded (see decodeEntryName) rather than returned exactly as stored, and they
// have to be, because they are what songRoots turns into the inner paths that ScanContainerAll
// and PackPathInner then address through this same fs. Stored names on one side and decoded
// names on the other would simply fail to find each other.
//
// Decoding does not weaken the blindness check: it only alters names that are not valid UTF-8,
// and a backslash-separated name is plain ASCII, so it arrives here exactly as stored.
func openArchive(p string) (fs.FS, []string, io.Closer, error) {
	items, closer, err := archiveItems(p)
	if err != nil {
		return nil, nil, nil, err
	}

	raw := make([]string, 0, len(items))
	for _, it := range items {
		raw = append(raw, decodeEntryName(it.stored))
	}
	return newArchiveFS(items), raw, closer, nil
}

// looksLikeASong reports whether any entry name in the archive is a song.ini or
// a chart file.
//
// It takes the RAW stored names rather than an fs.FS on purpose, and the first
// attempt at this got it wrong: walking the filesystem view inherits exactly the
// blindness this check exists to detect. The backslash case is the proof - the
// fs view shows a directory with nothing readable under it, so an fs.WalkDir
// finds no song.ini and concludes, wrongly, that the silence was justified.
//
// Both separators are handled because the whole point is names the fs view will
// not parse.
func looksLikeASong(rawNames []string) bool {
	for _, n := range rawNames {
		base := strings.ToLower(n)
		if i := strings.LastIndexAny(base, `/\`); i >= 0 {
			base = base[i+1:]
		}
		switch base {
		case "song.ini", "notes.mid", "notes.midi", "notes.chart", "notes.txt":
			return true
		}
	}
	return false
}

// songRoot finds the directory holding the chart, descending through the
// wrapper folders archives usually carry.
//
// Three shapes, in order of how often they turn up:
//   - chart at the archive root
//   - one folder at the root, chart inside it        (the common case)
//   - several folders, exactly one of which is a song
//
// Several song folders is refused rather than resolved. See ErrTooManySongs.
func songRoot(fsys fs.FS) (fs.FS, error) {
	cur := fsys
	for depth := 0; depth < maxNesting; depth++ {
		names, err := rootNames(cur)
		if err != nil {
			return nil, err
		}
		if _, ok := PickChartFile(names); ok {
			return cur, nil
		}

		dirs, err := rootDirs(cur)
		if err != nil {
			return nil, err
		}
		if len(dirs) == 0 {
			return nil, ErrNoChart
		}

		// Which of these directories is itself a song?
		var songs []string
		for _, d := range dirs {
			sub, err := fs.Sub(cur, d)
			if err != nil {
				continue
			}
			subNames, err := rootNames(sub)
			if err != nil {
				continue
			}
			if _, ok := PickChartFile(subNames); ok {
				songs = append(songs, d)
			}
		}
		switch {
		case len(songs) > 1:
			return nil, ErrTooManySongs
		case len(songs) == 1:
			return fs.Sub(cur, songs[0])
		case len(dirs) == 1:
			// No chart yet, but only one way down - keep descending.
			next, err := fs.Sub(cur, dirs[0])
			if err != nil {
				return nil, err
			}
			cur = next
		default:
			return nil, ErrNoChart
		}
	}
	return nil, ErrNoChart
}

// rootDirs lists the directories at the top of fsys.
func rootDirs(fsys fs.FS) ([]string, error) {
	entries, err := fs.ReadDir(fsys, ".")
	if err != nil {
		return nil, fmt.Errorf("scan: read archive root: %w", err)
	}
	var out []string
	for _, e := range entries {
		if e.IsDir() {
			out = append(out, e.Name())
		}
	}
	return out, nil
}

// ScanContainer scans a .zip or .7z holding one loose song folder.
//
// It is ScanDir with a different way of getting to the filesystem, which is the
// whole point: a song does not become a different song because somebody zipped
// it, and no scanning logic is duplicated to prove that.
func ScanContainer(p string) (*catalog.Song, error) {
	fsys, closer, err := OpenContainer(p)
	if err != nil {
		return nil, err
	}
	defer closer.Close()
	return ScanDir(fsys)
}

// PackPath packs any supported source - loose folder, .zip or .7z - to a .sng.
//
// A .sng is not accepted here: it is already the wire format and is served
// straight off disk rather than repacked.
func PackPath(p string, w io.Writer) error { return PackPathInner(p, "", w) }

// PackPathInner packs one song out of a source that may hold several.
//
// inner is the song's path within an archive, as reported by the walk; "" means the source is
// a single song. Threading it through is what makes a multi-song archive servable rather than
// merely listable - without it the packer would find the first song in the file and hand it
// back for every hash in that archive, which is worse than refusing the archive outright.
func PackPathInner(p, inner string, w io.Writer) error {
	st, err := os.Stat(p)
	if err != nil {
		return err
	}
	if st.IsDir() {
		return PackFS(os.DirFS(p), w)
	}
	if IsYargsong(p) {
		a, r, err := yargsong.OpenArchive(p)
		if err != nil {
			return err
		}
		defer r.Close()
		// Repacked rather than streamed, even though the decoded body IS an SNG. Streaming
		// it would mean serving a file whose first six bytes are a decoy instead of SNGPKG,
		// and a client reading our wire format is entitled to the real magic. Packing also
		// keeps one deterministic producer rather than two ways of making a .sng.
		//
		// WithMetadata, because an SNG keeps its song.ini namespace in the header: pack it
		// as a plain filesystem and the result is a valid, completely anonymous song.
		meta := make([]sng.Pair, 0, len(a.MetaOrder))
		for _, k := range a.MetaOrder {
			meta = append(meta, sng.Pair{Key: k, Value: a.Metadata[k]})
		}
		return PackFSWithMetadata(a, meta, w)
	}
	// With an inner path the archive is opened at its ROOT and narrowed here, because
	// OpenContainer narrows to the one song it found - which is the wrong song, or an error,
	// as soon as the archive holds several.
	if inner != "" {
		fsys, _, closer, err := openArchive(p)
		if err != nil {
			return err
		}
		defer closer.Close()
		sub, err := fs.Sub(fsys, inner)
		if err != nil {
			return fmt.Errorf("pack: %s in %s: %w", inner, filepath.Base(p), err)
		}
		return PackFS(sub, w)
	}

	fsys, closer, err := OpenContainer(p)
	if err != nil {
		return err
	}
	defer closer.Close()
	return PackFS(fsys, w)
}

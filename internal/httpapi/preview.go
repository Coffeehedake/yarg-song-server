package httpapi

import (
	"bytes"
	"errors"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/coffeehedake/yarg-song-server/internal/catalog"
	"github.com/coffeehedake/yarg-song-server/internal/library"
	"github.com/coffeehedake/yarg-song-server/internal/preview"
	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// defaultPreviewMS is used when song.ini gives no usable preview window. YARG's own preview is
// about this long, and a clip much longer stops being cheap - which is the entire point of the
// endpoint.
const defaultPreviewMS int64 = 30000

// maxPreviewMS caps a song.ini that asks for something absurd. A preview_end_time of 9999999 is
// not rare in the wild, and serving ten minutes of audio as a "preview" would defeat the feature
// while looking like it worked.
const maxPreviewMS int64 = 45000

// stemPreference is the order a preview falls back through when a song has no full mix.
//
// 3,861 of the live library's 4,085 songs carry "song", so the first entry answers 94.5% of
// requests. The remaining ~224 are the reason the rest of the list exists: without it they would
// get whichever stem the archive happened to list first, which for a drum chart is a preview of
// nothing but drums.
//
// Mixing several stems would be a better preview and is deliberately not done - it needs a
// decoder, which is what ADR-009 exists to avoid.
var stemPreference = []string{
	"song", "guitar", "rhythm", "keys", "bass", "vocals", "vocals_1", "drums", "drums_1", "crowd",
}

// resolveChart finds the one library entry a chart hash names, answering the request itself when
// it cannot.
//
// Extracted from songFile so the preview endpoint cannot drift from it. The ambiguous case is
// the reason it is worth sharing: when two packages hold the same chart hash, answering 300 with
// the choices is a deliberate decision recorded in songFile, and a second handler quietly
// picking the first entry instead would be a different contract for the same library.
func (s *Server) resolveChart(w http.ResponseWriter, r *http.Request, ix *library.Index,
	hash string) (*library.Entry, bool) {

	if pkg := r.URL.Query().Get("package"); pkg != "" {
		entry := ix.ByPackageHash(pkg)
		if entry == nil || entry.Song.ChartHash != hash {
			writeError(w, http.StatusNotFound, "no package with that package hash for this chart")
			return nil, false
		}
		return entry, true
	}

	entries := ix.ByChartHash(hash)
	switch len(entries) {
	case 0:
		writeError(w, http.StatusNotFound, "no song with that chart hash")
		return nil, false
	case 1:
		return entries[0], true
	default:
		choices := make([]map[string]string, 0, len(entries))
		for _, e := range entries {
			choices = append(choices, map[string]string{
				"package_hash": e.Song.PackageHash,
				"name":         e.Song.Name,
				"artist":       e.Song.Artist,
				"charter":      e.Song.Charter,
			})
		}
		writeJSON(w, http.StatusMultipleChoices, map[string]any{
			"error":      "this chart hash is shared by several packages; pass ?package=<package_hash>",
			"chart_hash": hash,
			"packages":   choices,
		})
		return nil, false
	}
}

// openPackage returns the song's .sng bytes, however the library stores them.
//
// A packed song is opened BY the cache rather than opened from a path the cache handed back -
// eviction between the two is a real race that once produced a 404 for a present song, and the
// reasoning is in packcache.Open.
func (s *Server) openPackage(entry *library.Entry) (*os.File, error) {
	if entry.Kind.NeedsPacking() {
		return s.Packs.Open(entry.Song.PackageHash, entry.Path, entry.Inner)
	}
	return os.Open(entry.Path)
}

// previewSource is one candidate the preview could be cut from.
type previewSource struct {
	Name      string
	ReadyMade bool
}

// previewCandidates lists what the preview could be cut from, best first.
//
// A LIST rather than one pick, because the best stem is not always a usable one. A test caught
// this: a song whose full mix could not be cut returned 500 while a perfectly good guitar stem
// sat beside it in the same package. In the wild that is a corrupt or oddly encoded song.ogg,
// and a preview of the guitar is plainly better than an error for a song that has audio.
//
// Falling through is not masking a fault - each skip is logged with its reason, and when every
// candidate fails the request still fails.
func previewCandidates(song *catalog.Song) []previewSource {
	var out []previewSource

	// A package that ships its own preview needs no cutting at all, and serving it is both
	// cheaper and more faithful than anything this code could cut.
	//
	// 215 of the live library's 4,085 songs do ship one - 150 mp3, 63 ogg, 2 opus - and that
	// number matters more than it looks, because this branch is FIRST and `ReadyMade` skips the
	// extension filter below. All 150 of the songs whose stems are mp3-only are in here, so they
	// were already being served before CutMP3 existed. This comment used to say "0 of 4,085",
	// which was true when written and was then read as a current measurement; the false claim
	// that those 150 songs answered 415 and played nothing came straight out of it. See the
	// correction in docs/ADR-009-preview-clips.md.
	if p := song.Assets.PreviewAudio; p != nil && p.Name != "" {
		out = append(out, previewSource{Name: p.Name, ReadyMade: true})
	}

	byStem := make(map[string]catalog.Stem, len(song.Assets.Stems))
	for _, st := range song.Assets.Stems {
		// Keep the first of each stem; variants ("clean", "explicit") are not chosen here
		// because censoring is the client's decision and it has no way to tell this endpoint.
		if _, seen := byStem[st.Stem]; !seen {
			byStem[st.Stem] = st
		}
	}

	seen := map[string]bool{}
	for _, want := range stemPreference {
		if st, found := byStem[want]; found && st.Name != "" && !seen[st.Name] {
			seen[st.Name] = true
			out = append(out, previewSource{Name: st.Name})
		}
	}

	// Anything recognised, rather than nothing. A song whose only audio is "drums_3" still
	// deserves a preview.
	for _, st := range song.Assets.Stems {
		if st.Name != "" && !seen[st.Name] {
			seen[st.Name] = true
			out = append(out, previewSource{Name: st.Name})
		}
	}
	return out
}

// findInArchive locates a file inside the package by base name, case-insensitively.
//
// The catalogue records a stem's base name, not its path inside the archive, and a package built
// from a loose folder can carry it in a subdirectory. Walking is cheap and guessing is not: a
// miss here would present as "this song has no audio".
func findInArchive(a *sng.Archive, base string) (string, bool) {
	want := strings.ToLower(base)
	found := ""
	_ = fs.WalkDir(a, ".", func(p string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || found != "" {
			return nil
		}
		if strings.ToLower(path.Base(p)) == want {
			found = p
		}
		return nil
	})
	return found, found != ""
}

// cuttableExt reports whether a stem can be cut without decoding it.
//
// Ogg and Opus are pages, MP3 is frames; all three are containers that can be sliced on their own
// boundaries. There are no .wav stems in the live library at all - 0 of 22,091 - so a WAV cutter
// would be code nobody runs, and it is left unwritten on purpose rather than forgotten.
func cuttableExt(ext string) bool {
	return ext == ".ogg" || ext == ".opus" || ext == ".mp3"
}

// defaultStartMS and defaultEndMS are where YARG previews a song that does not say.
//
// They are YARG.Core's DEFAULT_START_TIME and DEFAULT_END_TIME (PreviewContext.cs), in
// milliseconds. Twenty seconds in, not zero: the start of a track is an intro, a count-in or
// silence far more often than it is the part worth hearing.
const (
	defaultStartMS int64 = 20000
	defaultEndMS   int64 = 50000
)

// previewWindow is the clip's start and length in milliseconds.
//
// This REPRODUCES YARG.Core's PreviewContext rule and must keep doing so. The game is the
// specification for a question like "where does a preview start", and this server answering it
// differently is how 800 of 4,085 songs came to preview nothing at all.
//
// A negative preview_start_time means "not set" - it is the sentinel GH-derived charts use, and
// 800 songs in the live library carry it. The previous version clamped that -1 to 0 and then
// computed the duration as `end - start`, which for the overwhelmingly common pairing of
// start=-1 and an absent end is `0 - (-1)` = ONE MILLISECOND. The endpoint answered 200 with a
// valid Ogg stream containing a single page, so nothing anywhere reported a fault; it simply
// played silence. Measured on Megadeth - Peace Sells (gh5dlc) on 2026-11-05: 4,407 bytes against
// 793,264 for the copy of the same song whose song.ini gives a real start.
//
// The one deliberate difference from YARG: maxPreviewMS still caps the result. YARG clamps a
// preview_end_time of 9999999 to the length of the audio and will happily preview a whole song;
// this server serves clips over a network, and that is the cap's whole reason for existing.
func previewWindow(song *catalog.Song) (startMS, durMS int64) {
	lengthMS := song.SongLengthMS
	start, end := song.PreviewStartMS, song.PreviewEndMS

	// known reports whether the song's length is usable. YARG always has one - it measures the
	// mixer it just built - but this server works from song.ini, which may not say.
	known := lengthMS > 0

	var endMS int64
	switch {
	// Neither end of the window is usable: YARG's own default window.
	case (start < 0 || (known && start >= lengthMS)) && (end <= 0 || (known && end > lengthMS)):
		switch {
		case !known || defaultEndMS <= lengthMS:
			startMS, endMS = defaultStartMS, defaultEndMS
		case defaultPreviewMS <= lengthMS:
			startMS = (lengthMS - defaultPreviewMS) / 2
			endMS = startMS + defaultPreviewMS
		default:
			startMS, endMS = 0, lengthMS
		}

	// A usable start: honour it, and repair only the end.
	case start >= 0 && (!known || start < lengthMS):
		startMS, endMS = start, end
		if endMS <= startMS {
			endMS = startMS + defaultPreviewMS
		}
		if known && endMS > lengthMS {
			endMS = lengthMS
		}

	// Only the end is usable: work backwards from it.
	default:
		endMS = end
		startMS = endMS - defaultPreviewMS
		if startMS < 0 {
			startMS = 0
		}
	}

	durMS = endMS - startMS
	if durMS <= 0 {
		durMS = defaultPreviewMS
	}
	if durMS > maxPreviewMS {
		durMS = maxPreviewMS
	}
	return startMS, durMS
}

// previewFile serves a short audio clip cut from the song's own stem.
//
// GET /song/{chart_hash}/preview.ogg
//
// See docs/ADR-009-preview-clips.md. Nothing here decodes audio: the clip is whole Ogg pages
// copied out of the stem, which is why the server needs no ffmpeg and the image stays
// distroless-static.
//
// NOT YET CACHED ON DISK. ADR-009 says a clip should be cut once and kept beside the packed
// archives; this cuts on every request that the ETag does not satisfy. That is a performance gap
// rather than a correctness one - the output is deterministic, so the strong ETag below does the
// client-side half of the job - and it is recorded here rather than left for someone to discover
// from a slow endpoint.
func (s *Server) previewFile(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("hash"))

	ix, ok := s.index(w)
	if !ok {
		return
	}
	entry, ok := s.resolveChart(w, r, ix, hash)
	if !ok {
		return
	}

	candidates := previewCandidates(entry.Song)
	if len(candidates) == 0 {
		writeError(w, http.StatusNotFound, "this song has no audio to preview")
		return
	}

	startMS, durMS := previewWindow(entry.Song)

	// A strong ETag over what determines the bytes: the package's own content hash and the
	// window cut from it. The PACKAGE hash rather than one stem's, because which candidate wins
	// is decided below and the tag has to be known before any work is done - and any change to
	// any stem changes the package hash anyway. Deterministic output is what makes this sound,
	// the same property the packer's fixed mask protects.
	tag := `"` + entry.Song.PackageHash + "-preview-" + strconv.FormatInt(startMS, 10) + "-" +
		strconv.FormatInt(durMS, 10) + `"`
	w.Header().Set("ETag", tag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	f, err := s.openPackage(entry)
	if err != nil {
		// The two failures have different causes and must not share a status. Packing failing
		// is the server's problem; a library file having moved is the operator's, and the fix
		// for it is a rescan - which they only learn from the 404.
		s.logError("open song for preview", err, "chart_hash", hash)
		if entry.Kind.NeedsPacking() {
			writeError(w, http.StatusInternalServerError, "could not pack this song")
		} else {
			writeError(w, http.StatusNotFound,
				"this song is no longer where the index says it is; rescan")
		}
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		s.logError("stat song for preview", err, "chart_hash", hash)
		writeError(w, http.StatusInternalServerError, "could not read this song")
		return
	}

	a, err := sng.Open(f, st.Size())
	if err != nil {
		s.logError("open sng for preview", err, "chart_hash", hash)
		writeError(w, http.StatusInternalServerError, "could not read this package")
		return
	}

	var (
		clip     bytes.Buffer
		cutFrom  string
		cutExt   string
		lastErr  error
		cuttable int
	)
	for _, cand := range candidates {
		ext := strings.ToLower(path.Ext(cand.Name))
		if !cand.ReadyMade && !cuttableExt(ext) {
			continue
		}
		cuttable++

		inner, found := findInArchive(a, path.Base(cand.Name))
		if !found {
			lastErr = errors.New("the catalogue lists " + cand.Name + " and the package does not hold it")
			s.logError("stem missing from package", lastErr, "chart_hash", hash, "stem", cand.Name)
			continue
		}

		h, err := a.Open(inner)
		if err != nil {
			lastErr = err
			s.logError("open stem", err, "chart_hash", hash, "stem", inner)
			continue
		}

		clip.Reset()
		if cand.ReadyMade {
			_, err = io.Copy(&clip, io.LimitReader(h, 8<<20))
		} else {
			if ext == ".mp3" {
				err = preview.CutMP3(h, startMS, durMS, &clip)
			} else {
				err = preview.CutOgg(h, startMS, durMS, &clip)
			}
			if err != nil && preview.IsShortStream(err) {
				// A clip shorter than asked for is still a clip. Logged, because a library
				// full of these means the preview windows are wrong, not the cutter.
				s.logError("short preview", err, "chart_hash", hash, "stem", inner)
				err = nil
			}
		}
		h.Close()

		if err != nil || clip.Len() == 0 {
			if err == nil {
				err = errors.New("the clip came out empty")
			}
			lastErr = err
			// Not fatal: the next candidate may be fine. This is the case the test found -
			// an uncuttable song.ogg beside a usable guitar.ogg.
			s.logError("preview candidate unusable", err, "chart_hash", hash, "stem", inner)
			clip.Reset()
			continue
		}

		cutFrom = inner
		cutExt = ext
		break
	}

	if cutFrom == "" {
		if cuttable == 0 {
			// 415 and not 500: the request was understood and cannot be satisfied yet. MP3
			// frames and WAV offsets are cuttable by the same reasoning and are not written
			// yet - 449 of the library's 22,091 stem files, a 2% hole that says so.
			writeError(w, http.StatusUnsupportedMediaType,
				"this song's audio is "+strings.ToLower(path.Ext(candidates[0].Name))+
					"; Ogg, Opus and MP3 previews are cut, nothing else is")
			return
		}
		s.logError("no usable preview source", lastErr, "chart_hash", hash)
		writeError(w, http.StatusInternalServerError, "could not cut a preview from this song")
		return
	}

	// The type follows the BYTES, not the URL. A clip cut from an mp3 stem is mpeg however the
	// caller spelled the path, and a client told "audio/ogg" about mpeg data is being lied to
	// even if its decoder sniffs the content and copes.
	contentType := "audio/ogg"
	if cutExt == ".mp3" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, hash+"-preview"+cutExt, time.Time{}, bytes.NewReader(clip.Bytes()))
}

// previewStem serves ONE named stem's clip, so the client can mix several of them.
//
// GET /song/{chart_hash}/preview/{stem}
//
// **Why this exists at all is the important part.** `previewFile` has to choose a stem, and
// `stemPreference` is this server's own invention: upstream's `LoadPreviewAudio` prefers a
// shipped preview file and otherwise calls `CreateAudioMixer`, which adds EVERY stem to one
// mixer. The game never picks a stem. A server that picks one is guessing at a question the
// client already answers, and ADR-009 records how badly it guessed - 908 of 3,860 songs got a
// near-empty Rock Band backing stem, and the decoder-free audibility test written to fix that
// could not distinguish sparse audio from silence.
//
// So the server stops choosing. It cuts whichever stem it is asked for, and the client builds
// the same multi-channel mixer it builds for a local song.
//
// Note there is no extension in the path. `preview.ogg` already lies when the bytes are mpeg -
// that was a real defect, fixed by making the type follow the bytes - so this does not invite
// the same mistake. Content-Type is the answer to "what is this".
func (s *Server) previewStem(w http.ResponseWriter, r *http.Request) {
	hash := strings.ToLower(r.PathValue("hash"))
	want := strings.ToLower(strings.TrimSpace(r.PathValue("stem")))

	// Tolerate an extension if a caller adds one out of habit; the stem is the name, not the file.
	if ext := strings.ToLower(path.Ext(want)); cuttableExt(ext) {
		want = strings.TrimSuffix(want, ext)
	}
	if want == "" {
		writeError(w, http.StatusBadRequest, "a stem name is required")
		return
	}

	ix, ok := s.index(w)
	if !ok {
		return
	}
	entry, ok := s.resolveChart(w, r, ix, hash)
	if !ok {
		return
	}

	// Match on the catalogue's own stem label, and fall back to the file's base name so a
	// caller that read `assets.stems[].name` rather than `.stem` is not punished for it.
	var chosen string
	for _, st := range entry.Song.Assets.Stems {
		if st.Name == "" {
			continue
		}
		base := strings.ToLower(strings.TrimSuffix(path.Base(st.Name),
			path.Ext(st.Name)))
		if strings.ToLower(st.Stem) == want || base == want {
			chosen = st.Name
			break
		}
	}
	if chosen == "" {
		// 404 and NOT a substitution. Quietly serving a different stem would make a client
		// mixing four stems play one of them four times, and nothing would say so.
		writeError(w, http.StatusNotFound, "this song has no stem called "+want)
		return
	}

	ext := strings.ToLower(path.Ext(chosen))
	if !cuttableExt(ext) {
		writeError(w, http.StatusUnsupportedMediaType,
			"this stem is "+ext+"; Ogg, Opus and MP3 previews are cut, nothing else is")
		return
	}

	startMS, durMS := previewWindow(entry.Song)

	// The stem is IN the tag. Without it every stem of a song shares one ETag and a cache
	// hands out whichever it saw first for all of them - silently, and per client.
	tag := `"` + entry.Song.PackageHash + "-preview-" + strings.ToLower(chosen) + "-" +
		strconv.FormatInt(startMS, 10) + "-" + strconv.FormatInt(durMS, 10) + `"`
	w.Header().Set("ETag", tag)
	if match := r.Header.Get("If-None-Match"); match != "" && strings.Contains(match, tag) {
		w.WriteHeader(http.StatusNotModified)
		return
	}

	f, err := s.openPackage(entry)
	if err != nil {
		s.logError("open song for stem preview", err, "chart_hash", hash, "stem", chosen)
		if entry.Kind.NeedsPacking() {
			writeError(w, http.StatusInternalServerError, "could not pack this song")
		} else {
			writeError(w, http.StatusNotFound,
				"this song is no longer where the index says it is; rescan")
		}
		return
	}
	defer f.Close()

	st, err := f.Stat()
	if err != nil {
		s.logError("stat song for stem preview", err, "chart_hash", hash)
		writeError(w, http.StatusInternalServerError, "could not read this song")
		return
	}

	a, err := sng.Open(f, st.Size())
	if err != nil {
		s.logError("open sng for stem preview", err, "chart_hash", hash)
		writeError(w, http.StatusInternalServerError, "could not read this package")
		return
	}

	inner, found := findInArchive(a, path.Base(chosen))
	if !found {
		writeError(w, http.StatusNotFound,
			"the catalogue lists "+chosen+" and the package does not hold it")
		return
	}

	h, err := a.Open(inner)
	if err != nil {
		s.logError("open stem for preview", err, "chart_hash", hash, "stem", inner)
		writeError(w, http.StatusInternalServerError, "could not read this stem")
		return
	}
	defer h.Close()

	var clip bytes.Buffer
	if ext == ".mp3" {
		err = preview.CutMP3(h, startMS, durMS, &clip)
	} else {
		err = preview.CutOgg(h, startMS, durMS, &clip)
	}
	if err != nil && preview.IsShortStream(err) {
		// Shorter than asked for is still a clip, and the mixer will cope with channels of
		// unequal length. Logged because a library full of these means the windows are wrong.
		s.logError("short stem preview", err, "chart_hash", hash, "stem", inner)
		err = nil
	}
	if err != nil {
		s.logError("cut stem preview", err, "chart_hash", hash, "stem", inner)
		writeError(w, http.StatusInternalServerError, "could not cut a preview from this stem")
		return
	}
	if clip.Len() == 0 {
		writeError(w, http.StatusInternalServerError, "the clip came out empty")
		return
	}

	contentType := "audio/ogg"
	if ext == ".mp3" {
		contentType = "audio/mpeg"
	}
	w.Header().Set("Content-Type", contentType)
	http.ServeContent(w, r, hash+"-"+path.Base(chosen), time.Time{}, bytes.NewReader(clip.Bytes()))
}

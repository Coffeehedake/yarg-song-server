# yarg-song-server — working notes

Part of the "YARG — Open Source Contributions" project. The umbrella instructions live one level
up in the project folder's `CLAUDE.md`; this file covers only this repo.

## Read before writing parser code

**`docs/SOURCES.md` FIRST.** The official wiki documents `song.ini` better than reading YARG.Core
does — it carries defaults, deprecated aliases and a compatibility column that the source cannot
give you — and `.chart`/`.mid` are fully documented by TheNathannator. Deriving a format from
source when a spec exists cost this project four real defects, listed there. Check what is
already written down before reverse-engineering anything.

Then, by subject:

| Document | What it is for |
|---|---|
| `docs/ROADMAP.md` | The build order, with what is done and what each phase must prove |
| `docs/ADR-001-server-architecture.md` | Why Go, why two repos, why sync-first — and what Go costs |
| `docs/ADR-002-v1-store.md` | Why the catalog is in memory, why packed archives are cached to disk, and the two places the server deliberately sorts differently from the client |
| `docs/API.md` | The HTTP surface: every endpoint, what it promises, and what it does not claim |
| `docs/research/chart-preparsing.md` | Track names, note maps and the false-positive traps, cited per claim. **Read before touching `internal/chart`** |
| `docs/research/yarg-song-formats.md` | The `.sng` binary layout, song identity, stem naming. Marked as history at the top: parts of it were superseded, and it says which |
| `docs/TEST-CORPUS.md` | Where test input comes from, what we deliberately do not use, and how to run the oracle |

## Rules

- **Push to `origin` only** (`gitlab.badassium.com/fatalexception/yarg-song-server`). GitHub is a
  downstream force-overwritten mirror — never push there.
- **Never implement CON / mogg decryption**, and never generate `songcache.bin`. Both are hard
  non-goals, not "not yet" items. See the research doc for why.
- **Song identity is `SHA1(chart file bytes)`** and nothing else. If you find yourself hashing a
  folder, the audio, or `song.ini`, stop — the client will never agree with you.
- **Do not "improve" `internal/sortkey`.** It reproduces YARG.Core's `SortString`, including
  three things that look like defects: only uppercase `Æ` expands to `AE`, comparison is by
  UTF-16 code unit rather than code point, and the article list has no `a` or `an`. Each one is
  deliberate and tested, because a browse list that disagrees with the game is wrong even when
  it is more sensible.
- Chart-file priority order (`notes.mid` → `notes.midi` → `notes.chart` → `notes.txt`) is
  load-bearing. Hashing the wrong file in a folder that has two produces a wrong identity silently.

## Verification, not vibes

Go 1.27.0 and mingw-w64 GCC 16.2.0 are installed on ENG-1 as per-user toolchains under
`%LOCALAPPDATA%\Programs\`, both already on the user `Path`. If `go` or `gcc` is not found, the
shell predates the PATH change — open a new one.

- `gofmt -l .`, `go vet ./...` and `go test ./...` must all be clean before a commit.
- **`-race` works on ENG-1 now**, so run it: `go test ./... -count=1 -race`. It needs cgo, which
  needed a C toolchain; mingw-w64 (WinLibs GCC 16.2.0, UCRT/POSIX/SEH) is installed per-user at
  `%LOCALAPPDATA%\Programs\mingw64` and `go env CGO_ENABLED` now reports 1. First run takes about
  16 seconds while the race runtime compiles; after that it is cached.
- The detector was proved able to go **red** before its green was trusted — a throwaway module
  with eight goroutines incrementing a shared int returned `WARNING: DATA RACE` and exit 1. A
  green result from an instrument that has never produced a finding is indistinguishable from an
  instrument that cannot produce one.
- `make release` must succeed for every promised platform — linux/amd64, linux/arm64, linux/armv7,
  darwin/amd64, darwin/arm64, windows/amd64. The Pi target is a project promise, not a bonus.
- Any `.sng` writer must be validated two ways: round-trip through the reference `SngCli`, **and**
  a real YARG install scanning the output and reporting the same hash and metadata. Round-tripping
  against our own reader only proves our reader and writer agree with each other.
- **SngCli lives at `%LOCALAPPDATA%\Programs\sngcli\win-x64\SngCli.exe`** (v0.3.0, MIT). The
  reader is already validated against its output; `internal/sng/testdata/` holds the archive so
  that stays a regression test. `.gitignore` excludes `*.sng` with an explicit exception for that
  directory — keep song libraries out of the repo, keep the reference fixtures in.
- **Do not trust a contained file's extension to describe its container.** SngCli emits audio as
  `.mp3` whatever the source was; a `song.wav` comes back as `song.mp3` with its RIFF header
  intact. Classify by name (as YARG does), but sniff before decoding, and do not reproduce the
  behaviour in our writer.
- **Test fixtures are byte-exact inputs, and `.gitattributes` marks `**/testdata/**` as `-text`
  to keep them that way.** Song identity is `SHA1(chart file bytes)`, so a line-ending translation
  on checkout moves a hash a test asserts. `internal/sng/testdata/reference-notes.chart` is the
  loose copy of the chart that also sits inside `reference-sngcli-v0.3.0.sng`; the archive is
  binary and cannot be translated, so the loose copy must not be either. Before the attribute
  existed the fixture committed LF-normalised while the archive held CRLF, and
  `TestReferenceChartRoundTripsByteExact` therefore passed on ENG-1 (working tree
  `30b18fee1d336a6b83c2fd7e134487d013710e14`) and would have failed on any fresh Linux checkout
  (blob `15ba37a4812c74ad523ddcd614332efc598c3029`) — CI, the Docker build and the Pi. The failure
  presents as "chart bytes differ", which reads as a reader bug. **Never add a fixture that a
  platform is allowed to rewrite.**

## The oracle

A real YARG install is the only thing that can say whether a package is acceptable, and it has
found three bugs no unit test did. Use it whenever scanning or packing behaviour changes:

```powershell
go run ./cmd/mkcorpus -out $env:USERPROFILE\yarg-test\corpus
# edit SongFolders in %USERPROFILE%\AppData\LocalLow\YARC\YARG\release\settings.json,
# delete songcache.bin beside it, launch %LOCALAPPDATA%\Programs\YARG\YARG.exe, wait ~45s
Get-Content "$env:USERPROFILE\AppData\LocalLow\YARC\YARG\release\badsongs.txt"
```

`badsongs.txt` is YARG's verdict on every song it refused. The standard to hold: **every song
YARG rejects should be one this scanner independently flags.** That is true as of the 22-case
corpus; if a change breaks it, the change is wrong or the reason is worth writing down.

Launch YARG windowed (`-screen-fullscreen 0 -screen-width 1280 -screen-height 720`) — it runs on
Jay's workstation and should not take over the screen.

## Docs

Written in the same change as the code. Branded PDFs are FatalException:

```powershell
python "C:\dev\fatalexception-brand-kit\scripts\build-pdfs.py" "C:\dev\YARG - Open Source Contributions\yarg-song-server"
```

Commit the PDFs and `docs/pdf/.manifest.json` together with the markdown they came from.

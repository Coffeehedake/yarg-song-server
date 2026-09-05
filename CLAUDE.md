# yarg-song-server — working notes

Part of the "YARG — Open Source Contributions" project. The umbrella instructions live one level
up in the project folder's `CLAUDE.md`; this file covers only this repo.

## Read before writing parser code

`docs/research/yarg-song-formats.md`. It is the reason the scope is what it is, and it records
what must never be built. `docs/ADR-001-server-architecture.md` records why Go, and what that
choice costs. `docs/ROADMAP.md` has the build order.

## Rules

- **Push to `origin` only** (`gitlab.badassium.com/fatalexception/yarg-song-server`). GitHub is a
  downstream force-overwritten mirror — never push there.
- **Never implement CON / mogg decryption**, and never generate `songcache.bin`. Both are hard
  non-goals, not "not yet" items. See the research doc for why.
- **Song identity is `SHA1(chart file bytes)`** and nothing else. If you find yourself hashing a
  folder, the audio, or `song.ini`, stop — the client will never agree with you.
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

## Docs

Written in the same change as the code. Branded PDFs are FatalException:

```powershell
python "C:\dev\fatalexception-brand-kit\scripts\build-pdfs.py" "C:\dev\YARG - Open Source Contributions\yarg-song-server"
```

Commit the PDFs and `docs/pdf/.manifest.json` together with the markdown they came from.

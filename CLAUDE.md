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

Go 1.27.0 is installed on ENG-1 as a per-user toolchain at `%LOCALAPPDATA%\Programs\go`, already
on the user `Path`. If `go` is not found, the shell predates the PATH change — open a new one.

- `gofmt -l .`, `go vet ./...` and `go test ./...` must all be clean before a commit.
- **`-race` does not run on ENG-1.** The race detector needs cgo, and ENG-1 has no C toolchain
  (`CGO_ENABLED=0`, no gcc), so `go test -race` fails there with "requires cgo" rather than
  passing quietly. Run it in a Linux container or in CI, where `CGO_ENABLED=1`. Installing
  mingw-w64 on ENG-1 would fix it locally; that has not been done, and the rule is written this
  way rather than dropped so nobody assumes a green local run covered concurrency.
- `make release` must succeed for every promised platform — linux/amd64, linux/arm64, linux/armv7,
  darwin/amd64, darwin/arm64, windows/amd64. The Pi target is a project promise, not a bonus.
- Any `.sng` writer must be validated two ways: round-trip through the reference `SngCli`, **and**
  a real YARG install scanning the output and reporting the same hash and metadata. Round-tripping
  against our own reader only proves our reader and writer agree with each other.

## Docs

Written in the same change as the code. Branded PDFs are FatalException:

```powershell
python "C:\dev\fatalexception-brand-kit\scripts\build-pdfs.py" "C:\dev\YARG - Open Source Contributions\yarg-song-server"
```

Commit the PDFs and `docs/pdf/.manifest.json` together with the markdown they came from.

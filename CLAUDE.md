# yarg-song-server — working notes

Part of the "YARG — Open Source Contributions" project. The umbrella instructions — both repos,
their remotes, the tooling and the constraints that span them — are in
[`docs/WORKSPACE.md`](docs/WORKSPACE.md); this file covers only this repo.

*They used to live one level up, in a `CLAUDE.md` at the folder root. That was safe only while
the folder itself synced between machines. Syncthing was retired on 2026-09-06 and **the two
machines are now independent**, so anything outside a pushed repo reaches exactly one machine —
the umbrella instructions moved in here, and the folder root keeps a pointer.*

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
| `docs/ADR-003-archive-ingest.md` | `.zip`/`.7z` ingest, the measured cost of the `.7z` dependency, and why RB packages are refused out loud rather than skipped |
| `docs/ADR-004-remote-song-source.md` | Phase 3: what YARG.Core's code says about a remote song source, and the three increments it implies |
| `docs/API.md` | The HTTP surface: every endpoint, what it promises, and what it does not claim |
| `docs/research/chart-preparsing.md` | Track names, note maps and the false-positive traps, cited per claim. **Read before touching `internal/chart`** |
| `docs/research/yarg-song-formats.md` | The `.sng` binary layout, song identity, stem naming. Marked as history at the top: parts of it were superseded, and it says which |
| `docs/TEST-CORPUS.md` | Where test input comes from, what we deliberately do not use, and how to run the oracle |
| `docs/SCALE.md` | What this costs at 1k / 10k / 31k songs, the memory constant, and what the synthetic library does *not* prove |
| `docs/DEPLOY-VAULT2.md` | The live deployment, and every claim it has and has not established |
| `docs/UPSTREAM.md` | YARC's contribution rules, what we searched before proposing anything, and the draft outreach |
| `docs/WORKSPACE.md` | The umbrella: both repos, remotes, tooling, and the rules that span them |

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
- **What a chart CONTAINS never depends on its metadata.** A preparser that skips deriving notes
  because a title is missing, a format is odd, or the song "will be rejected anyway" is wrong even
  when the rejection prediction is right — and on 2026-09-05 it was not right. `PreparseUltraStar`
  returned early on a missing `#TITLE`, so `21-ultrastar-no-title` reported zero parts while its
  byte-identical twin reported vocals. Keep parsing; raise an issue separately.
- **A green must come from the thing being tested, not from the tester noticing something.** The
  second oracle run recorded "no part carries any difficulty" as though the scanner had flagged a
  song; it had raised no issue at all, and the run was written up as passing a standard it failed.
  If you find yourself reading a JSON field to decide whether something was caught, the answer is
  that it was not caught.
- **Packing is DETERMINISTIC. Never make `PackFS` depend on anything but the files it is given.** The `.sng`
  header mask is derived from the package hash (`sng.MaskKeyFor`), not drawn from `crypto/rand`.
  It is stored in plaintext in the header, so randomising it protects nothing and costs the strong
  `ETag`, the `Range` resume across a cache eviction, and "two machines syncing one server get the
  same files". It *was* random until 2026-09-06, and two clients syncing one server received 16
  different archives out of 22. If a timestamp, a counter, a map iteration order or a random value
  ever reaches the packer, that property is gone again and no single-client test will notice.
- **A test can lock a defect in.** `TestWriteUsesAFreshMask` required two packs of one folder to
  differ, and it was the reason the random mask looked correct. Before adding a test that asserts
  two runs must *not* agree, ask what would depend on them agreeing.
- **Probe before you assert, especially about code you did not write.** The hostile-archive
  tests exist because a throwaway `probe_test.go` *printed* what `archive/zip` actually does
  with traversal entries, corrupt files and odd separators. Reasoning would have produced four
  correct assertions and missed the fifth: a zip with **backslash** separators surfaces a
  directory with nothing readable under it, so a real song was silently ignored. Write the
  probe, read the output, then write the test, then delete the probe.
- **A filesystem view cannot detect its own blind spot.** The first fix for that defect asked
  "does this archive look like a song?" by walking the `fs.FS` — the same view that is blind to
  those entries. It inherited exactly the blindness it existed to catch. The check reads the RAW
  stored entry names for that reason, and the failing test is what said so; do not "simplify" it
  back onto the `fs.FS`.
- **Silence is a decision, and it needs a rule.** "No chart in a zip" is deliberately quiet
  because libraries are full of archives that are not songs. That same silence is unacceptable
  when the archive visibly holds a song, which is why `ErrUnreadableArchive` exists and is
  reported. Both directions are pinned; changing one without the other buries either real
  problems or real songs.
- **Never hand out a path where a handle will do.** The song handler asked the pack cache for
  a path and opened it as a second step; eviction could remove the archive in between, and the
  server answered 404 "no longer where the index says it is" for a song that was present. A
  path is a claim about the past. `packcache.Open` returns an open file for that reason.
- **Tune a regression test against the defect, or it is decoration.** The first version of the
  eviction test passed on the broken code. Two archives and one pass caught it one run in
  three; one archive and three rounds catches it every time. Reintroduce the defect and watch
  the test go red *before* believing it guards anything.
- **A failing measurement is not automatically a failing system.** Three times in one session
  the instrument was the problem: a probe comparing every archive's length to song 0's in a
  library of deliberately different sizes; a load harness using `http.Get` until the machine
  ran out of sockets, whose `status=0` responses were briefly written up as a second defect in
  the pack cache; and a Windows rename-while-open fix argued from share flags that broke every
  request the moment it ran. Check the instrument before the subject, and record the
  correction rather than quietly deleting it.
- **A lossy key makes a table that adds up and is wrong.** The oracle keyed YARG's rejections
  on `Split-Path -Leaf`, but `badsongs.txt` names the offending FILE for some failures, so four
  separate rejections collapsed onto one key of `notes.mid` — hiding half of them AND making the
  songs they belonged to look accepted. The first run reported 3 rejections; the corrected one
  reported 6. When a comparison cannot name what it compared, it must say so and exit
  inconclusive, not average it away.
- **The scanner hashes the chart; it does not read it.** Song identity is `SHA1(chart bytes)`
  and this project deliberately does not reimplement YARG's parser. The price, found on
  2026-09-07: a truncated MIDI and a text file named `notes.mid` both index as healthy songs
  that YARG then refuses. Anything claiming the scanner "validates" a chart is wrong.
- **Comparing counts and totals is not comparing bytes.** Two machines each reported 22 archives
  and 229,515 bytes and were 16 files apart. Any claim of the form "identical" must come from
  hashing every file; a summary line cannot support it. The same applies to a second request that
  is a cache *hit* — it re-reads a file and proves nothing about the producer. Empty the cache
  first, or the test passes no matter what the packer does.
- **The container a song arrived in must never reach its bytes.** A loose folder, a `.zip` of it
  and a `.7z` of it all pack to one archive, and `TestAllThreeContainersProduceTheSameArchive`
  exists to keep it that way. If that breaks, an operator who reorganises a library silently
  invalidates every client's copy of every song in it — the same failure the random mask caused,
  reached from a different direction. Everything archive-shaped goes through `scan.PackFS`; do
  not add a second packing path for a new container.
- **`go list -m all` is not the cost of a dependency.** Adding `sevenzip` made it list Google
  Cloud Storage, gRPC and OpenTelemetry, none of which is compiled in — that is the module
  graph, not the build. The real numbers come from `go list -deps ./cmd/yarg-song-server` and
  the size of the binary. Measured: 3 → 71 packages, +1.62 MB. See `docs/ADR-003`.
- **A Defender false positive on `cmd/yarg-sync` came and went on 2026-09-05.** For about a minute
  `go build ./cmd/yarg-sync` died with *"the file contains a virus or potentially unwanted
  software"* — `Trojan:Win32/Bearfoos.A!ml`, a machine-learning verdict on the shape of a small
  unsigned Go HTTP downloader. **It stopped reproducing roughly forty minutes later with no
  change to the machine**: same signature version, no exclusion added, three forced-relink builds
  and three distinct binary hashes all built, survived and executed with zero new detections. The
  verdict is cloud-delivered and was revised upstream.
  **The lesson is the response, not the detection.** An `!ml` verdict is provisional. Re-measure
  before doing anything structural: never add a Defender exclusion, never allow-list a threat ID,
  and never suggest disabling protection to get a build through. If it recurs and persists,
  submit the binary to Microsoft's false-positive form and sign the release artifact — those fix
  it for players too. Detail in `docs/SYNC-CLIENT.md`.

## Two things that only look broken on ENG-1

`core.autocrlf` is `true` here, so a Windows checkout has CRLF working-tree copies of files the
repo stores as LF. Everything is `i/lf` in the index — verify with `git ls-files --eol` before
believing otherwise — but two local commands lie about it:

- `gofmt -l .` flags any Go file git checked out as CRLF. On 2026-09-05 that was four files, all
  clean in the index and all clean in CI.
- `bash ci/*.sh` fails with `$'\r': command not found`. The scripts are LF in the repo and run
  fine on the runner.

Neither is a defect and neither should be "fixed" by rewriting the files. Trust CI, or
`git ls-files --eol`, over the local worktree.

## Verification, not vibes

Go 1.27.0 and mingw-w64 GCC 16.2.0 are installed on ENG-1 as per-user toolchains under
`%LOCALAPPDATA%\Programs\`, both already on the user `Path`. If `go` or `gcc` is not found, the
shell predates the PATH change — open a new one.

- `gofmt -l .`, `go vet ./...` and `go test ./...` must all be clean before a commit. CI runs
  the same things on every push (`.gitlab-ci.yml`), on a **shell** executor that brings its own
  pinned Go toolchain — do not add an `image:` and expect it to be honoured.
- **If `test:race` in CI dies with "-race requires cgo", that means the Vault2 runner has no C
  toolchain.** Do not drop `-race` to make the pipeline green; that is manufacturing a green
  by removing the instrument. Ask for gcc on the runner instead.
- **`-race` works on ENG-1 now**, so run it: `go test ./... -count=1 -race`. It needs cgo, which
  needed a C toolchain; mingw-w64 (WinLibs GCC 16.2.0, UCRT/POSIX/SEH) is installed per-user at
  `%LOCALAPPDATA%\Programs\mingw64` and `go env CGO_ENABLED` now reports 1. First run takes about
  16 seconds while the race runtime compiles; after that it is cached.
- The detector was proved able to go **red** before its green was trusted — a throwaway module
  with eight goroutines incrementing a shared int returned `WARNING: DATA RACE` and exit 1. A
  green result from an instrument that has never produced a finding is indistinguishable from an
  instrument that cannot produce one.
- **The container image is multi-arch and needs NO emulation.** Go cross-compiles, and the
  Dockerfile pins its build stage with `FROM --platform=$BUILDPLATFORM`, so the toolchain runs at
  the host's architecture and Go emits the arm64 binary. Do not install qemu, do not register
  `binfmt_misc`, and do not change the runner for this. Two non-obvious details: the job must
  create a **`docker-container`** buildx builder, because the default `docker` driver cannot build
  more than one platform or produce a manifest list at all; and `ci/verify-multiarch.sh` reads the
  ELF machine type inside the arm64 image, because buildx exiting 0 is not evidence the arm64 half
  is really arm64.
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
YARG rejects should be one this scanner independently flags.** That is true as of the 23-case
corpus, most recently on 2026-09-06 (21 accepted, 2 refused, and exactly those two flagged by us);
if a change breaks it, the change is wrong or the reason is worth writing down.

Launch YARG windowed (`-screen-fullscreen 0 -screen-width 1280 -screen-height 720`) — it runs on
Jay's workstation and should not take over the screen.

## Docs

Written in the same change as the code. Branded PDFs are FatalException:

```powershell
python "C:\dev\fatalexception-brand-kit\scripts\build-pdfs.py" "C:\dev\YARG - Open Source Contributions\yarg-song-server"
```

Commit the PDFs and `docs/pdf/.manifest.json` together with the markdown they came from.

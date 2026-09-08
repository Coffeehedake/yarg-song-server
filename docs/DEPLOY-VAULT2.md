# Running the server as a container

The published image is `registry.badassium.com/fatalexception/yarg-song-server`,
tagged `latest` and the commit short sha. It is a distroless static binary —
**8.6 MB**, no shell, running as `nonroot`.

This document records the first real deployment, on vault2, 2026-09-05.

## What this deployment proved, that nothing had proved before

The multi-arch CI job verifies the image config's architecture and the ELF
machine type of the binary inside. Both are checks on *bytes*. Until this
deployment **the published image had never actually been run** — not by CI, not
by anyone. It now has:

```
library indexed songs=22 distinct_charts=22 duplicate_packages=0 problems=0 took=7ms
listening addr=:8080 songs=/songs data=/data version=827e0fe
```

`version=827e0fe` is how you know the image is the commit it claims to be.

It also moved the server off the development machine for the first time. Every
earlier measurement was `127.0.0.1`, which cannot fail in any of the ways a real
network can.

## The deployment

```bash
# on the host
mkdir -p /mnt/cache/appdata/yarg-song-server/songs \
         /mnt/cache/appdata/yarg-song-server/data
chown -R 65532:65532 /mnt/cache/appdata/yarg-song-server/data

docker run -d --name yarg-song-server \
  --restart unless-stopped \
  -p 8099:8080 \
  -v /mnt/cache/appdata/yarg-song-server/songs:/songs:ro \
  -v /mnt/cache/appdata/yarg-song-server/data:/data \
  registry.badassium.com/fatalexception/yarg-song-server:latest
```

> **`:latest` CHANGED MEANING ON 2026-09-08, and everything written below this
> line predates that.** It used to follow the default branch, so `:latest` was
> "the newest commit on main" and the deployment records further down confirm it
> that way — correctly, for the day they were written. It now means **the newest
> release tag**. The tags are:
>
> | Tag | Means |
> |---|---|
> | `:<short sha>` | that exact build, eight characters, never moves — **this is what a deployment pins** |
> | `:main` | the default branch head; the honest replacement for what `:latest` used to be |
> | `:vX.Y.Z` | a release, built from that tag's own commit |
> | `:latest` | the newest **release**, which is deliberately older than main most of the time |
>
> The `docker run` above uses `:latest` only to get a first container onto the
> box. **Pin the eight-character sha for anything you intend to keep**, exactly
> as the rest of this document does.
>
> **A digest identifies a BUILD, not a commit.** Commit `6c686ce` was built
> twice on 2026-09-08 — once from `ci/tag-images`, once from `main` — with an
> identical version string (`v0.1.0-5-g6c686ce`) and identical source, and the
> two images have different digests (`e982e528…` and `ee21f46f…`). Container
> images are not bit-reproducible here the way the release archives are: the
> image config carries build timestamps and nothing sets `SOURCE_DATE_EPOCH`.
> So the digest comparison used further down proves "this tag and that tag are
> the same image", which is what it was used for — it does **not** prove "this
> image is the only one that commit can produce". The `:<sha>` tag resolves to
> whichever build of that commit pushed last.

Three things there are not decoration:

- **`chown 65532`.** The image is distroless and runs as `nonroot`, uid 65532.
  `/data` holds the on-demand pack cache and must be writable by that uid or the
  server starts fine and then fails the first time somebody asks for a song that
  is stored as anything but a `.sng` — a loose folder, a `.zip` or a `.7z`, all of
  which are packed on demand. `/songs` stays root-owned and is mounted `:ro`,
  because the server never writes to the library and should not be able to.
- **`/mnt/cache/...`, not `/mnt/user/...`.** An absolute pool path, not the shfs
  union. This host has a documented history of SQLite-on-FUSE corruption, and
  `/mnt/user` was **99% full** at deployment (568 GB of 29 TB) while the cache
  pool had 1.4 TB free.
- **`-p 8099:8080`.** 8099 was free; the container listens on 8080 internally.
  Checked against `ss -ltn` rather than assumed — this host had 59 containers
  running and 76 ports already in LISTEN.

## Registry authentication

The host's stored credential was stale and `docker pull` failed with
`unauthorized: HTTP Basic: Access denied`. A GitLab personal access token with
the **`api`** scope works for a registry pull — measured; `read_registry` is the
documented scope but was not required here.

Log in without putting the secret in `argv`, where `ps` would show it:

```bash
docker login registry.badassium.com -u <user> --password-stdin <<'ENDTOK'
<token>
ENDTOK
```

The token comes from 1Password at run time and is never written to a file, a
commit, or this document.

## End-to-end result, 2026-09-05

| Step | Result |
|---|---|
| Pull and start on vault2 | running, 0 restarts, indexed 22 songs in 7 ms |
| `GET /healthz` from the host | 200 |
| `yarg-sync` from ENG-1 over Tailscale | 22 downloaded, 0 failed, 229,515 bytes in **341 ms** |
| Byte count vs the localhost run | **identical** — same archives, different machine |
| Second sync | 0 downloaded, 22 already present, **0 bytes**, 19 ms |
| **Unmodified YARG on the synced folder** | **20 accepted, 2 refused** |

The two refusals were the same two, for the same reasons, as every earlier run —
`no notes found` and `no audio accompanying the chart file`, both independently
flagged by our own scanner.

The client reached the server by its **Tailscale name**, not a LAN literal, so
the same command works from anywhere ENG-1 happens to be. A `192.168.2.x`
address would have worked at the time of the test and failed from the office.

## The pack cache bound, verified here rather than in a test

`pack_cache_max` shipped with six red-proofed tests and did not work. Every test
inserted, so every test reached eviction through the insert path; this
deployment's cache was already full, every request was a hit, nothing was ever
packed, and the bound was never enforced. Deploying it found that in under a
minute.

After the fix (`6bcf4ec`, enforce at start as well as on insert), running
deliberately tight at **102,400 bytes** against a library that packs to 229,515:

| Moment | Cache |
|---|---|
| Before restart, from the broken build | 229,515 bytes, 22 archives |
| **Immediately after start, having served nothing** | **67,681 bytes, 6 archives** |
| After serving all 22 songs | 67,681 bytes, 6 archives |
| Songs the client received | **22 of 22, 0 failed** |

Three things in that table, in order of importance:

- The cache dropped from 22 archives to 6 **before a single request arrived**.
  That is the start-up enforcement, and it is the case the tests all missed.
- It stayed at 6 while serving 22 songs, so eviction kept pace with insertion
  rather than merely happening once.
- **Every song was still delivered and verified.** Sixteen of the twenty-two had
  to be re-packed mid-sync because eviction had removed them, and the client —
  which re-derives each song's identity from the bytes it receives — accepted
  all of them. That is the concrete evidence that eviction costs a re-pack and
  never data.

  > **The conclusion drawn from that third bullet was too strong, and the number
  > in it was a clue nobody read.** "Eviction costs a re-pack and never data" is
  > true; "and therefore the re-packed archive is the same archive" does not
  > follow, and was false. Those same sixteen songs came back with *different
  > bytes* — the packer drew a random mask per call — which the client could not
  > notice, because it verifies the chart's identity and the chart is copied
  > byte for byte. Identity survived; the archive did not. The next day the same
  > sixteen turned up as sixteen SHA-256 mismatches between two machines, and
  > that is what named the defect. Fixed on 2026-09-06 by deriving the mask;
  > `docs/TEST-CORPUS.md` has the run. **A client that verifies identity will
  > accept a byte stream that a cache, an `ETag` and a `Range` resume all
  > require to be stable — so client acceptance is not evidence of stability.**

## What this still does not prove

- ~~**arm64 has never been executed.**~~ **Closed 2026-09-06.** Both the
  cross-compiled arm64 binary and the published arm64 container image ran on a
  Raspberry Pi 4 Model B Rev 1.5 (`aarch64`, Debian 13 trixie), each indexing the
  22-song corpus in about 25 ms, and the archives they served were byte-for-byte
  identical to every x86-64 sync. See `docs/TEST-CORPUS.md`, "The ARM leg".
  Note the caveats there: it was a Pi 4 rather than the 3B+ (that board is dead),
  the machine was one-shot and wiped, and the image was transported by
  `docker save`/`load` rather than pulled directly on the Pi.
- ~~**The Phase 2b exit criterion needs two clients.**~~ **Closed 2026-09-06.**
  YARG was installed on r7-desktop and both machines were scanned: 20 accepted,
  2 refused on each, the same two songs. Doing it is what found the determinism
  defect above — see `docs/TEST-CORPUS.md`, fourth oracle run.
- ~~**One library, 22 songs, 224 KB.**~~ **Partly closed 2026-09-06.** Index time is
  linear at 0.57-0.59 ms/song and memory fits `13 MB + 6.5 KB/song`, measured at 1,000 /
  10,000 / 31,109 songs; packing streams, with resident memory flat at 16.5 MB while
  serving 1.17 GB, and the cache bound held at 11x oversubscription. See
  `docs/SCALE.md`. **Still open:** every song in that experiment is synthetic, so a real
  library's *variety* is untested, and the oracle has never run at scale - which is the
  measurement most likely to find a rejection category we do not flag.

## Re-deployed 2026-09-06 on `c623dae`, and what it proved beyond the fix

Every number above was taken from a server that packed with a random mask
(`6bcf4ec` was running; an earlier revision of this document said `827e0fe`,
which was the deployment before it). Pipeline 2259 published `c623dae` and the
host was re-pulled:

```bash
docker pull registry.badassium.com/fatalexception/yarg-song-server:latest
docker rm -f yarg-song-server
rm -rf /mnt/cache/appdata/yarg-song-server/data/packs   # start with an empty cache on purpose
docker run -d ...                                       # identical arguments to the first deploy
```

The stored registry credential from 2026-09-05 was still valid, so no fresh
login was needed. Wiping the pack cache first is deliberate: every archive is
then packed by the new binary, and nothing served afterwards is a leftover of
the random-mask era.

```
library indexed songs=22 distinct_charts=22 duplicate_packages=0 problems=0 took=11ms
pack cache bounded max_bytes=2147483648
listening addr=:8080 songs=/songs data=/data version=c623dae
```

**Then five independent syncs were compared file by file:**

| Client | Server | Result |
|---|---|---|
| ENG-1 | ENG-1, built from the working tree (windows/amd64) | reference |
| ENG-1 | same, after wiping the whole pack cache | identical |
| r7-desktop | ENG-1's server | identical |
| ENG-1 | **the vault2 container (linux/amd64)** | **identical** |
| r7-desktop | **the vault2 container (linux/amd64)** | **identical** |

110 archives, two client machines, two servers built for different operating
systems and by different toolchains, and one deliberate cache wipe — all one set
of bytes. **The derivation is not platform-dependent**, which the fix needed to
be but which nothing had shown until this run. Unmodified YARG on the
container's output: **20 accepted, 2 refused**, the same two songs.

Worth stating plainly because it is the standard this project holds: the last
row of that table is an identity established by hashing every file, not by
arguing that identical inputs must give identical results.

## Re-deployed 2026-09-06 on `423902b`, with archive ingest

The library on the host was replaced with the 23-case corpus, which includes
`23-zipped.zip` — a song delivered as an archive rather than a folder — and the
pack cache was wiped so nothing served afterwards was a leftover of the previous
image.

```
library indexed songs=23 distinct_charts=23 duplicate_packages=0 problems=0 took=9ms
listening addr=:8080 songs=/songs data=/data version=423902b
```

| Check | Result |
|---|---|
| `/version` | `423902b` |
| `/healthz` | 200 |
| The archive-sourced song in the catalog | `name="Zipped"`, `source_path="23-zipped.zip"`, **0 issues** |
| `yarg-sync` from an empty folder | 23 downloaded, 0 failed, 238,215 bytes in 399 ms |
| Every archive vs the locally built server | **23 / 23 identical** |

That last row is the container-independence property crossing a build platform:
the song the **linux/amd64 container** ingested from inside a `.zip` is
byte-identical to the one a **windows/amd64** build produced from the loose
folder. Nothing about the container it arrived in, or the machine that packed
it, reaches the bytes a client receives.

The host's stored registry credential was still valid, so no fresh login was
needed.

## Re-deployed 2026-09-06 on `077f36e`, and the hostile-archive fix measured on the real thing

Pipeline 2278 published `077f36e` (archive-ingest hardening). Two things were worth
measuring here that a test cannot reach: whether a code change altered the bytes a client
receives, and whether the new "this archive is unreadable" report actually arrives where an
operator would see it.

**`latest` was confirmed to be the commit before it was run**, not assumed:

```bash
docker pull …:latest
docker pull …:077f36eb        # note EIGHT characters - CI tags with CI_COMMIT_SHORT_SHA
docker image inspect …:077f36eb --format '{{.Id}}'   # same image id as :latest
```

That the tag is eight hex characters and the git short sha is seven is a small trap: a
`:077f36e` pull fails with `manifest unknown`, which reads like a missing image rather than a
mistyped tag. `GET /api/v4/projects/53/registry/repositories/14/tags` lists what exists.

### Nothing about a code change reached the bytes

The pack cache was hashed **before** the upgrade, wiped, and hashed again after the new
binary had re-packed all 23 songs:

```bash
sha256sum *.sng | sort -k2 > /tmp/packs-423902b.txt   # before
# … pull, rm -f, rm -rf data/packs, docker run …
sha256sum *.sng | sort -k2 > /tmp/packs-077f36e.txt   # after
diff /tmp/packs-423902b.txt /tmp/packs-077f36e.txt    # no output
```

| Check | Result |
|---|---|
| `/version` | `077f36e` |
| `/healthz` | 200 |
| Index | 23 songs, 23 distinct charts, **0 problems**, 9 ms |
| 23 archives re-packed on a wiped cache, vs the `423902b` cache | **byte-for-byte identical** |
| `yarg-sync` from ENG-1 over Tailscale | 23 downloaded, 0 failed, 238,215 bytes in 312 ms |
| The 23 files the client received, hashed, vs the 23 packs on the server | **identical set** |

The determinism property now holds **across a code change**, which is a different claim from
the ones already recorded — those compared machines, operating systems and architectures at a
fixed commit. Byte-stability across upgrades is what a client's `ETag` and `Range` resume
actually depend on in practice, since a server gets upgraded far more often than it changes
CPU.

### The silently-ignored song, reproduced and then observed being reported

`077f36e` exists because a zip written with **backslash** separators used to disappear from a
library with no message. A probe archive was built to be exactly that shape — `song.ini`,
`notes.mid` and `song.ogg` under `Backslash Song\`, written with the separator stored
verbatim — dropped into the host's library, and the server restarted.

Building the probe is itself instructive. Python's `zipfile` rewrites `os.sep` to `/` in
`ZipInfo.__init__`, so `ZipInfo("Backslash Song\\song.ini")` silently produces a *forward*
slash and the probe proves nothing; the name has to be assigned after construction. And
`namelist()` normalises backslashes when reading back, so the file looks wrong even when it is
right — the only honest check is to count `0x5C` bytes in the file itself. **Both the writer
and the reader hide the thing being tested**, which is the same shape as the defect: a
convenient view of an archive is not the archive.

```
level=INFO  msg="library indexed" songs=23 distinct_charts=23 duplicate_packages=0 problems=1
level=WARN  msg="could not index" path=24-backslash.zip
            err="scan: archive contains a song.ini or a chart but no song folder could be read
                 from it; if it was made by an old Windows tool it may use backslash path
                 separators - re-zipping it will fix that"
```

It appears in `/api/v1/library` under `problems` as well as in the log, so an operator with no
shell on the host still sees it. Before this commit the same file produced `problems=0` and no
mention anywhere.

The probe was removed afterwards and the library restored to the 23-case corpus — a permanent
`problems=1` would make every future index report ambiguous. `0 problems`, `restarts=0`,
health 200 at the end.

## Re-deployed 2026-09-07 on `64b42af`, and the first test with real concurrent clients

`64b42af` is the build that stopped the server 404-ing songs that exist (see
[ADR-002](ADR-002-v1-store.md), "Concurrency"). Deployed the same way: confirm the tag is the
commit by image id, wipe the pack cache, recreate.

**Take the eight-character tag from `git rev-parse`, do not type it from memory.** The image
tag is `CI_COMMIT_SHORT_SHA` — eight characters — and the git short sha is seven. Guessing the
eighth produced `64b42af7` and a `manifest unknown`, which reads like a missing image rather
than a typo. The sha is `64b42af1b368…`, so the tag is `64b42af1`, and its image id matched
`:latest` exactly.

| Check | Result |
|---|---|
| `/version` | `64b42af` |
| `/healthz` | 200, `restarts=0` |
| Index | 23 songs, 23 distinct charts, **0 problems**, 15 ms |
| 23 archives re-packed on a wiped cache, vs the `077f36e` cache | **byte-for-byte identical** |

That is determinism holding across a **second** code change, on top of the machines,
operating systems and architectures already covered.

### Four real clients at once, over the network

Every concurrency measurement until now used goroutines against an in-process test server.
Four `yarg-sync` processes were run simultaneously from ENG-1 against the vault2 container
over Tailscale:

| Client | Result |
|---|---|
| 1 | 23 downloaded, 0 failed, 238,215 bytes, 456 ms |
| 2 | 23 downloaded, 0 failed, 238,215 bytes, 448 ms |
| 3 | 23 downloaded, 0 failed, 238,215 bytes, 483 ms |
| 4 | 23 downloaded, 0 failed, 238,215 bytes, 449 ms |

**Every file byte-identical across all four clients**, compared by SHA-256 per filename rather
than by count or total. Four simultaneous clients cost about 40 ms each against the ~410 ms a
single client took earlier, so nothing here is serialising badly.

**What this run does NOT test, stated because it would be easy to read it as more than it
is:** this deployment's cache bound is 2 GB against a 238 KB library, so **nothing is ever
evicted** and the defect `64b42af` fixes could not fire here even on the old build. This
exercises the concurrent *serve* path over a real network; the eviction race is covered by
`internal/httpapi/concurrency_test.go`, which has to bound the cache to a single archive to
provoke it at all. A Pi with a small SD card and a large library is where the two conditions
meet, and that combination is still unmeasured on real hardware.

## Upgrade to `a5f98c4`, 2026-09-07 — the pack-cache race and the chart checks

Pipeline 2295. Two changes worth deploying: the eviction race in `packcache` that answered
**500 for a song that exists** on Linux (see `docs/ADR-002-v1-store.md`), and the two new
chart issues, `chart_unreadable` and `chart_truncated`.

`:latest` was confirmed to BE the commit before anything was recreated, by image id rather
than by trust:

```bash
docker pull -q …:latest
docker pull -q …:a5f98c41     # EIGHT characters; CI tags with CI_COMMIT_SHORT_SHA
docker image inspect …:latest    --format '{{.Id}}'   # sha256:b2bc48bb…
docker image inspect …:a5f98c41  --format '{{.Id}}'   # sha256:b2bc48bb…  same
```

The container was pinned to `:a5f98c41` rather than `:latest`, so `/version` and the running
image agree even after the next pipeline publishes.

### Determinism held across a fourth code change

The pack cache was hashed before the upgrade, wiped, and every song re-fetched so the new
binary re-packed all 23:

```
archives before: 23  after: 23
BYTES IDENTICAL across the code change
```

That is the fourth code change this has been checked across, and the check is cheap enough
that there is no reason to stop doing it.

| Check | Result |
|---|---|
| `/version` | `a5f98c4` |
| `/api/v1/library` | 23 songs, 23 distinct charts, **`problems: null`** |
| Browse page `GET /` | 200, 9,365 bytes |
| `GET /nope` | 404 — the catch-all trap has not come back |
| Pack cache after re-fetch | 23 archives, byte-identical to before |

**What this does NOT verify, and it is the important one:** the defect this upgrade exists to
fix cannot fire on this deployment. The bound here is 2 GB against a 225 KB library, so
nothing is ever evicted, and the rename-then-open window only opens when eviction runs
concurrently with packing. The fix is covered by
`TestEvictionNeverStealsAnArchiveBeforeItsPackerCanOpenIt`, which has to bound the cache to a
single archive to provoke it at all. **A green deployment here is not evidence about the
race.** The place the two conditions actually meet is a Pi with a small card and a large
library, and that is still unmeasured on real hardware.

## Second deployment, 2026-09-08 — `a5f98c4` to `3f80da0`

The live server had drifted a day behind `main`, so it was missing the browse page's scan-health
view among other things. Updated in place, verified rather than assumed.

**The image tag is the EIGHT-character short sha**, which is the thing most likely to trip
somebody up: `3f80da0ad757…` publishes as `:3f80da0a`, not `:3f80da0`. A seven-character pull
fails with `manifest unknown`, which reads like a missing image rather than a typo.

**The old container's configuration was read, not recalled.** `docker inspect` gave the exact
`Cmd`, port bindings, restart policy and mounts, and the new container was created from those
rather than from the example earlier in this document. An example in a doc is a record of what was
run once, not a description of what is running now.

```bash
docker pull registry.badassium.com/fatalexception/yarg-song-server:3f80da0a
docker rename yarg-song-server yarg-song-server-prev && docker stop yarg-song-server-prev
docker run -d --name yarg-song-server --restart unless-stopped -p 8099:8080 \
  -v /mnt/cache/appdata/yarg-song-server/songs:/songs:ro \
  -v /mnt/cache/appdata/yarg-song-server/data:/data \
  registry.badassium.com/fatalexception/yarg-song-server:3f80da0a \
  --songs /songs --data /data --listen :8080
```

**The previous container is renamed and stopped, not deleted.** Rolling back is
`docker rm yarg-song-server && docker rename yarg-song-server-prev yarg-song-server && docker start
yarg-song-server`. Delete it once the new one has been trusted for a while.

### Verified after the swap

| Check | Result |
|---|---|
| `/version` | `3f80da0` — the image is the commit it claims to be |
| Startup log | `songs=23 distinct_charts=23 duplicate_packages=0 problems=0 took=19ms` |
| Browse page carries the health machinery | all four markers present |
| Browse page still self-contained | **0** external references |
| A real song downloads | `200`, 8,471 bytes, begins `SNGPKG` |
| `/nope` | `404` — the root is still an exact match, not a catch-all |

The last two matter more than they look. A deployment that serves a page but not a song is a
deployment that passes a health check and fails a player; and the `GET /{$}` versus `GET /`
distinction is the kind of thing an image rebuild could quietly undo, turning every 404 into HTML
that a sync client would try to parse as a `.sng`.

## State check over Tailscale, 2026-09-08 13:52Z — read-only, nothing deployed

Reached vault2 as `vault2.tail20252e.ts.net` rather than `192.168.2.7`; the Tailscale name
works off-LAN and the LAN literal does not.

| Measured | Value |
|---|---|
| `docker inspect` | `running`, started `2026-09-08T02:39:26Z`, **restarts=0** |
| Image | `registry.badassium.com/fatalexception/yarg-song-server:3f80da0a` |
| Ports | `8080/tcp -> 0.0.0.0:8099` |
| `GET /healthz` on `:8099` | `200` |
| Startup log | `songs=23 distinct_charts=23 duplicate_packages=0 problems=0 took=19ms` |
| Commits behind `origin/main` (`3f80da0..7e12e55`) | **17** |

The live deployment is healthy and has not restarted; it is simply old on purpose. Nothing here
was changed — deploying, and turning `check_uploads` on, are both still Jay's calls.

**The image is distroless, so `docker exec <name> sh -c ...` fails with
`exec: "sh": executable file not found in $PATH`.** That is the image behaving correctly, not a
broken container. An authenticated `/api/v1/*` probe therefore cannot be run from inside the
container; use `/healthz` (unauthenticated) plus the startup log, or call the API from a host that
holds the key.

### The registry's on-disk tag directory is NOT a reliable inventory

Listing
`/var/opt/gitlab/gitlab-rails/shared/registry/docker/registry/v2/repositories/<ns>/<img>/_manifests/tags`
inside the GitLab-CE container returned **69** tags and omitted `7e12e557` — the tag for `main`'s
head, whose pipeline had succeeded 90 minutes earlier. The GitLab API returned **70**, including it.
Read from the disk, the conclusion would have been "CI never published an image for HEAD", which is
false and would have been the ninth wrong-instrument finding in this project.

**Use the API, and take the digest, not the tag name:**

```
GET /api/v4/projects/<id>/registry/repositories
GET /api/v4/projects/<id>/registry/repositories/<repo_id>/tags/<tag>   -> .digest
```

`latest` and `7e12e557` resolve to the same digest (`sha256:2597abd874b3b646…`), so `:latest`
currently *is* head-of-`main` — but that is a measurement with a timestamp on it, not a property.
Compare digests every time; a tag name is a label somebody can move.

Pipeline history for the 17 undeployed commits is 16 `success` and one `failed` (`e8eda83b`,
a docs commit). Project id is **53**; the credential is Windows Credential Manager
`dev:gitlab-ce-pat`, read via `CredRead` so the token never reaches a command line or the
transcript.

## Third deployment, 2026-09-08 — `3f80da0` to `027d331`, and v0.1.0

31 commits, covering the feature registry, the config menu and its persistence, `-trimpath`, and
the release packaging. **Feature defaults are unchanged**, so this is a code update rather than a
posture change: `browse_ui` on, `check_uploads` off, `config_writes` off.

**The pull failed first, and the reason is a landmine this document has warned about in general
terms.** vault2's cached registry credential in `/root/.docker/config.json` had one auth entry and
was refused (`denied: access forbidden`). `/root` does not survive a reboot on Unraid, so a cached
docker login is not durable infrastructure — it is something that works until it doesn't, silently,
at the moment somebody needs to deploy.

**The 1Password item named `GitLab Registry Pull (vault2 deploy token)` is NOT for this project.**
Its username is `fallout-pull` and it is scoped to fallout-research: `docker login` succeeds with
it and the very next `docker pull` is denied, which reads like a registry fault rather than a
scope one. The generic name is the trap. What worked is the documented route — a GitLab PAT with
`api` scope, `--password-stdin` so the token never reaches `argv`.

**The login was removed again immediately after the pull** (`docker logout`). An `api`-scoped
token is far more than a pull needs, and leaving one cached on the box is a standing over-grant.
The container runs from the local image afterwards and needs no registry access. **The right fix
is a project-scoped `read_registry` deploy token for `fatalexception/yarg-song-server`, stored in
1Password under a name that says which project it belongs to** — that is a credential change and
belongs to Jay.

```bash
docker pull registry.badassium.com/fatalexception/yarg-song-server:027d331f
docker rm -f yarg-song-server-prev            # the 16h-old rollback, now superseded
docker rename yarg-song-server yarg-song-server-prev && docker stop yarg-song-server-prev
docker run -d --name yarg-song-server --restart unless-stopped -p 8099:8080 \
  -v /mnt/cache/appdata/yarg-song-server/songs:/songs:ro \
  -v /mnt/cache/appdata/yarg-song-server/data:/data \
  registry.badassium.com/fatalexception/yarg-song-server:027d331f \
  --songs /songs --data /data --listen :8080
```

The container's configuration was **read from `docker inspect`, not recalled from this document** —
an example here is a record of what was run once, not a description of what is running now.

### Verified after the swap

| Check | Result |
|---|---|
| `/version` | `027d331` — the image is the commit it claims to be |
| Startup log | `songs=23 distinct_charts=23 duplicate_packages=0 problems=0 took=19ms` |
| `/healthz` | 200 |
| `GET /` | 200, 19,956 bytes (the page grew with the settings panel) |
| `GET /nope` | **404** — the root is still an exact match, not a catch-all |
| `POST /api/v1/check` | **404** — off, and still *absent* rather than forbidden |
| `PUT /api/v1/features/{name}` | **404** — writes off, so the route is not there |
| A real song | 200, 8,471 bytes, begins `SNGPKG` |

The last three matter most on this deployment. The routes for `check` and the feature writer are
now **registered unconditionally** — that changed in this batch so features could be toggled at
runtime — and the guarantee that a disabled feature is indistinguishable from an absent one is now
kept by the handlers rather than by the router. Those two 404s are that guarantee, measured on the
live server rather than only in a test.

**`config_writes` is off here, so nobody can change this server's features over HTTP.** Setting it
to `local` would only help somebody with a shell on vault2 anyway; `lan` is the one that would
matter, and on a box reachable from the whole house that is a decision rather than a convenience.

### v0.1.0

Tagged at `027d331`. The `release:tag` job produced all six archives plus `SHA256SUMS` with
**`artifacts_expire_at = NEVER`**, which is the whole point of tagging.

**A tagged pipeline does NOT build a container image.** `container-image` runs on the default
branch and on `ci/*`, and a tag has no `CI_COMMIT_BRANCH`, so it is skipped. The image for this
commit exists as `:027d331f` from the `main` pipeline, so nothing is missing today — but a tag and
its image are not linked, and anyone expecting `:v0.1.0` in the registry will not find it.

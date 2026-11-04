# ADR-009: Preview clips, cut without a decoder

**Status:** Accepted, implemented and **playing in the client** as of 2026-11-04.
`internal/preview` cuts Ogg and Opus, `GET /song/{chart_hash}/preview.ogg` serves it, deployed on
vault2 as `3a9bf3c`, and `RemoteSongEntry.LoadPreviewAudio` fetches it — verified by VLC and then
by **BASS, the game's own decoder** (both below). **MP3 is cut too as of 2026-11-04** — see
*"What the MP3 cutter actually fixed"* below, because the reason first written here was wrong:
those 150 songs were never silent. Clips are still not cached on disk.

**Context:** Jay, playing the fork with the server library browsing: *"the song preview does not
load"*. Correct, and by design — `RemoteSongEntry.LoadPreviewAudio` returns `null`, because a song
the server published and this disk does not hold has no bytes to preview. Jay asked for a server
endpoint instead of a client-side download.

## What the server already has

Nothing needs adding to the catalogue. Every song already carries:

| Field | Source |
|---|---|
| `preview_start_ms`, `preview_end_ms` | `internal/scan/scan.go:229`, from `song.ini` |
| `assets.stems` | `internal/scan/scan.go:422` |
| stem classification | `internal/scan/names.go:87` `ClassifyStem` |

And `internal/sng` implements `fs.FS`, so a stem inside a package opens as an ordinary file.

## The decision

**Cut the clip at container boundaries. Do not decode, do not re-encode, add no dependency.**

### Why not ffmpeg

This was the obvious first answer and it is wrong here, for a reason that is specific to this
deployment rather than general. The runtime image is `gcr.io/distroless/static-debian12:nonroot`
(`Dockerfile:19`) — **static distroless: no shell, no package manager, nothing to install into.**
Adding ffmpeg means replacing the base image and carrying roughly 60–100 MB of it, against
ADR-001's "tiny container" and the Raspberry Pi target that is a project promise rather than a
bonus. ADR-003 already set the precedent for pricing a dependency before taking it: `sevenzip`
cost 3 → 71 packages and +1.62 MB, and that was judged worth it. An ffmpeg layer is two orders of
magnitude more and would also need an arm64 build for the Pi.

### Why not range-serve the stem

The cheap alternative — serve bytes *N..M* of the stem and let the client's BASS decoder resync —
was offered and then **ruled out by measurement**, not by taste. Counted across the live
catalogue, 22,091 stem files:

```
.ogg     20288
.opus     1354
.mp3       449
```

**98% is Ogg-family.** An Ogg file declares its codec, sample rate and channel count in header
pages at the *start* of the file; hand a decoder bytes from the middle and there is nothing to
decode with. Range-serving would have worked for the 449 MP3 stems and failed for the other
21,642. It is not a cheaper option, it is a broken one.

### What "cut without decoding" means

An `.ogg` file is not one blob. It is a sequence of self-describing **pages**, each 27 bytes of
header plus a segment table plus payload, carrying a `granule_position` that says how far into
the stream the page ends. Playing audio means turning pages into sound, which needs a codec.
*Cutting* audio means copying whole pages, which needs only the ability to find where pages begin
and end — reading headers, not decoding them.

The same shape holds for the other two formats, which is why one approach covers all of them:

| Format | Unit | How its position is known |
|---|---|---|
| Ogg Vorbis | page | `granule_position`, in samples at the rate in the identification header |
| Ogg Opus | page | `granule_position`, always in 48 kHz units regardless of input rate |
| MP3 | frame | duration from the header's bitrate and sample rate, found by the sync word |
| WAV | byte | raw samples; pure arithmetic — **not implemented, see below** |

So the algorithm, for the 98% case:

1. copy the leading pages whose `granule_position` is 0 — those are the headers (`\x01vorbis`
   identification plus comment and setup, or `OpusHead` plus `OpusTags`), and copying them is
   what makes the output a standalone playable file rather than a fragment
2. read the sample rate out of the identification packet, so granule positions convert to time
3. skip pages until `granule_position >= preview_start`
4. copy pages until the clip is long enough
5. set the EOS flag on the last page, renumber the page sequence, and **recompute the CRC** of
   any page whose header was touched

Output: a valid Ogg of a few hundred KB instead of a ~50 MB download.

## What this costs, stated honestly

It is a parser, and parsers are where this project has found most of its defects. Two known
wrinkles rather than surprises:

- **Vorbis uses overlapping windows**, so the first audio page of a cut clip has no predecessor
  to overlap with and the first ~20 ms can glitch. Acceptable for a preview; worth knowing before
  somebody reports it as corruption.
- **Granule positions must be rebased** so the clip starts near zero, or a player reads it as a
  seek into a longer stream and may report a wrong duration.

## How it must be verified, and what would not count

**A clip this code calls valid is not a clip somebody can play.** The repo already has this rule
in two forms: *"a forgiving reader hides a strict writer, so exercise a new container by
EXTRACTING it"*, and *"round-tripping against our own reader only proves our reader and writer
agree with each other."* Both apply exactly. A cutter and a parser written in the same afternoon
will agree with each other about a malformed page.

So the standard is an **independent decoder**: the clip must play in the real YARG client, which
is the oracle this project already uses for packaging questions, and `BASS` is not our code. A Go
test asserting page structure is necessary and is not sufficient, and must not be reported as
proof the feature works.

The unit tests build Ogg streams by hand — pages with known granule positions — because that
tests the page arithmetic precisely and needs no fixture. Per the repo's rule, the cut must be
seen to go **wrong** first: a test that cuts at a known boundary and asserts the wrong page count
should fail before the correct one is trusted.

## The endpoint

```
GET /song/{chart_hash}/preview.ogg
```

- 404 when the hash is unknown, as `/song/{hash}.sng` already does
- 415 when the stem is a format this cannot cut
- strong `ETag`, because the output is deterministic — the same package and the same
  `preview_start_ms` always yield the same bytes, which is the property ADR-002 and the
  packer's deterministic mask already protect
- cached on disk beside the packed archives, so a clip is cut once

**Which stem.** `ClassifyStem` can report several. The preview should use the full mix where a
song has one, and otherwise the loudest-weighted guitar or vocals stem rather than an arbitrary
first entry — mixing stems would mean decoding, which is the one thing this design avoids. That
choice is a correctness question for the oracle, not a guess to be made here.

## Not in scope

- **Mixing multiple stems.** Needs a decoder. A single stem is a worse preview than YARG's own
  and is still a preview.
- **Transcoding to one format.** Clients get the container the stem was in. YARG decodes all
  three already.
- **Generating previews at ingest.** On-demand plus a cache gets the same result without a
  migration or a rescan of 4,085 songs.

## Verified, and by what

A clip cut from the live library's own packages, served by the real endpoint, then decoded by
**VLC's libvorbis** — not this project's code:

```
GET /song/a56c61f1…/preview.ogg   ->  200, 557,663 bytes in 10 ms   (the stem is 47 MB)
ETag: "7cde27f2…-preview-30000-30000"
Content-Type: audio/ogg

independent Ogg walk:  130 pages, 0 CRC mismatches, BOS on first, EOS on last,
                       granules rebased 0..1331144, sequence contiguous, 2 header pages
VLC decode:            30.16 s, 44.1 kHz stereo 16-bit
audio content:         peak 0.0 dBFS, rms -12.4 dBFS, audible in the first AND last second
```

Four things in that list are there because each would have passed without the others:

- **the CRC was checked by a second implementation**, in Python, written against the spec rather
  than by reusing the Go code under test
- **the header packets were checked for spill.** If Vorbis's setup packet had run past the
  granule-zero pages, the clip would have lost its codebooks and been undecodable while passing
  every structural assertion. Page 1's last segment is 145, not 255, so the packet ends there and
  page 2 starts fresh.
- **it was decoded, not just parsed.** A cutter and a parser written in the same afternoon agree
  with each other about a malformed page.
- **the decoded audio was measured for silence.** Thirty seconds of silence decodes perfectly and
  is not a preview.

The clip is byte-identical across a rebuild and a handler rewrite, which is the determinism the
strong ETag depends on.

**And it has now met BASS — 2026-11-04.** This paragraph used to say the oracle had not had its
say, and that VLC was strong evidence rather than the final word. It has had its say.
`RemotePreviewProbe` calls `RemoteSongEntry.LoadPreviewAudio` — the production path, the one the
music library calls when a song is highlighted — against the live server, and hands what comes
back to the game's own decoder:

```
PROBE INFO: Fredrik Thordendal's Special Defects - "Solo" Niger Within
PROBE PASS: the song has no bytes on this disk, so this is the remote path
PROBE INFO: BASS decoded it in 619 ms, length 30.06s, 1 channel set(s)
PROBE PASS: the clip has real length, not zero
PROBE PASS: and it is about the preview window rather than the whole song
PROBE PASS: a song the server does not have yields no preview
```

619 ms covers the fetch **and** the decode, against a 47 MB stem that was never downloaded.

The length assertions are not padding: a decoder handed rubbish can still return an empty
stream, and that would satisfy "returned a mixer" perfectly well. The last line is the negative
control — without it, a loader that returned some cached or default audio for any hash would
pass everything above it.

**And Jay heard one, in the game, on a song this machine does not hold** — `playtest-2026-11-04`,
the same evening. That is a different claim from the probe's and it is the one that finishes the
feature: a probe can only say BASS accepted the bytes, not that a person highlighting a song in
the music library hears the right music at the right moment. Every link now has evidence of its
own — the cutter against real encoders' CRCs, the clip against VLC, the client against BASS, and
the whole of it against somebody's ears.

## What the implementation does that this document originally did not say

- **Candidates are tried in order, not picked once.** A test caught a song whose full mix could
  not be cut returning 500 while a usable guitar stem sat beside it in the same package. Each
  skip is logged with its reason, and when every candidate fails the request still fails.
- **The ETag is over the PACKAGE hash**, not the chosen stem's, because which candidate wins is
  decided after the tag has to be sent — and any stem changing changes the package hash anyway.
- **The disk cache is not built.** Clips are cut per request that the ETag does not satisfy. A
  performance gap, not a correctness one, and recorded here rather than left to be discovered
  from a slow endpoint.

## MP3, added 2026-11-04

### What the MP3 cutter actually fixed, and what this entry claimed first

**The justification originally written here was false, and it was written without being checked
against the running server.** It said 150 of the 4,085 songs carry only `.mp3` stems, answered
415, and played nothing. The first half is right — 150 songs, measured from the catalogue's own
`assets.stems`, and 0 songs carry a `.wav` stem, which is why no WAV cutter exists. The rest was
an inference, and production contradicts it:

```
200  ct=audio/ogg  len=482304  magic=ff fb 94 64   Motorhead - (We Are) The Road Crew
200  ct=audio/ogg  len=398735  magic=ff fb 90 64   Queens of the Stone Age - 3's & 7's
200  ct=audio/ogg  len=338549  magic=ff fb 90 64   Foo Fighters - All My Life
```

`ff fb` is an MP3 frame header. All 150 ship a ready-made `preview.mp3`, `previewCandidates`
puts a ready-made preview **first**, and `ReadyMade` deliberately skips the extension filter —
so the 415 never happened. **215 of 4,085 songs carry their own preview file** (150 mp3, 63 ogg,
2 opus), and the comment in `previewCandidates` asserting *"Nothing in the live library does -
0 of 4,085"* was stale; it was read as a current measurement.

So on this library the change delivers exactly one thing: those 150 responses stop being
labelled `audio/ogg` when they are mpeg. **`CutMP3` runs on zero songs here**, because no song
has an MP3 stem *and* no ready-made preview. It is correct, verified, and insurance for a
library that has one — which is a smaller claim than the one it shipped under.

The error was not in the measurement that was taken; it was in not taking the one that mattered.
A stem extension says what the archive holds, not what the endpoint answers, and the endpoint is
one HTTP request away.

### How it works

Same idea, different container: an MP3 is a row of self-contained frames, so a clip is a run of
whole frames copied out. Frame length comes from the header's bitrate and sample rate, which is
also why it has to be parsed per frame rather than assumed — VBR files change bitrate as they go.

Two things that are easy to get wrong and silent when you do:

- **MPEG2 and 2.5 use different constants.** 576 samples per frame rather than 1152, and `72`
  rather than `144` in the length formula. Using the MPEG1 numbers on an MPEG2 file yields frames
  of twice the right length and a clip of noise.
- **An ID3v2 tag's size field is syncsafe** — seven significant bits per byte. Read it as plain
  big-endian and you overshoot into the audio. The resync would recover, but by discarding real
  audio, so the clip would start late for no visible reason.

**Verified against a real encoder's file, and the first version of the check did not discriminate.**
The test walks the `.mp3` stem of a song pulled from the live server and asserts the frames *tile*
it:

```
guitar.mp3: 2,641,504 bytes, 6,320 frames, 100.0% covered, 0 bytes skipped
```

The control that matters: changing `144` to `143` in the length formula still covered **99.3%**
and **passed** the original 95% threshold, because resync recovers within a few bytes of each
frame. The assertion is now on *skipped bytes*, where correct is zero and the corrupted formula
skips 12,639:

```
corrupted: 99.5% covered, 12639 bytes skipped  -> FAIL
restored : 100.0% covered,     0 bytes skipped -> pass
```

That is the whole lesson of the entry: **coverage was a plausible metric that could not see the
defect**, and only putting the bug back revealed it.

## Silence, and the stem that was never the full mix — 2026-11-04

**Status:** diagnosed and measured. **The fix first written for it was wrong and has been
reverted** — see *"The threshold died on the bench"* below. The remedy is to stop choosing a
stem at all; that is unbuilt.

Sampling clip sizes across the library turned up three clips at 4,407 / 7,225 / 8,623 bytes
against a median of **814,901**. The first read was "the cutter is broken". It is not. Parsing the
7,272-byte one page by page:

```
8 pages, one serial, EOS set on the last, last granule 1,419,298 = 32.18 s at 44.1 kHz
```

A correct cut, the right length, structurally valid — of silence. Six audio pages of 537 bytes
carrying 261,120 samples each is about **1.9 kbit/s**.

### The cause, measured rather than reasoned

A throwaway probe cut **every** stem of that package at the same window, so the byte counts are
directly comparable without decoding anything:

```
=== Metallica - ...And Justice for All  [rb1dlc]
    window 156300 ms + 30000 ms, package 78,584,308 bytes, 7 cuttable stems
    drums_3.ogg   987552 bytes   263.3 kbit/s
    guitar.ogg    866874 bytes   231.2 kbit/s
    vocals.ogg    798391 bytes   212.9 kbit/s
    drums_2.ogg   795509 bytes   212.1 kbit/s
    rhythm.ogg    497266 bytes   132.6 kbit/s
    drums_1.ogg   438875 bytes   117.0 kbit/s
    song.ogg        7272 bytes     1.9 kbit/s   <-- what the endpoint served
```

### "Silence" was a word the byte count could not earn, so it was decoded

Jay asked the right question of the table above: *are we sure it's silence and not very low
volume?* **Bytes cannot tell those apart** — low-level audio compresses well too — so the clips
were decoded with VLC's libvorbis to s16 PCM and measured with numpy:

```
stem             secs  peak dBFS  RMS dBFS  nonzero %  max |s|
drums_1         30.12      -5.35    -23.10     99.774    17694
drums_2         30.02      -5.49    -28.46     99.912    17420
drums_3         30.03      -4.70    -29.11     99.932    19070
guitar          29.99      -6.41    -19.22     99.988    15657
rhythm          30.00      -7.33    -17.11     99.937    14092
song            32.16       -inf      -inf      0.000        0
vocals          30.05      -6.46    -22.83     99.898    15580
```

`song.ogg`'s clip is **all 2,836,512 samples equal to zero**: peak `-inf` dBFS, 0.000% nonzero.
Not quiet — empty. Vorbis still spends 1.9 kbit/s on it because page headers are not free.

**The lesson is about the instrument, not the stem.** Clip size is a sound proxy for *picking a
better candidate at request time*, which is all the production fix needs, and it is the only
thing available without a decoder. It is not evidence about amplitude, and "silent" was being
asserted on it. Amplitude needed a decoder, and a decoder was available — out of band, in a
probe, which is exactly where ADR-009 permits one.

### Back to the mechanism

`stemPreference` picks `song` — **the only silent stem of the seven**, at 1/136th the bytes of the
loudest. In a Rock Band-derived package `song.ogg` is the *"everything else"* backing track, and
once every instrument has its own stem there is nothing left in it. The preference is still right
for the other 94.5% of the library, where `song.ogg` **is** the full mix.

### The threshold died on the bench, and the measurement that killed it is the point

The first remedy was a decoder-free audibility test: reject a clip whose own bit rate is under
`minAudibleKbps` (16) and try the next candidate, keeping the loudest rejected clip as a
fallback. It was implemented, it had a test that failed before it and passed after, the suite was
green under `-race`, and **it was wrong.**

Two measurements took it apart.

**First, the byte-space "gap" that justified 16 was duration, not loudness.** Taking the real
clip length from the last Ogg granule instead of assuming 30 s:

```
clips under 10 s : 556 of 1,022     <- GH-derived songs set preview_end_time ~5 s after start
clips 25-35 s    : 310
```

Over half the small clips are small because the *window* is short. In rate space the tidy gap
between 20 KB and 50 KB vanishes.

**Second, decoding a sample stratified by true bit rate showed the bands overlap:**

```
  kbit/s   secs  peak dB   RMS dB  nonzero%  stems  song
    1.84  31.30     -inf     -inf     0.000      8  Whitesnake - Still of the Night
    6.74   5.37     -inf     -inf     0.000      9  Blue Oyster Cult - Godzilla
    2.94  30.63   -30.92   -68.39     0.585      8  Bon Jovi - Lay Your Hands on Me
   11.14  30.02    -7.72   -33.92     4.194      7  Motorhead - (We Are) The Road Crew '08
   13.35  33.43    -8.87   -36.67     6.901      8  The Offspring - Pretty Fly (For a White Guy)
  248.67  30.02    -7.23   -26.38    99.917      8  Billy Currington - That's How Country Boys Roll

highest ALL-ZERO   : 6.74 kbit/s
lowest WITH SIGNAL : 2.94 kbit/s
```

Seven of 32 sampled clips below the floor carry **real signal**, two peaking near -8 dBFS. They
are **sparse, not quiet** - around 4% of samples nonzero, loud hits separated by silence - and
sparse audio compresses like silence while sounding nothing like it. The floor would have
discarded audible previews, and no number rescues it: lower it and the all-zero 6.7 kbit/s GH
clips get served, raise it and The Offspring gets dropped.

**So clip size answers "which of these two is louder" and not "is this audible".** It was sound
for the probe that found the mechanism, where both clips came from one package and one window,
and unsound the moment it was asked to judge a clip on its own. The same number, honest in one
use and misleading in the other.

### The real remedy: stop choosing a stem

Jay's question when shown the client code - *why don't we just use their preview instead of
making our own?* - is the answer. `stemPreference` is **this server's invention**. Upstream has
no such thing: `LoadPreviewAudio` prefers a shipped preview file and otherwise calls
`CreateAudioMixer`, which adds **every** stem to one mixer. The game never picks a stem, so a
server that picks one is guessing at a question the client already answers.

And the client can still answer it: `GlobalAudioHandler.CreateMixer` plus one
`mixer.AddChannel(stream, stemEnum)` per stem is the same path `SngEntry` uses locally, fed HTTP
streams instead of `.sng` streams. So the server cuts **every** stem at the window - code that
already exists and needs no decoder - and the client mixes them exactly as it does for a local
song. No preference order, no audibility threshold, no heuristic to calibrate, and parity with
the game by construction rather than by approximation.

**What it costs, measured:** the median clip is 713 KB, so a 7-stem song is about 5 MB and an
8-stem song 5.7 MB against today's single 713 KB. At the 3.3 MB/s measured over the tailnet that
is roughly 1.7 s per preview. Only multi-stem packages pay it - the 3,861 songs whose `song.ogg`
is the whole mix stay one clip - and the 215 songs shipping their own preview stay one file. It
also makes the absent disk cache matter considerably more.

**Unbuilt, and it spans both repos**: a per-stem endpoint on the server and a mixer-building
`LoadPreviewAudio` in the fork.

### While it existed, the threshold rerouted two tests and they stayed green

Kept even though that code is gone, because the trap is not specific to it. `syntheticVorbis`
emitted 256 bytes a second — 2 kbit/s, *below the floor that had just been added*. So
`TestPreviewCutsAClipFromTheStem` and `TestPreviewFallsBackWhenThereIsNoFullMix` both kept
passing while going through the new silent-stem fallback instead of the accept path they are
named for. Green, and no longer measuring what they claim. It was noticed by asking which branch
they now take, not by anything going red.

**A threshold added in one place can reroute tests somewhere else.** When you add one, check
which branch each passing test now takes — it is not enough that they still pass.

### What a one-stem preview approximates, and what it can never be

Worth stating plainly, because the fix above could be read as making the server agree with the
game. It does not. `SongEntry.UnpackedIni.LoadPreviewAudio` in YARG.Core prefers a shipped
preview file and otherwise calls `LoadAudio(speed, 0, enableCensoring, SongStem.Crowd)` — which
adds **every supported stem except Crowd** to the mixer. The game's preview is the full
multi-stem mix.

This server serves ONE stem, because mixing needs a decoder and that is the decision this ADR
exists to record. For the 3,861 songs whose `song.ogg` is the whole mix the two agree. For a
multi-stem package they cannot: the game plays guitar, drums, bass, vocals and rhythm together
and the server can only pick one of them.

So the fix does not achieve parity — it moves the server from *the one silent stem* to *the
loudest single stem*, which is closer to what the game plays and audibly better than nothing.
Anything claiming preview parity with the client for multi-stem packages is wrong.

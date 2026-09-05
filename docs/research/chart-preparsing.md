# YARG Song-Package Preparser — Instrument & Difficulty Detection Spec

## Executive summary

A preparser that only needs "which parts exist, at which difficulties" is a **track-name + note-number histogram** problem. It never needs tempo, timing, sustains, HOPO inference, or chord grouping.

Four facts drive the whole design:

1. **`.mid` tracks are identified by the track-name meta event, which is the first event of each track**, and *"Since any name can be used for a track, unknown tracks should be ignored."* — [GuitarGame_ChartFormats, .mid Format Overview § Tracks](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Format-Overview/)
2. **Difficulties are note-number blocks within one track** — Expert 96–107, Hard 84–95, Medium 72–83, Easy 60–71 — but *"Some instruments don't adhere to these ranges though, and may use more ranges, different ranges, or even dedicated tracks for each difficulty."* — same doc, § Track Difficulties. Pro Guitar, Pro Keys, 6-Fret, Vocals and Elite Drums all deviate.
3. **The difficulty ranges contain non-playable notes** (force-strum/HOPO, opens, 5-lane green, Expert+ kick, GH1 star-power/face-off markers). Counting "any note in the octave" is the single largest false-positive source.
4. **`.chart` is trivially easier**: one section per (difficulty × instrument), `[<Difficulty><Instrument>]`. — [.chart Format Overview § Section Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Format-Overview/)

**Source access note:** `wiki.yarg.in` returned 403 to every direct fetch (Miraheze anti-bot challenge; the MediaWiki `api.php`, `rest.php` and `action=raw` endpoints were all challenged too). I read `Help:Charting`, `Notes.mid` and `Notes.chart` **via the Wayback Machine** (snapshots 2026-08-23, 2026-08-23 and 2025-09-18 respectively) and note that below wherever I cite them. `wiki.yarg.in/wiki/Instruments` and `Elite_Drums` I could not retrieve (the latter is a redlink — the page does not exist).

---

# A. `.mid` (notes.mid)

## A.1 Track names, verbatim, and what they map to

### YARG's own list

The YARG wiki's `Notes.mid` page gives YARG's recognised tracks. Note it is explicitly *"a stub"* and is **incomplete** — the same page's opening line says the format supports 6-Fret Guitar and Pro Guitar, yet no `PART GUITAR GHL` or `PART REAL_GUITAR` row appears in its table. — [YARG Wiki, Notes.mid § Tracks](https://wiki.yarg.in/wiki/Notes.mid), read via [Wayback 20260823060309](https://web.archive.org/web/20260823060309/https://wiki.yarg.in/wiki/Notes.mid)

| Track name (verbatim) | YARG's stated purpose |
|---|---|
| `PART GUITAR` | 5-Fret Lead Guitar + guitarist animation cues |
| `PART GUITAR COOP` | 5-Fret **Melody** Guitar (YARG's name for co-op) |
| `PART RHYTHM` | 5-Fret Rhythm Guitar |
| `PART BASS` | 5-Fret Bass + bassist animation cues |
| `PART KEYS` | 5-Lane Keys + keys animation cues |
| `PART DRUMS` | 4-Lane, 5-Lane **and** Pro Drums + drummer animation cues |
| `PART ELITE_DRUMS` | Elite Drums |
| `PART REAL_KEYS_X` / `_H` / `_M` / `_E` | Pro Keys Expert / Hard / Medium / Easy |
| `PART KEYS_ANIM_RH` / `PART KEYS_ANIM_LH` | Keys hand **animation only — not playable** |
| `PART VOCALS` | Solo Vocals (notes + lyrics) + singer animation cues |
| `HARM1` / `HARM2` / `HARM3` | Harmony lines 1 (blue), 2 (orange), 3 (yellow) |
| `PART HARM1` / `PART HARM2` / `PART HARM3` | *"alternative names used by The Beatles: Rock Band. The recommended track names are HARM1, HARM2, HARM3"* |
| `EVENTS` | Global events / practice sections |
| `BEAT` | Beat lines |
| `VENUE` | Venue events |

### Full name list from the format documentation

**5-fret family** — [GG_ChartFormats, .mid → 5-Fret Guitar § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/5-Fret-Guitar/):

| Name | Instrument |
|---|---|
| `T1 GEMS` | Guitar Hero 1 Lead Guitar (legacy). *"While this track is uncommon and has some slightly different notes, the important parts are the same… It is safe to support the same way as the other tracks."* |
| `PART GUITAR` | Lead Guitar |
| `PART GUITAR COOP` | Co-op Guitar |
| `PART BASS` | Bass Guitar |
| `PART RHYTHM` | Rhythm Guitar |
| `PART KEYS` | 5-Lane Keys — *"While this track is not named for a guitar-type instrument, the game it comes from allows for playing it as if it were one."* |

**6-fret (GHL)** — [.mid → 6-Fret Guitar § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/6-Fret-Guitar/): `PART GUITAR GHL`, `PART BASS GHL`, `PART RHYTHM GHL`, `PART GUITAR COOP GHL`, `PART KEYS GHL`.

**Drums** — [.mid → Drums § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Drums/):

| Name | Meaning |
|---|---|
| `PART DRUMS` | Standard 4-Lane, 4-Lane Pro, **and** 5-Lane — all three share this one track |
| `PART DRUM` | *"Alternate track to PART DRUMS that FoFiX supports"* (legacy spelling) |
| `PART DRUMS_2X` | RBN 2x-kick chart. *"This shouldn't normally be present"* |
| `PART REAL_DRUMS_PS` | Phase Shift Real Drums. *"This track has the same notes as the standard Drums track"*, differing only in SysEx modifiers — [Phase Shift → Real Drums](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Phase%20Shift/MIDI-Tracks/Real-Drums/) |

> **Critical:** there is **no separate track name for Pro Drums or 5-lane drums.** *"These all unfortunately share the same track, and there are no specifications to allow explicitly distinguishing between different types of drum tracks by track name, so the track type must be determined through heuristics."* — same doc, § Drums Track Types.

**Elite Drums** — `PART ELITE_DRUMS`. *"Unlike 4L, 4L Pro, and 5L, this format lives in a separate MIDI track, `PART ELITE_DRUMS`."* — [Elite Drums MIDI/Engine Specification § Abstract](https://docs.google.com/document/d/1wPIHbe2Z2qSELyCFaVcHXO0VqG9rLSBRUZH54HWHLP4/view) (linked as "Elite Drums MIDI/Engine Specification" from YARG Wiki Help:Charting § Documentation)

**Pro Guitar / Pro Bass** — [.mid → Pro Guitar and Bass § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Pro-Guitar/): `PART REAL_GUITAR` (17-fret), `PART REAL_GUITAR_22` (22-fret), `PART REAL_BASS` (17-fret), `PART REAL_BASS_22` (22-fret), `PART REAL_GUITAR_BONUS` (*"Unknown Pro Guitar track that EoF has available"*). *"There are separate tracks for 17-fret and 22-fret due to different Pro Guitar controller models having a different amount of available frets."*

**Pro Keys** — [.mid → Pro Keys § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Pro-Keys/): `PART REAL_KEYS_X`, `PART REAL_KEYS_H`, `PART REAL_KEYS_M`, `PART REAL_KEYS_E`. Animation-only companions `PART KEYS_ANIM_LH` / `PART KEYS_ANIM_RH` — [Rock Band → Pro Keys](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Rock-Band/MIDI-Tracks/Pro-Keys/).

> **Naming discrepancy — flag it.** The C3/RBN minimum-requirements table spells the animation tracks **`PART REAL_KEYS_ANIM_RH`** and **`PART REAL_KEYS_ANIM_LH`** — [RBN/C3, Mix and MIDI Setup § MIDI file minimum requirements](http://docs.c3universe.com/rbndocs/index.php?title=Mix_and_MIDI_Setup). TheNathannator and the YARG wiki both spell them `PART KEYS_ANIM_RH/LH`. Reject both spellings as non-playable.

**Phase Shift Real Keys** — `PART REAL_KEYS_PS_X`, `PART REAL_KEYS_PS_M`, `PART REAL_KEYS_PS_H`, `PART REAL_KEYS_PS_E` — [Phase Shift → Real Keys § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Phase%20Shift/MIDI-Tracks/Real-Keys/). *(That page labels `_M` as "Hard" and `_H` as "Medium"; this is an apparent typo in the source, flagged as such — treat `_H`=Hard, `_M`=Medium by analogy with Pro Keys.)* Notes span **48–108**, not 48–72.

**Vocals** — [.mid → Vocals and Harmonies § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Vocals/): `PART VOCALS`; `HARM1`, `HARM2`, `HARM3`; alternates `PART HARM1`, `PART HARM2`, `PART HARM3`.

**Dance** — `PART DANCE` — [Phase Shift → Dance](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Phase%20Shift/MIDI-Tracks/Dance/). Non-standard difficulty naming (see A.2).

**Non-instrument tracks to skip:** `EVENTS`, `BEAT`, `VENUE`, `ANIM` (GH1 guitarist left-hand track — [GH1/2 → Guitar](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Guitar-Hero-1-and-2/MIDI-Tracks/Guitar/)).

---

## A.2 Note-number ranges per difficulty

### 5-fret guitar family (`PART GUITAR`, `COOP`, `BASS`, `RHYTHM`, `KEYS`, `T1 GEMS`)
[.mid → 5-Fret Guitar § Track Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/5-Fret-Guitar/)

| Diff | Full block | Open | Lanes (playable) | Force HOPO | Force strum |
|---|---|---|---|---|---|
| Easy | 59–66 | 59 | **60–64** | 65 | 66 |
| Medium | 71–78 | 71 | **72–76** | 77 | 78 |
| Hard | 83–90 | 83 | **84–88** | 89 | 90 |
| Expert | 95–102 | 95 | **96–100** | 101 | 102 |

Independently confirmed by RBN/C3: *"Expert Gems are MIDI notes 96 (C6) to 100 (E6)"*, *"Hard Gems are MIDI notes 84 (C5) to 88 (E5)"*, *"Medium Gems are MIDI notes 72 (C4) to 75 (D#4)"*, *"Easy Gems are MIDI notes 60 (C3) to 62 (D3)"* — [RBN/C3, Guitar and Bass Authoring § Authoring Rules by Difficulty](http://docs.c3universe.com/rbndocs/index.php?title=Guitar_and_Bass_Authoring). (C3's narrower Medium/Easy top ends reflect *authoring guidance* on which lanes to use, not a format restriction; the format ceiling is 76 / 64.)

**Pan-difficulty markers (outside the blocks):** 103 solo / GH1-2 Star Power, 104 tap, 105 P1-versus, 106 P2-versus, 116 Star Power, 120–124 Big Rock Ending 5→1, 126 tremolo lane, 127 trill lane.

### 6-fret guitar (GHL) — **does NOT follow the 4-block layout**
[.mid → 6-Fret Guitar § Track Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/6-Fret-Guitar/)

| Diff | Full block | Open | Lanes (playable: W1,W2,W3,B1,B2,B3) | HOPO | Strum |
|---|---|---|---|---|---|
| Easy | 58–66 | 58 | **59–64** | 65 | 66 |
| Medium | 70–78 | 70 | **71–76** | 77 | 78 |
| Hard | 82–90 | 82 | **83–88** | 89 | 90 |
| Expert | 94–102 | 94 | **95–100** | 101 | 102 |

The blocks are **9 notes wide and start one semitone lower** than 5-fret, because six lanes plus an open are needed. Markers: 103 solo, 104 tap, 116 Star Power only. **No BRE, no versus, no lane markers.**

### Drums (`PART DRUMS`, `PART DRUM`, `PART DRUMS_2X`, `PART REAL_DRUMS_PS`)
[.mid → Drums § Track Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Drums/)

| Diff | Full block | Kick | 4-lane pads (R,Y,B,G) | 5-lane Green | Expert+ kick |
|---|---|---|---|---|---|
| Easy | 60–65 | 60 | **61–64** | 65 | — |
| Medium | 72–77 | 72 | **73–76** | 77 | — |
| Hard | 84–89 | 84 | **85–88** | 89 | — |
| Expert | **95**–101 | 96 | **97–100** | 101 | **95** |

**Expert is 7 notes wide, not 6** — note 95 (Expert+ / 2x kick) sits *below* the octave boundary. Confirmed by C3: *"Expert Gems are MIDI notes 96 (C6) to 100 (E6)"*, *"Hard … 84 (C5) to 88 (E5)"*, *"Medium … 72 (C4) to 76 (E4)"*, *"Easy … 60 (C3) – 64 (E3)"* — [RBN/C3, Drum Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Drum_Authoring). C3 omits the 5-lane green and Expert+ kick because Rock Band does not support them — [Rock Band → Drums § Not supported](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Rock-Band/MIDI-Tracks/Drums/).

**Markers:** 103 solo, 105/106 versus, 109 flam, **110 yellow-tom / 111 blue-tom / 112 green-tom**, 116 Star Power, 120–124 fill/BRE, 126 1-lane roll, 127 2-lane roll.

**Drum type is a heuristic, not a name:**
- *"If tom markers are present, it's a 4-lane Pro track."* (notes 110–112)
- *"If the 5-lane green note is present, or if notes are sustained, it's a 5-lane track."* (notes 65/77/89/101)
- *"If neither 5-lane nor Pro are detected, fall back to standard 4-lane."*
- *"If both are detected, it may be preferable to prioritize Pro over 5-lane."*
- `song.ini` `pro_drums` / `five_lane_drums` override. *"Both being set to true is an invalid state."*
— [.mid → Drums § Determining Track Type](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Drums/)

### Elite Drums (`PART ELITE_DRUMS`) — **two octaves per difficulty**
*"Unlike other formats, each difficulty spans two octaves rather than one; the lower octave contains the notes themselves, while the upper octave contains modifiers for relatively uncommon features."* — [Elite Drums MIDI/Engine Specification § MIDI Structure](https://docs.google.com/document/d/1wPIHbe2Z2qSELyCFaVcHXO0VqG9rLSBRUZH54HWHLP4/view)

| Purpose | Expert | Hard | Medium | Easy |
|---|---|---|---|---|
| Pedal Down (note **and** modifier) | 72 | 48 | 24 | 0 |
| 2x Kick | 73 | 49 | 25 | 1 |
| 1x Kick | 74 | 50 | 26 | 2 |
| Snare | 75 | 51 | 27 | 3 |
| Hi-Hat (yellow cymbal) | 76 | 52 | 28 | 4 |
| Left Crash (purple) | 77 | 53 | 29 | 5 |
| Tom 1 | 78 | 54 | 30 | 6 |
| Tom 2 | 79 | 55 | 31 | 7 |
| Tom 3 | 80 | 56 | 32 | 8 |
| Ride (blue) | 81 | 57 | 33 | 9 |
| Right Crash (green) | 82 | 58 | 34 | 10 |
| **Flam marker** (modifier) | 87 | 63 | 39 | 15 |
| **Indifferent hat marker** (modifier) | 88 | 64 | 40 | 16 |
| **Disco flip marker** (modifier) | 90 | 66 | 42 | 18 |

So: **gems Expert 72–82, Hard 48–58, Medium 24–34, Easy 0–10**; modifier octaves 84–95 / 60–71 / 36–47 / 12–23.

**Pan-difficulty markers:** 103 solo, **104 Overdrive/Star Power/Unison** (note: *not* 116 — differs from every other track type), 105/106 reserved for battle phrases, 108 stomp/splash roll, 109 unused, 110 kick roll, 111 snare roll, 112 hi-hat roll, 113 left-crash roll, 114 tom-1 roll, 115 tom-2 roll, 116 tom-3 roll, 117 ride roll, 118 right-crash roll, 120 Activation/BRE. The spec also *recommends but does not require* MIDI note 125 as a kick-lane marker on ordinary 4L/5L drums.

### Pro Guitar / Pro Bass — **does NOT follow the standard blocks**
[.mid → Pro Guitar and Bass § Track Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Pro-Guitar/)

| Diff | Full block | **Strings (playable) 6 notes** | HOPO | Slide | Arpeggio | Str. emphasis | "Unknown" |
|---|---|---|---|---|---|---|---|
| Easy | 24–34 | **24–29** | 30 | 31 | 32 | — | 34 (unobserved) |
| Medium | 48–58 | **48–53** | 54 | 55 | 56 | — | 58 |
| Hard | 72–82 | **72–77** | 78 | 79 | 80 | 81 | 82 |
| Expert | 96–106 | **96–101** | 102 | 103 | 104 | 105 | 106 |

Blocks are **11 notes wide** and Easy/Medium sit **two and one octaves below** the normal positions. Easy at 24–34 and Medium at 48–58 mean a Pro Guitar track's Easy block sits where nothing else lives, but its **Hard block (72–82) is numerically identical to a 5-fret track's Medium block** — another reason detection must be gated on track name.

Also on the track: 4–15 root-note markers, 16 slash chord, 17 hide chord name, 18 sharp-flat switch, 107 force full chord numbering, 108 left-hand position, 115 solo, 116 Star Power, 120–125 BRE 6→1, 126 tremolo, 127 trill.

Fret number is carried in **velocity**, *"starting from velocity 100 and going to velocity 117 for 17-fret or 122 for 22-fret"* — irrelevant to presence detection, but it means a preparser must not assume velocity ≤127 encodes dynamics here.

### Pro Keys — separate track per difficulty (see A.3)
### Vocals / Harmonies — no difficulty blocks (see A.3)

### Dance (`PART DANCE`) — non-standard difficulty **names**
[Phase Shift → Dance](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Phase%20Shift/MIDI-Tracks/Dance/): the top tier is called **"Challenge"**, not Expert — Challenge 96–99, Hard 84–87, Medium 72–75, (Easy 60–63). Four lanes only. Markers 103 solo, 116 Star Power.

---

## A.3 Pro Keys, Vocals, Harmonies

### Pro Keys — one MIDI track per difficulty, no note-block subdivision

*"`PART REAL_KEYS_X` - Pro Keys Expert / `PART REAL_KEYS_H` - Pro Keys Hard / `PART REAL_KEYS_M` - Pro Keys Medium / `PART REAL_KEYS_E` - Pro Keys Easy"* — [.mid → Pro Keys § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Pro-Keys/)

All four tracks use **the same playable note range: 48 (C1) – 72 (C3)**, 25 semitones. Confirmed independently: *"The Expert Pro Keys part should be authored between C2 (48) and C4 (72) on the PART REAL_KEYS_X track"* — and the identical sentence for `_H`, `_M`, `_E` — [RBN/C3, Pro Keyboard Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Pro_Keyboard_Authoring). (C3 uses a different octave-naming convention; the MIDI numbers agree.)

Non-note events on the same track: **range shifts on notes 0, 2, 4, 5, 7, 9** (below the range), 115 solo, 116 Star Power, 120 BRE, 126 glissando, 127 trill.

> Presence rule: `PART REAL_KEYS_X` exists **and** contains ≥1 Note-On in 48–72 ⇒ Pro Keys Expert exists. Repeat per track. Difficulty detection for Pro Keys is entirely track-name-driven.
>
> **Trap:** `PART KEYS_ANIM_LH` / `PART KEYS_ANIM_RH` (a.k.a. `PART REAL_KEYS_ANIM_*`) use **the identical 48–72 note range** — [Rock Band → Pro Keys § Hand Animation Track Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Rock-Band/MIDI-Tracks/Pro-Keys/). Only the name distinguishes them. Never pattern-match on `REAL_KEYS` as a substring.

Also: *"Only Star Power marked on the Expert track will work"* and *"Glissandos only work in Expert"* — presence of 116 on `_H`/`_M`/`_E` is inert and must not count as evidence.

### Vocals — how presence is signalled

Vocals has **no difficulty blocks at all.** The documentation defines no Easy/Medium/Hard/Expert note ranges for `PART VOCALS` or the HARM tracks — the whole track is a single part. **NOT DOCUMENTED:** any per-difficulty subdivision of a vocals track.

What the track contains — [.mid → Vocals and Harmonies § Track Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Vocals/):

| Range | Meaning |
|---|---|
| **36–84** | Pitch notes, C2 (lowest) to C6 (highest) |
| 96 | Displayed percussion |
| 97 | Not-displayed percussion |
| 105 | Player 1 lyrics **phrase marker** |
| 106 | Player 2 lyrics phrase marker |
| 116 | Star Power |
| 0 | Range shift |
| 1 | Lyric shift |

Lyrics are meta events: *"Lyrics are stored as meta events (usually text or lyric), usually paired up with notes in the 36 to 84 range to determine pitch. **Some charts don't have notes for pitch, these charts are lyrics-only.**"* — same section.

So the correct presence test for vocals is a **disjunction**:

> Vocals exists ⇔ track named `PART VOCALS` contains (≥1 Note-On in 36–84) **OR** (≥1 lyric/text meta event that is not a bracketed `[…]` directive).

RBN's minimum requirement is stricter and worth knowing: `PART VOCALS` requires *"1 lyric (aligned with note tube)"* and *"1 Note tube, 1 Phrase Marker"* — [RBN/C3, Mix and MIDI Setup § MIDI file minimum requirements](http://docs.c3universe.com/rbndocs/index.php?title=Mix_and_MIDI_Setup). A phrase marker (105) alone with no pitch notes and no lyrics is not a vocals part.

Phrase markers are on 105: *"Phrase markers are placed on A6 (105)"*, and Overdrive is *"marked by copying the Phrase marker up to G#7 (116)"* — [RBN/C3, Vocal Authoring § Phrases](http://docs.c3universe.com/rbndocs/index.php?title=Vocal_Authoring). Percussion is *"placed on MIDI note C6 (96)"* with non-playable percussion on *"C#6 (97)"* — same doc, § Percussion Sections.

### Harmonies — how many parts

*"`HARM1`, `HARM2`, `HARM3` - Harmonies track 1, 2, and 3 respectively"*, alternates `PART HARM1/2/3`. — [.mid → Vocals § Track Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/Vocals/)

Counting rules that matter:

- *"For harmonies, the HARM1 phrase is used for all 3 harmony tracks. The HARM2 phrase is used to mark when harmony 2/3 lyrics shift in static vocals, and it must cover all notes in HARM3. **Phrase markers are not used in HARM3.**"* — same doc, § Lyrics.
- C3: *"`HARM1`: This is the lead vocal part… `HARM2`: … used for 2 part harmony songs… `HARM3`: This track is only used in 3 part harmony songs."* — [RBN/C3, Harmony Authoring § Track Overview](http://docs.c3universe.com/rbndocs/index.php?title=Harmony_Authoring)
- The C3 minimum-requirements table lists `HARM2` and `HARM3` as requiring **"None"** text events and **"None"** MIDI notes, while `HARM1` requires *"1 lyric"* + *"1 Note tube, 1 Phrase Marker"* — [Mix and MIDI Setup](http://docs.c3universe.com/rbndocs/index.php?title=Mix_and_MIDI_Setup).

> **Harmony count = the number of HARM*n* tracks that contain ≥1 Note-On in 36–84 or ≥1 lyric event.** Do **not** count phrase markers, because HARM3 is documented to have none, and do not require them, because HARM2/HARM3 are documented as having no required content. Report the count as 0 / 2 / 3 — a lone HARM1 with nothing else is not "harmonies", it is the lead line. **NOT DOCUMENTED:** whether a chart may legally ship HARM1 + HARM3 without HARM2, or HARM1 alone.

`song.ini` carries `diff_vocals_harm` — *"Difficulty of the Harmonies track"* — [song.ini Standard Tags](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/song-ini/Standard-Tags/). It is a difficulty rating, not a part count.

---

## A.4 Minimum evidence for "this difficulty exists" — the false-positive list

**Presence of *any* note in a difficulty's range is NOT sufficient.** Every family has non-playable note numbers inside the block. Use an explicit **allow-list of playable note numbers**, never a range test.

### Playable-note allow-lists (the only numbers that should count)

| Family | Easy | Medium | Hard | Expert |
|---|---|---|---|---|
| 5-fret | 60–64 (+59 only if `[ENHANCED_OPENS]`) | 72–76 (+71 gated) | 84–88 (+83 gated) | 96–100 (+95 gated) |
| 6-fret | 58–64 | 70–76 | 82–88 | 94–100 |
| Drums | 60–64 (+65 if 5-lane) | 72–76 (+77) | 84–88 (+89) | 96–100 (+101), 95 only if 2x-kick counted |
| Elite Drums | 0–10 | 24–34 | 48–58 | 72–82 |
| Pro Guitar/Bass | 24–29 | 48–53 | 72–77 | 96–101 |
| Pro Keys | *(per-track)* 48–72 | 48–72 | 48–72 | 48–72 |
| Vocals/Harm | *(no difficulties)* 36–84 or lyric events | — | — | — |

### Explicitly excluded notes that live *inside* difficulty ranges

**5-fret / 6-fret — force markers.** 65/66, 77/78, 89/90, 101/102 (5-fret) and the same numbers on 6-fret are *"force HOPO"* / *"force strum"*. *"Notes can have their natural state overridden using the force strum/HOPO markers"* — they modify notes, they are not notes. A difficulty containing **only** 101 and 102 has no Expert chart. — [.mid → 5-Fret Guitar](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Tracks/5-Fret-Guitar/)

**5-fret note 59 — the big one.** *"The note-based open notes are disabled by default since note 59 is normally part of left hand animations in GH2/RB tracks. An `[ENHANCED_OPENS]` text event (with or without brackets) needs to be placed at the start of the track to enable them."* — same doc. Rock Band's left-hand animation notes occupy **40–59**, with 59 = *"Left hand position 20"* — [Rock Band → Guitar § Animation](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Rock-Band/MIDI-Tracks/Guitar/). **A `PART GUITAR` with animation data and no Easy chart will contain note 59.** Counting 59 unconditionally invents an Easy difficulty on a large fraction of RB-derived charts.
> Rule: on 5-fret tracks, count 59 / 71 / 83 / 95 **only if** the track carries an `ENHANCED_OPENS` text event, or a Phase Shift open-note SysEx (`'P','S',0x00, 0x00, <diff>, 0x01, <0|1>`) covers them. On 6-fret, opens need no gate: *"The `[ENHANCED_OPENS]` text event is not required for 6-fret note-based open notes."*

**GH1 `T1 GEMS` — per-difficulty markers inside the octaves.** This track puts Star Power and face-off markers *inside* each difficulty block: Easy 67 SP / 69 P1 / 70 P2; Medium 79 / 81 / 82; Hard 91 / 93 / 94; Expert 103 / 105 / 106. — [GH1/2 → Guitar § GH1 Guitar Notes](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Guitar-Hero-1-and-2/MIDI-Tracks/Guitar/). A naive Easy = 60–71 test fires on a face-off marker with zero playable notes. The 60–64 allow-list handles this automatically.

**Drums — tom markers, flam, animation.** Tom markers 110/111/112 and flam 109 are above the ranges and safe. But **Rock Band drum animation notes occupy 24–51** (24 kick-right-foot … 51 floor-tom-right-hand) — [Rock Band → Drums § Animation](https://thenathannator.github.io/GuitarGame_ChartFormats/Implementation-Specific/Rock-Band/MIDI-Tracks/Drums/). Those do **not** overlap the 4-lane difficulty blocks (60–65 upward), so they are harmless for standard drums — **but they sit squarely inside Elite Drums' Hard (48–58) and Medium (24–34) blocks.** This is only a hazard if a preparser applies the wrong note map to a track; another argument for name-keyed dispatch.

**Drums note 95.** Expert+ / 2x kick. *"2x Kick notes should be opt-in."* Do **not** treat note 95 alone as evidence of an Expert drum chart — a `PART DRUMS_2X` companion track can legitimately carry little else.

**Elite Drums — modifier octaves.** 87/88/90 (Expert), 63/64/66 (Hard), 39/40/42 (Medium), 15/16/18 (Easy) are *"modifiers and fringe behaviors"* — disco flip, indifferent hat, flam. The disco-flip marker in particular *"has no direct effect on ED and is only for non-Pro 4L downcharting purposes"*, so it can exist on a difficulty with no gems. Exclude all three modifier numbers. **Also exclude Pedal Down alone** if you want strictness — the spec notes it *"serves a dual purpose as a note and modifier"*, and at velocity 1 it *"does not generate any pedal gem"*.

**Pro Guitar — five marker numbers per difficulty.** Expert 102 (HOPO), 103 (slide), 104 (arpeggio), 105 (string emphasis), 106 (unknown); and the analogous 78–82, 54–58, 30–34. Plus **root-note markers 4–18**, which are *"required for chords"* but are chord-name metadata, not playable strings. A Pro Guitar track can carry a full run of root markers with no notes in 24–29.

**Pro Keys — range shifts on 0/2/4/5/7/9.** *"One of these shifts should be marked at the very beginning of the chart as the starting range"* and *"each difficulty MUST have one range marker (lane shift) at the beginning of the song before the keys come in"* ([RBN/C3, Pro Keyboard Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Pro_Keyboard_Authoring)). So **every Pro Keys track — including empty ones — is expected to contain note 0, 2, 4, 5, 7 or 9.** Presence must be tested strictly inside 48–72.

**Vocals — 96/97 percussion, 105/106 phrases, 116 SP.** 96 and 97 sit above the 36–84 pitch range and are percussion, not sung notes. Whether displayed percussion alone constitutes a vocals part is **NOT DOCUMENTED**; RBN's minimum requires a note tube *and* a lyric, so treat percussion-only as not-a-part.

**Star Power (116) and solo (103/115) are never evidence.** They are pan-difficulty phrases on every family. So are BRE/fill (120–125), roll/trill/tremolo lanes (126/127, and Elite Drums 108–118), tap (104), and versus phrases (105/106).

**Velocity is not a difficulty signal, except once.** *"Trill and tremolo lanes… only apply to Expert unless they are marked at a velocity between 50-41, in which case it applies to Hard as well"* — this affects lane semantics, never note presence. Ignore velocity entirely for presence detection **except** on Pro Guitar, where velocity carries fret number, and Elite Drums, where Pedal Down velocity 1 means "no gem".

---

# B. `.chart` (notes.chart)

## B.1 Section headers — the full difficulty × instrument matrix

*"Typically, each difficulty of every instrument is its own section, with the standard nomenclature of these sections' names being the name of the difficulty followed by a code name for the instrument: `[<Difficulty><Instrument>]`. For example, Hard on Drums is written as `[HardDrums]`."* — [.chart Format Overview § Section Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Format-Overview/)

FireFox's original spec agrees: *"Header Tag = Difficulty + Instrument"* with difficulties *Easy, Medium, Hard, Expert* — [Chart File Format Specifications by FireFox § Tracks → Difficulties/Instruments](https://docs.google.com/document/d/1v2v0U-9HQ5qHeccpExDOLJ5CMPZZ3QytPmAG5WF0Kzs/view) (linked from YARG Wiki Help:Charting § Documentation as *"Specification of the notes.chart file format written by FireFox2000000"*).

**Difficulty prefixes (exactly four):** `Easy`, `Medium`, `Hard`, `Expert`.

**Instrument suffixes:**

| Suffix | Maps to | Source |
|---|---|---|
| `Single` | 5-fret Lead Guitar | [.chart → 5-Fret Guitar § Section Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/5-Fret-Guitar/); FireFox |
| `DoubleGuitar` | 5-fret Co-op Guitar | same |
| `DoubleBass` | 5-fret Bass Guitar | same |
| `DoubleRhythm` | 5-fret Rhythm Guitar | same |
| `Keyboard` | 5-lane Keys *("the game it comes from allows for playing it as if it were [a guitar]")* | same |
| `SingleBass` | *"Legacy bass track present in some old charts; exact distinction unclear. Used for GH3/GHTCP; otherwise largely unsupported."* FireFox: *"technically supported by the Feedback editor but was never used in an actual game and thus is considered to not be supported."* | same |
| `Drums` | Drums / Pro Drums / 5-Lane Drums (one suffix for all three) | [.chart → Drums § Section Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/Drums/) |
| `GHLGuitar` | 6-fret Lead | [.chart → 6-Fret Guitar § Section Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/6-Fret-Guitar/); FireFox |
| `GHLBass` | 6-fret Bass | same |
| `GHLRhythm` | 6-fret Rhythm | same |
| `GHLCoop` | 6-fret Co-op | same |
| `GHLKeys` | 6-fret Keys | TheNathannator only — **absent from FireFox's list** |

**Legacy Feedback-only suffixes** — *"present in Feedback Editor, but they were never fleshed out or implemented in anything significant. They simply use the regular 5-fret guitar layout when edited in Feedback"*: `EnhancedGuitar`, `CoopLead`, `CoopBass`, `10KeyGuitar`, `DoubleDrums`, `Vocals`. — [.chart → Legacy Tracks](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/Legacy-Tracks/). **NOT DOCUMENTED:** whether these take a difficulty prefix. Ignore them.

**Non-instrument sections:** `[Song]`, `[SyncTrack]`, `[Events]`. *"All sections aside from Song (for chart resolution) and SyncTrack are optional. Instrument sections may show up in any order. **A missing section is equivalent to a section with no data.**"* — Format Overview § Section Names. And: *"Any unrecognized/unknown sections should be ignored."*

So the v1 matrix is **4 × 11 = 44 valid instrument section names** (excluding `SingleBass` and the Feedback legacy set), of which YARG's `Notes.chart` wiki page says the format supports *"5-Fret Guitar, 6-Fret Guitar, 4-Lane Drums, 5-Lane Drums and Pro Drums"* — [YARG Wiki, Notes.chart](https://wiki.yarg.in/wiki/Notes.chart), read via [Wayback 20250918221247](https://web.archive.org/web/20250918221247/https://wiki.yarg.in/wiki/Notes.chart).

## B.2 Telling "has notes" from "exists but empty / events only"

Inside an instrument section every line is `<Position> = <Type Code> <Value[]>`, with type codes: *"`A`: Tempo position anchor / `B`: Tempo change / `E`: Text event / `H`: (Legacy) Guitar Hero 1 hand animation position / `N`: Note event / `S`: Special phrase / `TS`: Time signature change"* — [.chart Format Overview § Track Events](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Format-Overview/).

- `<Position> = N <Type> <Length>` — *"Notes and modifiers use the N type code"*. **Both notes and modifiers use `N`.**
- `<Position> = S <Type> <Length>` — special phrases (Star Power, rolls, face-off). Never a note.
- `<Position> = E <Text>` — local events (`solo`, `soloend`, `mix_…`). Never a note.

So **"has real notes" = at least one `N` line whose Type is in the instrument's playable set**:

| Instrument | Playable `N` types | Modifier `N` types (do NOT count) |
|---|---|---|
| 5-fret (`Single`, `Double*`, `Keyboard`) | **0,1,2,3,4** (G,R,Y,B,O) and **7** (open) | 5 = strum/HOPO flip, 6 = tap |
| 6-fret (`GHL*`) | **0,1,2** (W1–W3), **3,4,8** (B1–B3), **7** (open) | 5 = flip, 6 = tap |
| `Drums` | **0** kick, **1,2,3,4** lanes, **5** 5-lane green, **32** 2x kick | 34–38 accents, 40–44 ghosts, 66–68 cymbal toggles |

Sources: [.chart → 5-Fret Guitar § Note and Modifier Types](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/5-Fret-Guitar/), [→ 6-Fret Guitar](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/6-Fret-Guitar/), [→ Drums § Note and Modifier Types](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/Drums/), corroborated by FireFox § N - Notes.

Note the 6-fret oddity FireFox flags: Black-3 is `N 8`, not `N 5`, *"in order to prevent issues like having Black Lane 2 have a fret value of 4 and Black Lane 3 have a non-sequential fret value of 8"* — 5 was already taken by the force flag.

**Worked example** from the docs — this `[ExpertSingle]` is non-empty because of `N 0`, `N 1`, `N 2`, `N 3`, `N 4`, `N 7`; the `N 5`, `N 6`, `S 64` and `E solo` lines would not qualify on their own:

```
[ExpertSingle]
{
  768 = N 0 0
  768 = S 64 768
  864 = N 1 0
  864 = N 5 0
  ...
  1248 = N 7 0
  1248 = E soloend
}
```

**Drum type detection in `.chart`** mirrors `.mid` but the cymbal polarity is inverted: *"4-lane notes are toms by default in .chart. Cymbals are marked using the cymbal modifiers"* — versus *"4-lane notes are cymbals by default in .mid."* FireFox is explicit: *"Note that notes are toms by default and are manually toggled to cymbals. This is the opposite of how the RBN midi spec handles cymbals."* Detection: *"If cymbal markers are present, it's a 4-lane Pro track. If the 5-lane green note is present, or if notes are sustained, it's a 5-lane track."* (`.chart` says **cymbal** markers 66–68; `.mid` says **tom** markers 110–112 — same idea, opposite polarity.)

## B.3 Format asymmetries

**In `.mid`, not in `.chart`:**
- Vocals, Harmonies, Pro Keys, Pro Guitar/Bass, Elite Drums, Dance. YARG states plainly: *"Unfortunately, Elite Drums, Vocals, and other Pro instruments (such as Pro Guitar and Pro Keys) are not supported by the notes.chart format and therefore cannot be created with Moonscraper."* — [YARG Wiki, Help:Charting § Chart creation tools](https://wiki.yarg.in/wiki/Help:Charting), read via [Wayback 20260823010420](https://web.archive.org/web/20260823010420/https://wiki.yarg.in/wiki/Help:Charting). And *"It is considered inferior to the notes.mid format due its limited range of available instruments"* — YARG Wiki Notes.chart.
- Per-difficulty force-strum/HOPO as distinct note numbers; SysEx modifiers; velocity-encoded dynamics; animation data; VENUE; BEAT.
- Tap-note *marker note* 104 and BRE markers.

**In `.chart`, not in `.mid`:**
- **Lyrics live in the global `[Events]` section**, as `phrase_start` / `phrase_end` / `lyric <syllable>` text events — not on a vocals track, and with **no pitch information**. — [.chart → Lyrics § Lyric Events](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Tracks/Lyrics/). This means a `.chart` can carry lyrics while having no vocals *part*; the preparser should not report a Vocals instrument from `.chart` lyrics.
- Tempo anchors (`A` type code).
- Metadata in-file (`[Song]` section) — *"No song metadata is stored within the .mid file itself"*, .mid always needs `song.ini`. For `.chart`, *"The metadata in the song.ini should be prioritized over the metadata in the .chart."*
- GHTCP battle phrases `S 3/4/5`, drum SP-activation phrase `S 64`.
- Multiple `.chart` files per folder with `[Y]`/`[F]`/`[N]` suffixes: *"If multiple .chart files are found, the one named `notes` should be preferred over custom-named .chart files, and names suffixed with `[Y]` or `[F]` should be preferred over `[N]` or no suffix."* — [.chart Format Overview § File Names](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/chart-format/Format-Overview/)

---

# C. Practicalities for a Go implementation

## C.1 The minimum a reader must do

**`.mid`:**

You **cannot skip delta times** — they are variable-length quantities interleaved with events, so the parser must decode each VLQ to find the next event boundary. But you never need to **accumulate** them, convert to seconds, or read the tempo map. Concretely, per track chunk:

1. Read `MThd`: verify **format type 1** and **ticks-per-quarter** division. *"The notes.mid file is written using MIDI format type 1 (multi-track) and ticks-per-quarter resolution. Type 0/2 and SMPTE resolution are not supported."* — [.mid Format Overview § MIDI Format](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Format-Overview/)
2. Read `MTrk` length; the **first event is the track-name meta (`FF 03`)**. *"Tracks are identified by their track name meta event, which is the first event of each track."* — same doc. You may bail out of a track immediately if the name is unrecognised.
3. Walk events, discarding everything but: **Note-On (0x9n) with velocity > 0**, and **meta text events (`FF 01`–`FF 0F`)** for `ENHANCED_OPENS` / lyrics.
4. Set a bit per (track, note-number). One `uint128`-equivalent (`[2]uint64`) bitmap of note numbers per track is all the state you need.

Things you can throw away entirely: Note-Off, controllers, program change, pitch bend, tempo, time signature, SysEx (unless you want the Phase Shift open-note gate).

**`.chart`:** pure line-oriented text scan. Track the current `[Section]`, and for instrument sections set a bit for each `N <type>` seen. No tick arithmetic, no ordering assumptions (*"Sections are not separated with line breaks, and can show up in any order"*, and *"Events within the same section should be written in increasing order of tick position"* is a should, not a must).

**You must still handle MIDI running status.** And the docs warn it is done wrong in the wild: *"Some charts don't reset running status after a System Exclusive or meta event and continue the running status of prior non-Exclusive/meta events, which breaks some MIDI parsers (notably NAudio)."* Also: *"Charts commonly make use of a 0xFF byte in SysEx events. This is barely technically spec-compliant, but some MIDI parsers might have issues with this regardless."* — [.mid Format Overview § Notable Specification Violations](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/mid-format/Format-Overview/). **This is the #1 reason to consider a hand-rolled ~200-line SMF scanner rather than a strict library** — a spec-correct parser will hard-error on files YARG plays fine. If using a library, wrap per-track parsing in a recover/continue so one malformed track doesn't kill the file.

## C.2 Gotchas that produce wrong answers

**Track present but empty / animation-only.** The Elite Drums spec documents this case directly: *"`PART ELITE_DRUMS` does not include any animation data. Charters should continue to use the animation flags in `PART DRUMS`… For this reason, **the mere presence of a `PART DRUMS` track should not disable automatic downcharting**; it is only when `PART DRUMS` contains playable notes that automatic downcharting should be disabled."* And: *"If `PART DRUMS` does not contain any playable notes (such as if it contains only animation data) or does not exist at all, then the game should derive 4L, 4L Pro, and 5L playable parts from `PART ELITE_DRUMS`."* — [Elite Drums Spec § 4L/5L Downcharting](https://docs.google.com/document/d/1wPIHbe2Z2qSELyCFaVcHXO0VqG9rLSBRUZH54HWHLP4/view).

> **Consequence for the browser grid:** a chart with `PART ELITE_DRUMS` and an animation-only `PART DRUMS` **should still show 4-Lane / Pro / 5-Lane as available**, derived by downchart. A v1 preparser that reports "no drums" here is wrong in a user-visible way. Recommendation: report `elite_drums` present, and mark the downcharted 4L/5L parts with a `derived: true` flag rather than omitting them.

**Difficulty blocks containing only markers.** Covered exhaustively in A.4. The single highest-yield mitigations, in order: (1) allow-list playable notes rather than range-testing; (2) gate 5-fret note 59/71/83/95 on `ENHANCED_OPENS`; (3) never count 103/104/105/106/115/116/120–127.

**`song.ini` declares an instrument the chart doesn't contain.** The `diff_*` tags (`diff_guitar`, `diff_bass`, `diff_drums`, `diff_drums_real`, `diff_keys`, `diff_keys_real`, `diff_vocals`, `diff_vocals_harm`, `diff_guitar_real`, `diff_guitar_real_22`, `diff_bass_real`, `diff_bass_real_22`, `diff_guitarghl`, `diff_bassghl`, `diff_rhythm`, `diff_rhythm_ghl`, `diff_guitar_coop`, `diff_guitar_coop_ghl`, `diff_drums_real_ps`, `diff_keys_real_ps`, `diff_dance`, `diff_band`) are **difficulty *ratings*, not presence flags** — [song.ini Standard Tags](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/song-ini/Standard-Tags/). Elite Drums adds `diff_elite_drums`, and the spec says it *"should be treated as an entirely separate diff from `diff_drums` and `diff_drums_real`, with its own icon and reserved space in the Song Select menu."*

The only documented "absent" convention is: *"A value of -1 may be used in some number fields to indicate that there is no value set."* — [song.ini Format Overview § Tags](https://thenathannator.github.io/GuitarGame_ChartFormats/Chart-File-Formats/song-ini/Format-Overview/). It says *may*, and *no value set* — not *part absent*.

> **Rule: the chart file is the sole authority on what exists. `song.ini` is authority only on (a) drum type, via `pro_drums` / `five_lane_drums`, (b) the Star Power note, via `multiplier_note` / `star_power_note` (*"The only valid options for these tags are 103 or 116"*), (c) difficulty *ratings* for display.** Never synthesise a part from a `diff_*` tag; never suppress a detected part because its `diff_*` is missing or -1.

**The reverse — chart contains a part `song.ini` never mentions** — is common and expected, since `song.ini` is hand-edited. Report it.

**Other traps:**
- **Duplicate track names in one file.** **NOT DOCUMENTED.** Real files do it (e.g. a stray second `PART GUITAR`). Merge bitmaps by name rather than last-wins.
- **`PART DRUMS_2X`.** *"This shouldn't normally be present."* If both it and `PART DRUMS` exist, it is an alternate rendering of the same part; do not report two drum parts.
- **`PART GUITAR GHL` vs `PART GUITAR`.** Match track names **exactly, whole-string**. Prefix matching maps GHL tracks onto 5-fret and produces garbage difficulties (the note maps differ by one semitone).
- **Case and whitespace.** **NOT DOCUMENTED** whether track names are case-sensitive or may carry trailing whitespace. Trim trailing NULs/whitespace defensively; match case-sensitively first, then case-insensitively as a fallback, and log the fallback.
- **`.chart` quoting.** *".chart has no character escapes however, so values (normally) cannot have quotation marks in them… Of course, people did this anyways."* Only relevant for metadata, not for `N` lines.
- **`.chart` multiple files.** Pick per the `notes` > `[Y]`/`[F]` > `[N]` precedence in B.3, or you may preparse the wrong variant.
- **Pro Keys empty-track trap.** Every Pro Keys track is *required* to carry a range-shift marker at song start, so "track exists and has notes" is always true if you don't restrict to 48–72.
- **Elite Drums Star Power is on 104, not 116.** If you reuse a shared "these are markers" set across families you will silently classify Elite Drums' Overdrive marker as a tap-note marker (both 104) — harmless — but you will also fail to exclude anything from 116, which on Elite Drums is the **Tom 3 roll lane**. Keep per-family marker sets.

## C.3 Go MIDI libraries

| Library | Import path | Licence | Maturity | Verdict |
|---|---|---|---|---|
| **gomidi/midi v2** | `gitlab.com/gomidi/midi/v2/smf` | **MIT** | Actively maintained — GitLab API reports last activity **2026-06-15**, 252 tags, 6 releases. v2 is the current major. | **Recommended.** `smf.ReadFile()` / `ReadFrom()` yield tracks of `(delta, Message)` events; `Message.GetMetaTrackName()`, `GetNoteOn()`, `GetNoteOff()`, `GetNoteEnd()` give exactly the four things a preparser needs. — [pkg.go.dev: gitlab.com/gomidi/midi/v2/smf](https://pkg.go.dev/gitlab.com/gomidi/midi/v2/smf), [gitlab.com/gomidi/midi](https://gitlab.com/gomidi/midi) |
| kbinani/midi | `github.com/kbinani/midi` | MIT (`Copyright (c) 2017 kbinani`) | Minimal — 5 commits, ~5 stars, no time-signature extraction, no writing. | Simple `midi.Read(f)` → `file.Tracks[i].Events`, which is genuinely all a preparser needs; but effectively unmaintained. Useful as a reference implementation, not a dependency. — [github.com/kbinani/midi](https://github.com/kbinani/midi) |
| algoGuy/EasyMIDI | `github.com/algoGuy/EasyMIDI/smf` | MIT | Stale — published 2018, untagged `v0.0.0-…`, no valid `go.mod`, not v1. | Avoid. — [pkg.go.dev: EasyMIDI](https://pkg.go.dev/github.com/algoGuy/EasyMIDI/smf) |
| gomidi/midi v1 | `gitlab.com/gomidi/midi/smf` (+ `smf/smfreader`) | MIT | Superseded by v2. | Avoid for new code. |

**Recommendation:** `gitlab.com/gomidi/midi/v2/smf` as the default, with a fallback hand-rolled chunk scanner behind an interface for files it rejects. Given the documented running-status and `0xFF`-in-SysEx violations, budget for a permissive fallback path from day one; the full "walk MTrk chunks, decode VLQ, collect Note-On + text meta" scanner is small enough (~200 lines) to own outright, and owning it removes the strictness risk entirely. `.chart` needs no dependency at all — `bufio.Scanner` plus `strings.Fields`.

---

# Recommended v1 preparser scope

## Detect (v1)

**From `.mid` — 12 parts:**

| Part reported | Track name(s) | Difficulty detection |
|---|---|---|
| `guitar` | `PART GUITAR`, `T1 GEMS` | 5-fret allow-list |
| `guitar_coop` | `PART GUITAR COOP` | 5-fret allow-list |
| `rhythm` | `PART RHYTHM` | 5-fret allow-list |
| `bass` | `PART BASS` | 5-fret allow-list |
| `keys` | `PART KEYS` | 5-fret allow-list |
| `guitar_ghl` / `bass_ghl` / `rhythm_ghl` / `coop_ghl` / `keys_ghl` | `PART … GHL` | 6-fret allow-list |
| `drums` (+ subtype `4lane` / `pro` / `5lane`) | `PART DRUMS`, `PART DRUM` | drums allow-list; subtype by `pro_drums`/`five_lane_drums`, else 110–112 → pro, else 65/77/89/101 → 5-lane, else 4-lane |
| `elite_drums` | `PART ELITE_DRUMS` | Elite gem allow-list |
| `pro_keys` | `PART REAL_KEYS_{X,H,M,E}` | one difficulty per track, notes 48–72 |
| `vocals` | `PART VOCALS` | no difficulties — boolean |
| `harmonies` (count 2 or 3) | `HARM1/2/3`, `PART HARM1/2/3` | no difficulties — count of non-empty tracks |
| `pro_guitar` / `pro_bass` (+ `17`/`22`) | `PART REAL_{GUITAR,BASS}[_22]` | Pro Guitar allow-list |

**From `.chart` — 11 parts** (the 4 × 11 matrix minus `SingleBass`): `Single`, `DoubleGuitar`, `DoubleBass`, `DoubleRhythm`, `Keyboard`, `Drums`, `GHLGuitar`, `GHLBass`, `GHLRhythm`, `GHLCoop`, `GHLKeys` — each with the four difficulties, gated on ≥1 playable `N` type.

**Derived-part flag:** when `PART ELITE_DRUMS` has gems and `PART DRUMS` has none, emit `drums` with `derived: true` for the difficulties Elite Drums has.

## Skip (v1) — and why

| Skipped | Reason |
|---|---|
| `PART DANCE` | Phase Shift only; *"the format doesn't seem to have been fleshed out very much"*; not in YARG's supported-modes list on Notes.mid. |
| `PART REAL_KEYS_PS_*` (Phase Shift Real Keys) | Different note range (48–108), Phase Shift only, not in YARG's track table. |
| `PART REAL_DRUMS_PS` | *"same notes as the standard Drums track"* — folding it in as an alias of `PART DRUMS` risks double-reporting; defer. |
| `PART REAL_GUITAR_BONUS` | *"Unknown Pro Guitar track that EoF has available"* — undefined semantics. |
| `SingleBass`, `EnhancedGuitar`, `CoopLead`, `CoopBass`, `10KeyGuitar`, `DoubleDrums`, `Vocals` (.chart) | *"never used in an actual game"* / *"never fleshed out"*. |
| `PART DRUMS_2X` | Alternate rendering, not a distinct part. Track its existence as a boolean if you want a "2x kick available" badge; do not add a grid row. |
| Star Power, solos, BREs, fills, rolls, lanes, taps, forcing, sustains, dynamics, disco flip, lyrics text, tempo, venue, animations | Out of scope by definition — none affects "which parts, which difficulties". |
| Pro Guitar velocity→fret decoding | Not needed for presence. |
| SysEx decoding | **One exception worth keeping:** the Phase Shift open-note SysEx (`modifier 0x01`) as a secondary gate for 5-fret notes 59/71/83/95. Cheap, and the docs say SysEx opens are the *preferred* authoring method (*"the SysEx-based markers for tap notes and open notes should be used when writing charts instead of the note-based markers"*). If v1 skips it, opens-only difficulties authored with SysEx will be missed — rare, since a difficulty with *only* open notes is pathological. |

## Confidence model I'd recommend

Emit per (part, difficulty) a count of playable Note-Ons, not just a boolean. It costs nothing, and it lets the song browser distinguish a real chart from a 1-gem placeholder — which matters because RBN's own minimum bar is literally *"1 gem (all difficulties)"* ([Mix and MIDI Setup](http://docs.c3universe.com/rbndocs/index.php?title=Mix_and_MIDI_Setup)), so single-gem stub difficulties genuinely exist in the wild and are format-legal.

---

## Sources

- [Guitar Game Chart Formats (TheNathannator)](https://thenathannator.github.io/GuitarGame_ChartFormats/) — .mid Format Overview, .mid Tracks (5-Fret Guitar, 6-Fret Guitar, Drums, Pro Guitar, Pro Keys, Vocals, Global Events), .chart Format Overview, .chart Tracks (5-Fret Guitar, 6-Fret Guitar, Drums, Lyrics, Legacy Tracks), song.ini Format Overview and Standard Tags, Implementation-Specific → Rock Band (Guitar, Drums, Pro Keys), Phase Shift (Real Drums, Real Keys, Dance), Guitar Hero 1 and 2 (Guitar)
- [Chart File Format Specifications, by FireFox (Moonscraper author)](https://docs.google.com/document/d/1v2v0U-9HQ5qHeccpExDOLJ5CMPZZ3QytPmAG5WF0Kzs/view)
- [RBN/C3 Documentation](http://docs.c3universe.com/rbndocs/index.php?title=Authoring) — [Mix and MIDI Setup](http://docs.c3universe.com/rbndocs/index.php?title=Mix_and_MIDI_Setup), [Guitar and Bass Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Guitar_and_Bass_Authoring), [Drum Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Drum_Authoring), [5 Lane Keyboard Authoring](http://docs.c3universe.com/rbndocs/index.php?title=5_Lane_Keyboard_Authoring), [Pro Keyboard Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Pro_Keyboard_Authoring), [Vocal Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Vocal_Authoring), [Harmony Authoring](http://docs.c3universe.com/rbndocs/index.php?title=Harmony_Authoring)
- [Elite Drums MIDI/Engine Specification](https://docs.google.com/document/d/1wPIHbe2Z2qSELyCFaVcHXO0VqG9rLSBRUZH54HWHLP4/view)
- YARG Wiki (all via Wayback — live host returns 403): [Help:Charting](https://web.archive.org/web/20260823010420/https://wiki.yarg.in/wiki/Help:Charting), [Notes.mid](https://web.archive.org/web/20260823060309/https://wiki.yarg.in/wiki/Notes.mid), [Notes.chart](https://web.archive.org/web/20250918221247/https://wiki.yarg.in/wiki/Notes.chart)
- Go libraries: [gitlab.com/gomidi/midi/v2/smf](https://pkg.go.dev/gitlab.com/gomidi/midi/v2/smf), [gitlab.com/gomidi/midi](https://gitlab.com/gomidi/midi), [github.com/kbinani/midi](https://github.com/kbinani/midi), [github.com/algoGuy/EasyMIDI](https://pkg.go.dev/github.com/algoGuy/EasyMIDI/smf)

---

**Key findings worth surfacing to the caller:**

1. `wiki.yarg.in` is unreachable directly (Miraheze anti-bot 403 on HTML, `api.php`, `rest.php` and `action=raw` alike). I read all three requested pages via the Wayback Machine. `Notes.mid` is a **stub** whose track table omits 6-fret and Pro Guitar despite claiming support for them; `Notes.chart` is two sentences.
2. The single largest false-positive source is **5-fret MIDI note 59**, which is simultaneously "Easy Open" and "Left hand position 20". It must be gated on an `ENHANCED_OPENS` text event. GH1 `T1 GEMS` is the second: it places star-power and face-off markers *inside* every difficulty octave (67/69/70, 79/81/82, 91/93/94, 103/105/106).
3. Four families break the standard 60/72/84/96 layout: **6-fret** (9-wide blocks starting a semitone lower), **drums** (Expert starts at 95), **Pro Guitar** (11-wide blocks at 24/48/72/96), and **Elite Drums** (two octaves per difficulty, gems at 0/24/48/72). **Pro Keys** and **Vocals/Harmonies** have no blocks at all.
4. Elite Drums puts Star Power on note **104**, not 116 — the only family that does — and 116 there is a tom-3 roll lane. Per-family marker sets are mandatory.
5. Documented behaviour with real UX impact: an animation-only `PART DRUMS` alongside `PART ELITE_DRUMS` must still yield playable 4L/Pro/5L parts by downcharting. A naive preparser reports "no drums" for these charts.
6. `song.ini` `diff_*` tags are ratings, not presence flags; the only documented "absent" convention is `-1` meaning "no value set". The chart file must be the sole authority on existence.
7. Recommended dependency: `gitlab.com/gomidi/midi/v2/smf` (MIT, active as of 2026-06-15) — but budget for a permissive fallback scanner, because the format docs explicitly record that real charts violate running-status reset after SysEx/meta and embed `0xFF` in SysEx.
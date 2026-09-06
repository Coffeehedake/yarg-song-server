package chart

import (
	"bufio"
	"bytes"
	"strings"
)

// UltraStar (notes.txt) is a karaoke format, and in YARG it is a VOCALS-ONLY
// format: no instrumental parts are recognised at all.
//
// It is also the one chart format whose metadata does NOT come from song.ini.
// YARG reads TITLE, ARTIST and ALBUM out of the .txt itself, and refuses the
// song outright when TITLE is missing or blank - which is where the
// "Name metadata not provided" rejection in our corpus run came from.
//
// Header syntax, from the official UltraStar format specification: each header
// line starts with '#', key and value are separated by ':', whitespace around
// both is ignored, key comparison is case-insensitive, and the header ends at
// the first body element. Files are UTF-8 with no byte order mark.

// UltraStar is the metadata an UltraStar chart carries in place of song.ini.
type UltraStar struct {
	Title  string
	Artist string
	Album  string
	// Tags is every header tag as read, keys upper-cased.
	Tags map[string]string
	// HasTitle is false when TITLE is absent or blank. YARG rejects such a
	// chart, so this is a rejection signal rather than a cosmetic gap.
	HasTitle bool
}

// PreparseUltraStar reads the header block and reports the parts YARG derives.
func PreparseUltraStar(data []byte) (*Result, *UltraStar, error) {
	us := &UltraStar{Tags: map[string]string{}}

	sc := bufio.NewScanner(bytes.NewReader(data))
	sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" {
			continue // empty lines are ignored throughout the file
		}
		if !strings.HasPrefix(line, "#") {
			break // first body element ends the header
		}
		colon := strings.IndexByte(line, ':')
		if colon < 0 {
			continue
		}
		key := strings.ToUpper(strings.TrimSpace(line[1:colon]))
		value := strings.TrimSpace(line[colon+1:])
		if key == "" {
			continue
		}
		if _, seen := us.Tags[key]; !seen {
			us.Tags[key] = value
		}
	}
	if err := sc.Err(); err != nil {
		return nil, nil, err
	}

	us.Title = us.Tags["TITLE"]
	us.Artist = us.Tags["ARTIST"]
	us.Album = us.Tags["ALBUM"]
	us.HasTitle = strings.TrimSpace(us.Title) != ""

	r := newResult()
	if !us.HasTitle {
		// This used to return here without deriving any part, on the belief
		// that YARG refuses a title-less UltraStar chart outright and so its
		// contents did not matter. The oracle disproved that on 2026-09-05:
		// packed into a .sng the song PLAYS, because the packer writes the
		// name into the archive metadata from song.ini. Returning early made
		// 21-ultrastar-no-title report zero parts while its byte-identical
		// twin 20-ultrastar reported vocals - the only difference between the
		// two files is the #TITLE line.
		//
		// What a chart CONTAINS does not depend on whether it is titled. The
		// title is a metadata question, raised as an issue by the caller, and
		// note derivation carries on below regardless.
		r.note("UltraStar chart has no #TITLE; YARG refuses it as a loose folder, but plays it once packed, because the name then comes from the archive metadata")
	}

	// Vocals only, always at Expert - UltraStar has no difficulty tiers.
	r.add(LeadVocals, Expert)

	// Harmony is keyed on PARTS, which YARG reads but which is not one of the
	// core tags in the UltraStar specification - the spec uses #P1..#P9 to name
	// voices. We follow YARG, because agreeing with the client is the point,
	// and record the discrepancy rather than quietly reconciling it.
	if us.Tags["PARTS"] == "2" {
		r.add(HarmonyVocals, Expert)
		r.HarmonyCount = 2
	} else if _, hasP2 := us.Tags["P2"]; hasP2 {
		r.note("UltraStar chart declares a #P2 voice but not #PARTS:2; YARG keys harmony on PARTS, so no harmony part is reported")
	}

	r.note("UltraStar is a vocals-only format in YARG; no instrumental parts are derived from it")
	return r, us, nil
}

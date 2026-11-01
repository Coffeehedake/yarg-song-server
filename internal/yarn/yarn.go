// Package yarn reads YARG's own release network: the marketplace index, the per-pack
// profiles, and the archives those profiles point at.
//
// Everything this package READS is public and unauthenticated. The index, all 41 setlist
// profiles and the archives themselves were fetched anonymously while this was written; there
// is no account, no token and nothing purchased. What the archives have is a password, derived
// by published arithmetic - see password.go, which explains why we implement it.
//
// The split in here is deliberate. Catalogue reading is cheap, safe and useful on its own: it
// answers "what songs exist and which pack holds them" without downloading a byte of audio.
// Installing is the expensive, disk-writing half and lives behind a feature that is off by
// default. A client that only wants search should never cause a download.
package yarn

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the launcher's own releases server, read from its public .env.
const DefaultBaseURL = "https://releases.yarg.in"

// userAgent identifies us honestly. Pretending to be the launcher would make our traffic
// indistinguishable from it in YARC's logs, which is exactly the wrong thing to do when the
// whole position of this package is "we are doing this openly".
const userAgent = "yarg-song-server (+https://github.com/coffeehedake/yarg-song-server)"

// Entry is one row of the marketplace index.
type Entry struct {
	UUID     string `json:"uuid"`
	Type     string `json:"type"`     // "setlist", "venue", "application"
	Category string `json:"category"` // "official_setlist", "yarn_setlist", ...
	URL      string `json:"url"`
	Release  string `json:"release"`
	Name     string `json:"name"`
	SubText  string `json:"subText"`
}

// IsSetlist reports whether this entry is songs rather than a venue or the game itself.
func (e Entry) IsSetlist() bool { return e.Type == "setlist" }

type index struct {
	LastUpdated string  `json:"lastUpdated"`
	Profiles    []Entry `json:"profiles"`
}

// Song is one track as the pack's own metadata describes it.
type Song struct {
	Title       string `json:"title"`
	Artist      string `json:"artist"`
	LengthMS    int64  `json:"length"`
	ReleaseDate string `json:"releaseDate"`
}

// File is one downloadable archive.
//
// FileType is "encrypted" for every setlist archive YARN publishes - all 42 of them, measured
// 2026-10-31. The field is kept rather than assumed away because an unencrypted pack would be
// perfectly legal to handle and we should notice if one appears.
type File struct {
	FileType string `json:"fileType"`
	URL      string `json:"url"`
}

type content struct {
	Platforms []string `json:"platforms"`
	Files     []File   `json:"files"`
}

type versionInner struct {
	UUID    string    `json:"uuid"`
	Tag     string    `json:"tag"`
	Release string    `json:"release"`
	Content []content `json:"content"`
}

type versionOuter struct {
	Type    string       `json:"type"`
	Version versionInner `json:"version"`
}

type metadata struct {
	Name        string `json:"name"`
	Description string `json:"description"`
	Badge       string `json:"badge"`
	Songs       []Song `json:"songs"`
}

// Profile is one pack: what it contains and where its archives are.
type Profile struct {
	Type     string       `json:"type"`
	UUID     string       `json:"uuid"`
	Metadata metadata     `json:"metadata"`
	Version  versionOuter `json:"version"`
}

// Name is the pack's display name.
func (p *Profile) Name() string { return p.Metadata.Name }

// Songs is what the pack says it contains.
func (p *Profile) Songs() []Song { return p.Metadata.Songs }

// Tag identifies the published version, e.g. "official-2026-09-04-0".
func (p *Profile) Tag() string { return p.Version.Version.Tag }

// Files are the archives to fetch, in order. A pack may be split across several.
func (p *Profile) Files() []File {
	var out []File
	for _, c := range p.Version.Version.Content {
		out = append(out, c.Files...)
	}
	return out
}

// Client reads the release network.
type Client struct {
	BaseURL string
	HTTP    *http.Client
}

// NewClient returns a client with a timeout that is generous for JSON and useless for a 1.9 GB
// archive - downloads take their own context, because one timeout cannot serve both.
func NewClient() *Client {
	return &Client{
		BaseURL: DefaultBaseURL,
		HTTP:    &http.Client{Timeout: 30 * time.Second},
	}
}

func (c *Client) base() string {
	if c.BaseURL == "" {
		return DefaultBaseURL
	}
	return strings.TrimRight(c.BaseURL, "/")
}

func (c *Client) getJSON(ctx context.Context, url string, into any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", userAgent)

	resp, err := c.HTTP.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("GET %s: HTTP %d", url, resp.StatusCode)
	}
	if err := json.NewDecoder(resp.Body).Decode(into); err != nil {
		return fmt.Errorf("GET %s: %w", url, err)
	}
	return nil
}

// Index fetches the marketplace listing.
func (c *Client) Index(ctx context.Context) ([]Entry, error) {
	var ix index
	if err := c.getJSON(ctx, c.base()+"/profiles/", &ix); err != nil {
		return nil, err
	}
	return ix.Profiles, nil
}

// Profile fetches one pack's detail. The URL comes from an index Entry rather than being
// built here, so a change in their layout does not need a change in ours.
func (c *Client) Profile(ctx context.Context, entryURL string) (*Profile, error) {
	var p Profile
	if err := c.getJSON(ctx, entryURL, &p); err != nil {
		return nil, err
	}
	return &p, nil
}

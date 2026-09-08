package config

// This file is the feature registry: one place that says what the server's
// optional capabilities are, whether each is on, and WHY it is on.
//
// The "why" is the part that earns its keep. Settings arrive from three places
// - built-in defaults, a config file, then flags - and until now the server
// could report the resolved value but not its origin. Those are different
// answers to different questions. "check_uploads is off" is a fact; "check
// _uploads is off and nothing you wrote said otherwise" tells an operator their
// config file was never read, which is the failure that costs an afternoon.
//
// It is also what a config UI needs before it can exist (ROADMAP phase 4): a
// menu of capabilities has to render the current state and where it came from
// before anyone can sensibly change it.

// Source says where a resolved value came from.
type Source string

const (
	// FromDefault: nothing the operator wrote mentions this setting.
	FromDefault Source = "default"
	// FromFile: a config file set it.
	FromFile Source = "file"
	// FromFlag: a flag was actually typed on the command line. Flags win.
	FromFlag Source = "flag"
)

// Feature is one optional capability as the server actually resolved it.
//
// Name is the config-file key, which is the flag name with underscores - the
// package keeps one vocabulary rather than two that can drift, and the registry
// inherits that.
type Feature struct {
	Name        string `json:"name"`
	Enabled     bool   `json:"enabled"`
	Default     bool   `json:"default"`
	Source      Source `json:"source"`
	Description string `json:"description"`
	// Endpoint is the route this capability adds, when it adds one. Empty for a
	// feature that changes behaviour without adding a route.
	Endpoint string `json:"endpoint,omitempty"`
	// EnableWith is what an operator types to turn it on. Present whether the
	// feature is on or off, because the reader who needs it is the one looking
	// at a feature that is off.
	EnableWith string `json:"enable_with"`
}

// Resolved is a Config together with where each of its values came from.
//
// File is the config file that was actually read, or "" when none was - which
// is a normal first run and also the state an operator is in when they edited a
// file the server never looked at. Reporting it is the whole point.
type Resolved struct {
	Config
	File       string
	Provenance map[string]Source
}

// sourceOf reports where a setting came from, defaulting to FromDefault so a
// zero Resolved still answers honestly rather than with an empty string.
func (r Resolved) sourceOf(key string) Source {
	if s, ok := r.Provenance[key]; ok && s != "" {
		return s
	}
	return FromDefault
}

// Features is the registry: every optional capability, in a stable order.
//
// Only capabilities belong here - things an operator turns on or off. Paths,
// the listen address and cache sizes are settings, not features, and they are
// deliberately absent: this list is served unauthenticated (see the API docs),
// and a server's filesystem layout is not something to hand to anyone who can
// reach the port. Which capabilities exist is not a secret; where the songs
// live is nobody's business.
func (r Resolved) Features() []Feature {
	def := Defaults()
	return []Feature{
		{
			Name:        "browse_ui",
			Enabled:     r.BrowseUI,
			Default:     def.BrowseUI,
			Source:      r.sourceOf("browse_ui"),
			Description: "Phone-friendly page listing the library.",
			Endpoint:    "GET /",
			EnableWith:  "--browse-ui / browse_ui = yes",
		},
		{
			Name:        "check_uploads",
			Enabled:     r.CheckUploads,
			Default:     def.CheckUploads,
			Source:      r.sourceOf("check_uploads"),
			Description: "Scan an uploaded archive and answer with the verdict, keeping nothing.",
			Endpoint:    "POST /api/v1/check",
			EnableWith:  "--check-uploads / check_uploads = yes",
		},
	}
}

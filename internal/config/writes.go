package config

import "fmt"

// WriteAccess says who, if anyone, may change a feature at runtime.
//
// This is the setting that turns the read-only feature registry into the config
// menu ROADMAP phase 4 asks for, and it is the one setting in this package that
// exists purely to bound risk. Everything else here answers "what should the
// server do"; this answers "who is allowed to change that".
//
// It is deliberately NOT a bool. "on" would have meant "anyone who can reach
// the port may reconfigure this server", and on a LAN that is every device on
// the network including the ones nobody is thinking about. The middle value is
// the useful one: the operator, sitting at the machine, gets a settings page,
// and nobody else does.
type WriteAccess string

const (
	// WritesOff: nothing may change a feature over HTTP. The route is not
	// there. This is the default, and an existing deployment must never
	// acquire a write surface by being upgraded.
	WritesOff WriteAccess = "off"
	// WritesLocal: only a caller from this machine. A settings page opened on
	// the server itself works; the same page opened from a phone does not.
	WritesLocal WriteAccess = "local"
	// WritesLAN: any caller that can reach the port. Correct for a headless
	// box you administer from a laptop, and a decision the operator has to
	// make on purpose, because this server has no authentication of any kind.
	WritesLAN WriteAccess = "lan"
)

// ParseWriteAccess reads the three spellings and refuses everything else.
//
// A bool spelling is accepted for neither "yes" nor "no": somebody writing
// `config_writes = yes` has a specific intent - probably "let me change things"
// - and guessing which of local or lan they meant is exactly the guess that
// ends with a server reconfigurable by the whole network. Refusing to start and
// naming the three values is the kinder failure.
func ParseWriteAccess(s string) (WriteAccess, error) {
	switch WriteAccess(s) {
	case WritesOff, WritesLocal, WritesLAN:
		return WriteAccess(s), nil
	}
	return "", fmt.Errorf("%q is not off, local or lan", s)
}

// AllowsLAN reports whether a caller from another machine may write.
func (w WriteAccess) AllowsLAN() bool { return w == WritesLAN }

// Enabled reports whether the write route exists at all.
func (w WriteAccess) Enabled() bool { return w == WritesLocal || w == WritesLAN }

// Writable is the set of features a caller may change over HTTP.
//
// `config_writes` is NOT in it, and that is the important part. A write surface
// that can widen its own access is a write surface with no bound at all: one
// call to set it to "lan" and the "only from this machine" promise is gone,
// made by whoever was already allowed to make it. Changing who may configure
// this server stays a decision made at the machine, in the file.
//
// The paths, the listen address and the cache size are absent for a duller
// reason: they are settings rather than capabilities, and the registry this
// list mirrors has never carried them.
func Writable(name string) bool {
	switch name {
	case "browse_ui", "check_uploads":
		return true
	}
	return false
}

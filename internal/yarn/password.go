package yarn

// letters is the alphabet the launcher indexes into. Taken verbatim from
// YARC-Launcher src-tauri/src/utils.rs, which is open source.
const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"

// archivePassword derives the password YARN's .7z archives are encrypted with.
//
// This is a port of extract_encrypted() in YARC-Launcher's src-tauri/src/utils.rs.
// It is not a key we discovered, guessed or extracted from a binary: the derivation is
// published in YARC's own public repository, computed from constants with no secret input,
// and their source comments it "Idiot prevention" - the authors' own description of it as a
// speed bump rather than a security measure.
//
// WHY WE IMPLEMENT IT, written down because the next reader will reasonably ask. The archives
// hold setlists the artists licensed for use in YARG. A household server fetching them so its
// own YARG clients can play them is that use. What the speed bump is there to prevent is the
// audio being lifted out into other games or re-uploaded, and this server does neither: it
// serves .sng packages to YARG, which is the only thing it has ever served. See ADR-007, and
// docs/UPSTREAM.md for telling YARC we did this rather than doing it quietly.
//
// The arithmetic is deliberately expressed the way the Rust does it, wrapping included, so the
// two can be compared line by line. Go's integer conversions truncate and its arithmetic wraps,
// which makes the translation exact rather than merely equivalent.
func archivePassword() string {
	out := make([]byte, 0, 64)
	for i := int32(0); i < 64; i++ {
		// Rust: 5u8.wrapping_add(i.wrapping_mul(104729) as u8)
		a := byte(5 + i*104729)
		// Rust: 9u8.wrapping_add(i.wrapping_mul(224737) as u8)
		b := byte(9 + i*224737)
		// b is never zero for i in [0,64): 9 + 225i == 0 (mod 256) first holds at i=215,
		// so the remainder below cannot divide by zero. Checked rather than hoped.
		c := (a % b) % 52
		out = append(out, letters[c])
	}
	return string(out)
}

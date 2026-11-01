package yargsong

import (
	"fmt"

	"github.com/coffeehedake/yarg-song-server/internal/sng"
)

// OpenArchive opens a .yargsong as the SNG it actually is.
//
// Two layers come off here. The Crawford transform in this package turns the file into a
// readable body, and that body is an ordinary SNG wearing a decoy where its magic should be -
// the packs measured carried "RB3CON", which is a red herring rather than a Rock Band package.
// sng.OpenUntagged skips those six bytes exactly as YARG does.
//
// The returned Archive reads lazily through the decoded Reader, so opening a 200 MB song costs
// its header rather than its audio. The caller closes the Reader, which the Archive borrows.
func OpenArchive(path string) (*sng.Archive, *Reader, error) {
	r, err := Open(path)
	if err != nil {
		return nil, nil, err
	}

	a, err := sng.OpenUntagged(r, r.Size())
	if err != nil {
		r.Close()
		return nil, nil, fmt.Errorf("yargsong: decoded body is not an SNG: %w", err)
	}
	return a, r, nil
}

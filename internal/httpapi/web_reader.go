package httpapi

import (
	"bytes"
	"io"
)

// newBytesReader adapts a byte slice to what http.ServeContent needs.
//
// bytes.Reader already satisfies io.ReadSeeker; this exists only to make the
// intent obvious at the call site and to keep the embed a []byte rather than a
// string, so no copy is made per request.
func newBytesReader(b []byte) io.ReadSeeker { return bytes.NewReader(b) }

package yarn

import (
	"crypto/sha256"
	"encoding/hex"
	"hash"
)

// hasher lets a download be hashed while it streams to disk, so a 1.9 GB archive is read once
// rather than written and then read back.
type hasher struct{ h hash.Hash }

func newHasher() *hasher { return &hasher{h: sha256.New()} }

func (w *hasher) Write(p []byte) (int, error) { return w.h.Write(p) }

func (w *hasher) hex() string { return hex.EncodeToString(w.h.Sum(nil)) }

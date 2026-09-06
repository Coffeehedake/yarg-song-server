package sng

import "crypto/sha256"

// Masking in .sng is obfuscation, not encryption. There is no key material to
// recover: the 16-byte per-file mask is stored in plaintext in the header.
//
// The mask expands to a 256-byte table:
//
//	table[i] = mask[i%16] ^ byte(i)     for i in 0..255
//
// and each contained file is XORed with it:
//
//	plain[j] = cipher[j] ^ table[j%256]
//
// where j is the byte index WITHIN THE CONTAINED FILE, starting at 0 for each
// file - not the absolute offset in the .sng. YARG tracks this per-stream in
// SngFileStream.cs. Getting this wrong produces garbage for every file except
// the first.

// MaskTableSize is the length of the expanded XOR table.
const MaskTableSize = 256

// maskKeyDomain separates this derivation from every other use of the same
// input, so a mask can never be mistaken for - or turned back into - the hash it
// was derived from.
const maskKeyDomain = "yarg-song-server/sng-mask/v1\x00"

// MaskKeyFor derives a header mask deterministically from a seed the caller
// chooses.
//
// The mask is not key material. It is stored in plaintext in the header of
// every .sng, so deriving it rather than drawing it from crypto/rand gives
// nothing away: an attacker who wants the mask reads it out of the file.
//
// What derivation buys is that packing the same folder produces the same bytes
// on every machine and every run. That is not a nicety. The server sends a
// strong ETag built from the package hash and honours Range requests, and both
// of those promise that the same ETag means the same octets. With a random mask
// they did not: on 2026-09-06 two clients synced the same 22 songs from the same
// server and got 16 different archives, and a Range resume spanning a cache
// eviction would have spliced two differently masked archives together.
//
// Seed it with something derived from the content - the package hash - so that
// two different packages still get two different masks.
func MaskKeyFor(seed string) [MaskSize]byte {
	sum := sha256.Sum256([]byte(maskKeyDomain + seed))
	var key [MaskSize]byte
	copy(key[:], sum[:])
	return key
}

// Mask is an expanded XOR table derived from a .sng file's 16-byte header mask.
type Mask [MaskTableSize]byte

// NewMask expands the 16-byte header mask into its 256-byte table.
func NewMask(key [MaskSize]byte) Mask {
	var m Mask
	for i := 0; i < MaskTableSize; i++ {
		m[i] = key[i%MaskSize] ^ byte(i)
	}
	return m
}

// ApplyAt XORs buf in place, treating buf[0] as byte offset `off` within the
// contained file. Masking is symmetric, so this both masks and unmasks.
//
// Callers streaming a file in chunks must pass the running offset, not zero.
func (m *Mask) ApplyAt(buf []byte, off int64) {
	start := int(off % MaskTableSize)
	if start < 0 {
		start += MaskTableSize
	}
	for i := range buf {
		buf[i] ^= m[(start+i)%MaskTableSize]
	}
}

// Apply XORs buf in place starting from the beginning of a contained file.
func (m *Mask) Apply(buf []byte) { m.ApplyAt(buf, 0) }

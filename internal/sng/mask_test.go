package sng

import (
	"bytes"
	"crypto/rand"
	"testing"
)

func TestNewMaskMatchesSpec(t *testing.T) {
	var key [MaskSize]byte
	for i := range key {
		key[i] = byte(i * 7)
	}
	m := NewMask(key)
	for i := 0; i < MaskTableSize; i++ {
		want := key[i%MaskSize] ^ byte(i)
		if m[i] != want {
			t.Fatalf("table[%d] = %#x, want %#x", i, m[i], want)
		}
	}
}

func TestApplyIsInvolution(t *testing.T) {
	var key [MaskSize]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	m := NewMask(key)

	orig := make([]byte, 1024+37) // deliberately not a multiple of 256
	if _, err := rand.Read(orig); err != nil {
		t.Fatal(err)
	}

	buf := bytes.Clone(orig)
	m.Apply(buf)
	if bytes.Equal(buf, orig) {
		t.Fatal("masking was a no-op")
	}
	m.Apply(buf)
	if !bytes.Equal(buf, orig) {
		t.Fatal("mask is not its own inverse")
	}
}

// Chunked reads must produce the same bytes as one whole-file read. This is the
// bug that silently corrupts everything after the first 256 bytes.
func TestApplyAtMatchesWholeBuffer(t *testing.T) {
	var key [MaskSize]byte
	if _, err := rand.Read(key[:]); err != nil {
		t.Fatal(err)
	}
	m := NewMask(key)

	orig := make([]byte, 5000)
	if _, err := rand.Read(orig); err != nil {
		t.Fatal(err)
	}

	whole := bytes.Clone(orig)
	m.Apply(whole)

	chunked := bytes.Clone(orig)
	for _, size := range []int{1, 7, 256, 999} {
		buf := bytes.Clone(orig)
		for off := 0; off < len(buf); off += size {
			end := min(off+size, len(buf))
			m.ApplyAt(buf[off:end], int64(off))
		}
		if !bytes.Equal(buf, whole) {
			t.Fatalf("chunk size %d disagrees with whole-buffer masking", size)
		}
		chunked = buf
	}
	_ = chunked
}

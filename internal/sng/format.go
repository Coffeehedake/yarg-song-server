// Package sng reads and writes the .sng container format used by YARG and
// Clone Hero tooling.
//
// A .sng is the same logical content as a loose song folder packed into one
// file: song.ini keys become a metadata key/value section, and every other file
// (chart, audio stems, album art, background) becomes a file-listing entry.
// It is deliberately uncompressed so that readers can stream or mmap it.
//
// Format reference: https://github.com/mdsitton/SngFileFormat
// Upstream reader:  YARG.Core/IO/SngHandler/SngFile.cs
//
// All integers are little-endian.
//
//	offset  size  field
//	0x00    6     magic "SNGPKG"
//	0x06    4     version           uint32
//	0x0A    16    xorMask           [16]byte, random per file
//	0x1A    8     metadata length   int64   (includes the following count field)
//	0x22    8     metadata count    uint64
//	0x2A    var   metadata pairs    keyLen int32, key, valueLen int32, value (UTF-8, no NULs)
//	--      8     file index length int64   (includes the following count field)
//	--      8     listing count     uint64
//	--      var   listings          nameLen uint8, name, contentsLen int64, contentsIndex int64
//	--      8     file data length  uint64
//	--      var   file data         masked bytes
//
// Filenames use forward slashes and are lowercased by YARG on load, so a writer
// should emit them lowercase. Metadata keys are NOT lowercased and must be
// spelled exactly as song.ini spells them.
package sng

// Magic is the 6-byte file signature at offset 0.
var Magic = [6]byte{'S', 'N', 'G', 'P', 'K', 'G'}

// Header field offsets and sizes.
const (
	MagicSize   = 6
	VersionSize = 4
	MaskSize    = 16

	// HeaderSize is magic + version + mask; the metadata section starts here.
	HeaderSize = MagicSize + VersionSize + MaskSize // 0x1A

	// MaxFilenameLen is the ceiling imposed by the single-byte length prefix on
	// each file listing.
	MaxFilenameLen = 255
)

// Version1 is the only version observed in the wild. YARG performs no
// validation against an allowed set at load time, so a reader should accept
// unknown versions rather than refusing them, and only refuse if the structure
// itself fails to parse.
const Version1 uint32 = 1

// Listing is one contained file: where its bytes live in the .sng and how many
// there are. Name is a forward-slash path, lowercased.
type Listing struct {
	Name          string
	ContentsLen   int64
	ContentsIndex int64
}

// File is a parsed .sng header: everything except the file data itself.
type File struct {
	Version  uint32
	Mask     [MaskSize]byte
	Metadata map[string]string
	Listings map[string]Listing
}

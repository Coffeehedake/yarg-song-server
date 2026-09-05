package sng

import (
	"io"
	"io/fs"
	"path"
	"sort"
	"strings"
	"time"
)

// A .sng carries a flat list of forward-slash names, but fs.FS is a tree: it
// requires Open(".") to work and directories to be walkable. Without that,
// fs.WalkDir and fstest.TestFS both refuse the archive, and the "one code path
// for folders and .sng files" design in ADR-001 quietly stops being true.
//
// So the directory structure is synthesised from the names at open time. It is
// cheap - a .sng holds a handful of files - and it means callers can treat an
// archive exactly like os.DirFS over an unpacked folder.

// dirEntries returns the immediate children of dir ("." for the root).
func (a *Archive) dirEntries(dir string) []fs.DirEntry {
	prefix := ""
	if dir != "." {
		prefix = dir + "/"
	}
	seenDirs := map[string]bool{}
	var out []fs.DirEntry
	for _, name := range a.names {
		if prefix != "" && !strings.HasPrefix(name, prefix) {
			continue
		}
		rest := strings.TrimPrefix(name, prefix)
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			child := rest[:i]
			if !seenDirs[child] {
				seenDirs[child] = true
				out = append(out, dirEntry{name: child, dir: true})
			}
			continue
		}
		out = append(out, dirEntry{name: rest, size: a.listings[name].ContentsLen})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name() < out[j].Name() })
	return out
}

// isDir reports whether any listing lives under this path.
func (a *Archive) isDir(name string) bool {
	if name == "." {
		return true
	}
	prefix := name + "/"
	for _, n := range a.names {
		if strings.HasPrefix(n, prefix) {
			return true
		}
	}
	return false
}

// ReadDir implements fs.ReadDirFS.
func (a *Archive) ReadDir(name string) ([]fs.DirEntry, error) {
	if !fs.ValidPath(name) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrInvalid}
	}
	if !a.isDir(strings.ToLower(name)) {
		return nil, &fs.PathError{Op: "readdir", Path: name, Err: fs.ErrNotExist}
	}
	return a.dirEntries(strings.ToLower(name)), nil
}

type dirEntry struct {
	name string
	dir  bool
	size int64
}

func (e dirEntry) Name() string { return e.name }
func (e dirEntry) IsDir() bool  { return e.dir }
func (e dirEntry) Type() fs.FileMode {
	if e.dir {
		return fs.ModeDir
	}
	return 0
}
func (e dirEntry) Info() (fs.FileInfo, error) { return dirEntryInfo(e), nil }

type dirEntryInfo dirEntry

func (i dirEntryInfo) Name() string { return i.name }
func (i dirEntryInfo) Size() int64  { return i.size }
func (i dirEntryInfo) Mode() fs.FileMode {
	if i.dir {
		return fs.ModeDir | 0o555
	}
	return 0o444
}
func (i dirEntryInfo) ModTime() time.Time { return time.Time{} }
func (i dirEntryInfo) IsDir() bool        { return i.dir }
func (i dirEntryInfo) Sys() any           { return nil }

// dirHandle is an open directory, satisfying fs.ReadDirFile.
type dirHandle struct {
	name    string
	entries []fs.DirEntry
	offset  int
}

func (d *dirHandle) Stat() (fs.FileInfo, error) {
	return dirEntryInfo{name: path.Base(d.name), dir: true}, nil
}
func (d *dirHandle) Read([]byte) (int, error) {
	return 0, &fs.PathError{Op: "read", Path: d.name, Err: fs.ErrInvalid}
}
func (d *dirHandle) Close() error { return nil }

func (d *dirHandle) ReadDir(n int) ([]fs.DirEntry, error) {
	if n <= 0 {
		rest := d.entries[d.offset:]
		d.offset = len(d.entries)
		return rest, nil
	}
	if d.offset >= len(d.entries) {
		return nil, io.EOF
	}
	end := min(d.offset+n, len(d.entries))
	out := d.entries[d.offset:end]
	d.offset = end
	return out, nil
}

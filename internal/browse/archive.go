package browse

import (
	"archive/tar"
	"bufio"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"strconv"
	"strings"
	"time"
)

// An archive's table of contents, for the viewer (docs/file-preview.md, "More
// kinds"). Listed, never opened: nothing here reads an entry's bytes into
// anything, and nothing is ever written. What is read is bounded below, and
// each bound is a number an archive cannot talk its way past, because an
// archive is a file an agent was told to download from wherever.

const (
	// ArchiveMaxEntries is how many entries a listing holds; past it the
	// listing says it stopped.
	ArchiveMaxEntries = 10000
	// zipDirMaxBytes bounds how much of a zip's central directory is read. Ten
	// thousand entries with ordinary names are a megabyte or two; the bound is
	// for the directory whose names and extra fields are each 64 KiB.
	zipDirMaxBytes = 32 << 20
	// tarSpecialMaxBytes bounds what a plain tar's headers may make the reader
	// read rather than seek over: PAX records and GNU long names, which
	// archive/tar caps at 1 MiB each and which an archive can repeat.
	tarSpecialMaxBytes = 16 << 20
	// tgzMaxBytes bounds a compressed tar. It cannot be seeked through: every
	// entry's bytes are decompressed to reach the next header, so this is the
	// bound on the work, and a bomb stops here rather than at its own size.
	tgzMaxBytes = 64 << 20
)

// ArchiveEntry is one row of a listing.
type ArchiveEntry struct {
	Path string `json:"path"`
	// Size is the entry's uncompressed size, -1 when the format does not say.
	Size int64 `json:"size"`
	// Modified is Unix seconds, 0 when the format does not say. A zip with no
	// extended timestamp records local time with no zone; it is read as UTC.
	Modified int64 `json:"modified"`
	IsDir    bool  `json:"isDir"`
	// Unsafe is a name that would not stay inside the directory it was
	// extracted into, or that means different things on different systems:
	// absolute, a drive letter, a ".." element, a backslash. The name is shown
	// as the archive holds it, because that is what somebody looking at a
	// suspicious archive needs to see, and marked.
	Unsafe bool `json:"unsafe,omitempty"`
}

// ArchiveListing is the table of contents.
type ArchiveListing struct {
	// Format is zip, tar, tar.gz or gzip (one compressed file, no tar in it).
	Format  string         `json:"format"`
	Entries []ArchiveEntry `json:"entries"`
	// Truncated is a listing that stopped before the end; Note says why.
	Truncated bool   `json:"truncated"`
	Note      string `json:"note,omitempty"`
}

// ArchiveError is an archive the listing could not read at all, with a
// sentence for the person who opened it.
type ArchiveError struct{ Reason string }

func (e *ArchiveError) Error() string { return e.Reason }

func refuse(format string, a ...any) error { return &ArchiveError{Reason: fmt.Sprintf(format, a...)} }

// IsArchive is the archive half of SniffMedia: the leading bytes of a zip, a
// gzip stream, or a POSIX tar.
func IsArchive(head []byte) bool {
	switch {
	case bytes.HasPrefix(head, []byte("PK\x03\x04")), bytes.HasPrefix(head, []byte("PK\x05\x06")):
		return true
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b, 0x08}):
		return true
	case len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar")):
		return true
	}
	return false
}

// ListArchive reads the table of contents of the archive in r. name is the
// file's own name, which a gzip stream with no name of its own is listed by.
func ListArchive(r io.ReaderAt, size int64, name string) (*ArchiveListing, error) {
	head := make([]byte, 512)
	n, _ := r.ReadAt(head, 0)
	head = head[:n]
	switch {
	case bytes.HasPrefix(head, []byte("PK")):
		return listZip(r, size)
	case bytes.HasPrefix(head, []byte{0x1f, 0x8b}):
		return listGzip(r, size, name)
	case len(head) >= 262 && bytes.Equal(head[257:262], []byte("ustar")):
		// A SectionReader seeks, and archive/tar seeks over an entry's bytes
		// when it can: what a plain tar costs is its headers.
		b := &budget{r: io.NewSectionReader(r, 0, size), left: tarSpecialMaxBytes}
		return listTar(b, b, "tar", tarSpecialMaxBytes)
	}
	return nil, refuse("this is not a zip, tar or gzip archive")
}

// budget is a reader that stops after a number of bytes it reads, and passes
// seeks through: a seek is not a byte read.
type budget struct {
	r    io.Reader
	left int64
	hit  bool
}

func (b *budget) Read(p []byte) (int, error) {
	if b.left <= 0 {
		b.hit = true
		return 0, errBudget
	}
	if int64(len(p)) > b.left {
		p = p[:b.left]
	}
	n, err := b.r.Read(p)
	b.left -= int64(n)
	return n, err
}

func (b *budget) Seek(off int64, whence int) (int64, error) {
	if s, ok := b.r.(io.Seeker); ok {
		return s.Seek(off, whence)
	}
	return 0, errors.New("not seekable")
}

var errBudget = errors.New("read budget spent")

// nonSeeking hides Seek, so archive/tar reads (decompresses) through entries
// instead of trying to seek a gzip stream.
type nonSeeking struct{ io.Reader }

func listTar(r io.Reader, b *budget, format string, limit int64) (*ArchiveListing, error) {
	l := &ArchiveListing{Format: format, Entries: []ArchiveEntry{}}
	tr := tar.NewReader(r)
	for {
		h, err := tr.Next()
		if errors.Is(err, io.EOF) {
			return l, nil
		}
		if err != nil {
			if b.hit {
				l.Truncated = true
				l.Note = fmt.Sprintf("Listing stopped after reading %d MB of the archive.", limit>>20)
				return l, nil
			}
			if len(l.Entries) == 0 {
				return nil, refuse("the archive is damaged: %v", err)
			}
			l.Truncated = true
			l.Note = fmt.Sprintf("The archive is damaged after entry %d: %v", len(l.Entries), err)
			return l, nil
		}
		if len(l.Entries) == ArchiveMaxEntries {
			l.Truncated = true
			l.Note = fmt.Sprintf("Only the first %d entries are listed.", ArchiveMaxEntries)
			return l, nil
		}
		e := ArchiveEntry{Path: entryName(h.Name), Size: h.Size, IsDir: h.Typeflag == tar.TypeDir}
		if !h.ModTime.IsZero() {
			e.Modified = h.ModTime.Unix()
		}
		if h.Typeflag != tar.TypeReg && h.Typeflag != tar.TypeRegA && h.Typeflag != tar.TypeGNUSparse {
			e.Size = 0
		}
		e.Unsafe = unsafeName(h.Name)
		l.Entries = append(l.Entries, e)
	}
}

// listGzip is a compressed tar, or one compressed file. Which is decided from
// the first 512 bytes inflated: a tar header's checksum holds or it does not.
func listGzip(r io.ReaderAt, size int64, name string) (*ArchiveListing, error) {
	zr, err := gzip.NewReader(io.NewSectionReader(r, 0, size))
	if err != nil {
		return nil, refuse("the gzip stream is damaged: %v", err)
	}
	zr.Multistream(false)
	block := make([]byte, 512)
	n, err := io.ReadFull(zr, block)
	if err == nil && tarHeader(block[:n]) {
		zr, err = gzip.NewReader(io.NewSectionReader(r, 0, size))
		if err != nil {
			return nil, refuse("the gzip stream is damaged: %v", err)
		}
		b := &budget{r: zr, left: tgzMaxBytes}
		return listTar(nonSeeking{b}, b, "tar.gz", tgzMaxBytes)
	}
	if err != nil && !errors.Is(err, io.EOF) && !errors.Is(err, io.ErrUnexpectedEOF) {
		return nil, refuse("the gzip stream is damaged: %v", err)
	}
	// One file. Its name is the one the stream recorded, else the file's
	// without .gz; its size is not given, because gzip keeps it only modulo
	// 4 GiB at the end of the stream, and a stream of several members keeps
	// the last member's.
	inner := zr.Name
	if inner == "" {
		inner = strings.TrimSuffix(strings.TrimSuffix(name, ".gz"), ".GZ")
	}
	e := ArchiveEntry{Path: entryName(inner), Size: -1, Unsafe: unsafeName(inner)}
	if !zr.ModTime.IsZero() {
		e.Modified = zr.ModTime.Unix()
	}
	return &ArchiveListing{
		Format: "gzip", Entries: []ArchiveEntry{e},
		Note: "One compressed file. gzip does not record its uncompressed size dependably, so none is given.",
	}, nil
}

// tarHeader is whether a block is a tar header: its checksum, which is the
// sum of the header's bytes with the checksum field read as spaces, matches
// the octal number in that field. This is what an old V7 tar with no "ustar"
// has too.
func tarHeader(b []byte) bool {
	if len(b) < 512 || b[0] == 0 {
		return false
	}
	field := strings.TrimRight(strings.TrimSpace(string(bytes.TrimRight(b[148:156], "\x00 "))), "\x00")
	want, err := strconv.ParseInt(field, 8, 64)
	if err != nil {
		return false
	}
	var sum int64
	for i, c := range b[:512] {
		if i >= 148 && i < 156 {
			c = ' '
		}
		sum += int64(c)
	}
	return sum == want
}

// listZip reads the central directory, and only that: the end record, then
// the directory's records one after another, never a local header or an
// entry's data. archive/zip is not used because it reads the whole directory
// into memory before handing back the first entry, and a directory is what
// the archive says it is.
func listZip(r io.ReaderAt, size int64) (*ArchiveListing, error) {
	if size < 22 {
		return nil, refuse("the zip is damaged: too short to hold its directory")
	}
	tailLen := min(size, 22+65535)
	tail := make([]byte, tailLen)
	if _, err := r.ReadAt(tail, size-tailLen); err != nil && !errors.Is(err, io.EOF) {
		return nil, refuse("the zip could not be read: %v", err)
	}
	i := bytes.LastIndex(tail, []byte("PK\x05\x06"))
	for i >= 0 && i+22 > len(tail) {
		i = bytes.LastIndex(tail[:i], []byte("PK\x05\x06"))
	}
	if i < 0 {
		return nil, refuse("the zip is damaged: it has no end-of-directory record")
	}
	end := size - tailLen + int64(i)
	eocd := tail[i:]
	records := uint64(binary.LittleEndian.Uint16(eocd[10:]))
	dirSize := uint64(binary.LittleEndian.Uint32(eocd[12:]))
	dirOff := uint64(binary.LittleEndian.Uint32(eocd[16:]))
	if records == 0xffff || dirSize == 0xffffffff || dirOff == 0xffffffff {
		// Zip64: a locator 20 bytes before the end record names the zip64 end
		// record, which holds the 64-bit numbers.
		loc := make([]byte, 20)
		if end < 20 {
			return nil, refuse("the zip is damaged: its zip64 locator is missing")
		}
		if _, err := r.ReadAt(loc, end-20); err != nil || !bytes.HasPrefix(loc, []byte("PK\x06\x07")) {
			return nil, refuse("the zip is damaged: its zip64 locator is missing")
		}
		at := binary.LittleEndian.Uint64(loc[8:])
		if at > uint64(end) || uint64(end)-at < 56 {
			return nil, refuse("the zip is damaged: its zip64 record points outside the file")
		}
		rec := make([]byte, 56)
		if _, err := r.ReadAt(rec, int64(at)); err != nil || !bytes.HasPrefix(rec, []byte("PK\x06\x06")) {
			return nil, refuse("the zip is damaged: its zip64 record is missing")
		}
		records = binary.LittleEndian.Uint64(rec[32:])
		dirSize = binary.LittleEndian.Uint64(rec[40:])
		dirOff = binary.LittleEndian.Uint64(rec[48:])
		end = int64(at)
	}
	// The directory has to fit in the file before the end record. A claim it
	// does not -- a gigabyte of directory in a file of a few hundred bytes --
	// is refused here, before anything is read on the strength of it.
	if dirSize > uint64(end) || dirOff > uint64(end)-dirSize {
		return nil, refuse("the zip is damaged: its directory claims %d bytes at offset %d, and the file has %d", dirSize, dirOff, size)
	}
	// Bytes in front of the archive (a self-extracting stub) move the whole
	// thing along; the directory still ends where the end record begins.
	start := end - int64(dirSize)

	l := &ArchiveListing{Format: "zip", Entries: []ArchiveEntry{}}
	readLen := min(int64(dirSize), zipDirMaxBytes)
	br := bufio.NewReaderSize(io.NewSectionReader(r, start, readLen), 64<<10)
	fixed := make([]byte, 46)
	for n := uint64(0); n < records; n++ {
		if len(l.Entries) == ArchiveMaxEntries {
			l.Truncated = true
			l.Note = fmt.Sprintf("Only the first %d of %d entries are listed.", ArchiveMaxEntries, records)
			return l, nil
		}
		if _, err := io.ReadFull(br, fixed); err != nil {
			return zipStopped(l, readLen < int64(dirSize), err)
		}
		if !bytes.HasPrefix(fixed, []byte("PK\x01\x02")) {
			return zipStopped(l, false, errors.New("a directory record is not where the previous one ended"))
		}
		nameLen := int(binary.LittleEndian.Uint16(fixed[28:]))
		extraLen := int(binary.LittleEndian.Uint16(fixed[30:]))
		commentLen := int(binary.LittleEndian.Uint16(fixed[32:]))
		nameExtra := make([]byte, nameLen+extraLen)
		if _, err := io.ReadFull(br, nameExtra); err != nil {
			return zipStopped(l, readLen < int64(dirSize), err)
		}
		if _, err := br.Discard(commentLen); err != nil {
			return zipStopped(l, readLen < int64(dirSize), err)
		}
		raw := string(nameExtra[:nameLen])
		e := ArchiveEntry{
			Path:   entryName(raw),
			Size:   int64(binary.LittleEndian.Uint32(fixed[24:])),
			IsDir:  strings.HasSuffix(raw, "/"),
			Unsafe: unsafeName(raw),
		}
		e.Modified = dosTime(binary.LittleEndian.Uint16(fixed[14:]), binary.LittleEndian.Uint16(fixed[12:]))
		zipExtra(nameExtra[nameLen:], &e, binary.LittleEndian.Uint32(fixed[24:]) == 0xffffffff)
		l.Entries = append(l.Entries, e)
	}
	return l, nil
}

// zipStopped is a directory that ended before its record count did: damaged,
// or longer than the bound.
func zipStopped(l *ArchiveListing, bounded bool, err error) (*ArchiveListing, error) {
	if bounded {
		l.Truncated = true
		l.Note = fmt.Sprintf("Listing stopped after reading %d MB of the archive's directory.", zipDirMaxBytes>>20)
		return l, nil
	}
	if len(l.Entries) == 0 {
		return nil, refuse("the zip is damaged: %v", err)
	}
	l.Truncated = true
	l.Note = fmt.Sprintf("The zip's directory is damaged after entry %d.", len(l.Entries))
	return l, nil
}

// zipExtra reads the two extra fields a listing needs: zip64's real size, and
// the Unix timestamp that replaces the DOS one.
func zipExtra(b []byte, e *ArchiveEntry, size64 bool) {
	for len(b) >= 4 {
		id := binary.LittleEndian.Uint16(b)
		n := int(binary.LittleEndian.Uint16(b[2:]))
		if n > len(b)-4 {
			return
		}
		body := b[4 : 4+n]
		switch {
		case id == 0x0001 && size64 && len(body) >= 8:
			if s := binary.LittleEndian.Uint64(body); s <= 1<<62 {
				e.Size = int64(s)
			}
		case id == 0x5455 && len(body) >= 5 && body[0]&1 != 0:
			e.Modified = int64(binary.LittleEndian.Uint32(body[1:]))
		}
		b = b[4+n:]
	}
}

// dosTime is a zip's own timestamp, two-second resolution and no zone.
func dosTime(date, tm uint16) int64 {
	if date == 0 {
		return 0
	}
	return time.Date(int(date>>9)+1980, time.Month(date>>5&0xf), int(date&0x1f),
		int(tm>>11), int(tm>>5&0x3f), int(tm&0x1f)*2, 0, time.UTC).Unix()
}

// entryName is a name as text: an archive's names are bytes, in whatever
// encoding the tool that wrote them used.
func entryName(s string) string { return strings.ToValidUTF8(s, "�") }

// unsafeName is a name an extractor could be led outside its directory by, or
// that is a different path on another system.
func unsafeName(s string) bool {
	if strings.HasPrefix(s, "/") || strings.Contains(s, "\\") {
		return true
	}
	if len(s) >= 2 && s[1] == ':' && (s[0]|0x20 >= 'a' && s[0]|0x20 <= 'z') {
		return true
	}
	for _, part := range strings.Split(s, "/") {
		if part == ".." {
			return true
		}
	}
	return false
}

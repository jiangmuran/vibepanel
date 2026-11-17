package browse

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

// counted is a ReaderAt that counts what is read from it: the bounds below are
// claims about bytes read, and this is how they are measured rather than
// believed.
type counted struct {
	r io.ReaderAt
	n atomic.Int64
}

func (c *counted) ReadAt(p []byte, off int64) (int, error) {
	n, err := c.r.ReadAt(p, off)
	c.n.Add(int64(n))
	return n, err
}

func list(t *testing.T, b []byte, name string) (*ArchiveListing, error, int64) {
	t.Helper()
	c := &counted{r: bytes.NewReader(b)}
	l, err := ListArchive(c, int64(len(b)), name)
	return l, err, c.n.Load()
}

func TestArchiveListsTheFixtures(t *testing.T) {
	dir := filepath.Join("..", "..", "web", "scripts", "fixtures", "preview", "files", "archives")
	for name, want := range map[string]struct {
		format  string
		entries string
	}{
		"bundle.zip":    {"zip", "a.txt 2 false|dir/b.txt 2 false"},
		"bundle.tar":    {"tar", "inside.txt 17 false"},
		"bundle.tar.gz": {"tar.gz", "inside.txt 17 false"},
		"log.gz":        {"gzip", "log -1 false"},
	} {
		b, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if !IsArchive(b[:min(512, len(b))]) {
			t.Errorf("%s: not sniffed as an archive", name)
		}
		l, err, _ := list(t, b, name)
		if err != nil {
			t.Errorf("%s: %v", name, err)
			continue
		}
		var got []string
		for _, e := range l.Entries {
			got = append(got, fmt.Sprintf("%s %d %v", e.Path, e.Size, e.IsDir))
		}
		if l.Format != want.format || strings.Join(got, "|") != want.entries || l.Truncated {
			t.Errorf("%s: %s %q truncated=%v, want %s %q", name, l.Format, strings.Join(got, "|"), l.Truncated, want.format, want.entries)
		}
	}
}

func zipOf(t *testing.T, names ...string) []byte {
	t.Helper()
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	for _, n := range names {
		w, err := z.CreateHeader(&zip.FileHeader{Name: n, Method: zip.Store, Modified: time.Unix(1700000000, 0)})
		if err != nil {
			t.Fatal(err)
		}
		_, _ = w.Write([]byte(n))
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// A zip's end record says how big its directory is and where it starts. One
// that claims a gigabyte in a file of a few hundred bytes is refused with a
// sentence, having read the end record and nothing on the strength of the
// claim.
func TestAZipThatClaimsAGigabyteIsRefusedUnread(t *testing.T) {
	b := zipOf(t, "a.txt", "b.txt")
	eocd := bytes.LastIndex(b, []byte("PK\x05\x06"))
	for name, patch := range map[string]func([]byte){
		"size":   func(b []byte) { binary.LittleEndian.PutUint32(b[eocd+12:], 1<<30) },
		"offset": func(b []byte) { binary.LittleEndian.PutUint32(b[eocd+16:], 1<<30) },
	} {
		bad := bytes.Clone(b)
		patch(bad)
		l, err, read := list(t, bad, "x.zip")
		var ae *ArchiveError
		if !errors.As(err, &ae) || !strings.Contains(ae.Reason, "directory claims") {
			t.Errorf("%s: %+v %v, want refused for its claim", name, l, err)
		}
		// The 512-byte sniff and the tail the end record is found in.
		if read > 512+int64(len(bad)) {
			t.Errorf("%s: read %d bytes of a %d-byte file", name, read, len(bad))
		}
	}
	// And the zip64 version: a locator pointing at a record that claims the
	// directory is a gigabyte.
	z64 := bytes.Clone(b)
	binary.LittleEndian.PutUint32(z64[eocd+12:], 0xffffffff)
	rec := make([]byte, 56)
	copy(rec, "PK\x06\x06")
	binary.LittleEndian.PutUint64(rec[32:], 1<<40)
	binary.LittleEndian.PutUint64(rec[40:], 1<<30)
	binary.LittleEndian.PutUint64(rec[48:], 0)
	loc := make([]byte, 20)
	copy(loc, "PK\x06\x07")
	binary.LittleEndian.PutUint64(loc[8:], uint64(eocd))
	z64 = append(append(append(z64[:eocd:eocd], rec...), loc...), z64[eocd:]...)
	if _, err, _ := list(t, z64, "x.zip"); err == nil || !strings.Contains(err.Error(), "directory claims") {
		t.Errorf("zip64: %v, want refused for its claim", err)
	}
}

// Ten thousand entries and a line saying there were more.
func TestAZipListingStopsAtTenThousand(t *testing.T) {
	names := make([]string, ArchiveMaxEntries+50)
	for i := range names {
		names[i] = fmt.Sprintf("f%05d", i)
	}
	l, err, _ := list(t, zipOf(t, names...), "many.zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != ArchiveMaxEntries || !l.Truncated || !strings.Contains(l.Note, "10000 of 10050") {
		t.Errorf("%d entries, truncated=%v, note %q", len(l.Entries), l.Truncated, l.Note)
	}
}

// Zip bomb shapes: an entry that inflates to a terabyte, and a zip inside a
// zip. Listed, so harmless -- and the test is that the bytes read are the
// directory's, not the entries': the megabyte of stored data in front of
// them is never touched.
func TestAZipBombIsListedFromItsDirectoryAlone(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	pad, _ := z.CreateHeader(&zip.FileHeader{Name: "padding.bin", Method: zip.Store})
	_, _ = pad.Write(bytes.Repeat([]byte{0xa5}, 1<<20))
	inner := zipOf(t, "deeper.txt")
	nested, _ := z.CreateHeader(&zip.FileHeader{Name: "nested.zip", Method: zip.Store})
	_, _ = nested.Write(inner)
	// Raw, so the header can say a terabyte without anybody compressing one.
	bomb, err := z.CreateRaw(&zip.FileHeader{Name: "bomb.bin", Method: zip.Deflate,
		CompressedSize64: 2, UncompressedSize64: 1 << 40})
	if err != nil {
		t.Fatal(err)
	}
	_, _ = bomb.Write([]byte{0x03, 0x00})
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	b := buf.Bytes()
	l, err, read := list(t, b, "bomb.zip")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 3 || l.Entries[2].Path != "bomb.bin" || l.Entries[2].Size != 1<<40 {
		t.Fatalf("%+v", l.Entries)
	}
	if bound := int64(512 + 22 + 65535 + 64<<10); read > bound {
		t.Errorf("read %d bytes of a %d-byte zip listing three entries; the bound is %d", read, len(b), bound)
	}
}

// A directory past the read bound -- here, entries whose extra fields are
// each 60 KB -- is listed as far as the bound and says it stopped.
func TestAZipDirectoryIsReadOnlyToItsBound(t *testing.T) {
	var buf bytes.Buffer
	z := zip.NewWriter(&buf)
	extra := make([]byte, 60000)
	binary.LittleEndian.PutUint16(extra, 0xcafe)
	binary.LittleEndian.PutUint16(extra[2:], uint16(len(extra)-4))
	n := zipDirMaxBytes/len(extra) + 20
	for i := 0; i < n; i++ {
		if _, err := z.CreateHeader(&zip.FileHeader{Name: fmt.Sprintf("e%d", i), Method: zip.Store, Extra: extra}); err != nil {
			t.Fatal(err)
		}
	}
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	l, err, read := list(t, buf.Bytes(), "wide.zip")
	if err != nil {
		t.Fatal(err)
	}
	if !l.Truncated || len(l.Entries) >= n || !strings.Contains(l.Note, "MB of the archive's directory") {
		t.Errorf("%d of %d entries, truncated=%v, note %q", len(l.Entries), n, l.Truncated, l.Note)
	}
	if bound := int64(512 + 22 + 65535 + zipDirMaxBytes); read > bound {
		t.Errorf("read %d bytes; the bound is %d", read, bound)
	}
}

func tarOf(t *testing.T, hs ...*tar.Header) []byte {
	t.Helper()
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for _, h := range hs {
		if h.Typeflag == 0 {
			h.Typeflag = tar.TypeReg
		}
		h.Format = tar.FormatPAX
		if err := tw.WriteHeader(h); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(bytes.Repeat([]byte{'x'}, int(h.Size)))
	}
	if err := tw.Close(); err != nil {
		t.Fatal(err)
	}
	return buf.Bytes()
}

// Names that climb out of the directory they would be extracted into are
// listed as the archive has them, and marked.
func TestATarWhosePathsClimbIsListedAndMarked(t *testing.T) {
	names := map[string]bool{
		"../../etc/passwd": true,
		"/etc/shadow":      true,
		`dir\..\..\x.txt`:  true,
		"C:/Windows/x.dll": true,
		"a/../../b":        true,
		"ok/fine.txt":      false,
		"..dots-are-fine":  false,
	}
	var hs []*tar.Header
	for n := range names {
		hs = append(hs, &tar.Header{Name: n, Size: 1, Mode: 0o644})
	}
	l, err, _ := list(t, tarOf(t, hs...), "climb.tar")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != len(names) {
		t.Fatalf("%d entries", len(l.Entries))
	}
	for _, e := range l.Entries {
		want, ok := names[e.Path]
		if !ok {
			t.Errorf("%q is not a name the archive holds; listings show names as they are", e.Path)
		} else if e.Unsafe != want {
			t.Errorf("%q unsafe=%v, want %v", e.Path, e.Unsafe, want)
		}
	}
}

// A plain tar is listed by its headers: the reader seeks over an entry's
// bytes, so four megabytes of entry cost the next header and nothing else.
func TestATarIsListedFromItsHeaders(t *testing.T) {
	b := tarOf(t,
		&tar.Header{Name: "big.bin", Size: 4 << 20, Mode: 0o644, ModTime: time.Unix(1700000000, 0)},
		&tar.Header{Name: "d/", Typeflag: tar.TypeDir, Mode: 0o755},
		&tar.Header{Name: "after.txt", Size: 3, Mode: 0o644})
	l, err, read := list(t, b, "big.tar")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != 3 || l.Entries[0].Size != 4<<20 || l.Entries[0].Modified != 1700000000 || !l.Entries[1].IsDir || l.Entries[2].Path != "after.txt" {
		t.Fatalf("%+v", l.Entries)
	}
	if read > 64<<10 {
		t.Errorf("read %d bytes of a %d-byte tar to list three headers", read, len(b))
	}
}

// A header that claims a gigabyte of entry in a file that has none: listed
// up to there and marked damaged, with the gigabyte never read.
func TestATarEntryThatClaimsAGigabyteIsNotRead(t *testing.T) {
	b := tarOf(t, &tar.Header{Name: "first.txt", Size: 5, Mode: 0o644}, &tar.Header{Name: "huge.bin", Size: 0, Mode: 0o644})
	// Rewrite the second header's size to a gigabyte, checksum and all.
	h := b[1024:1536]
	if !bytes.HasPrefix(h, []byte("huge.bin")) {
		// PAX records may sit in between; find the header by its name.
		i := bytes.Index(b[512:], []byte("huge.bin"))
		h = b[512+i-(i%512) : 512+i-(i%512)+512]
	}
	copy(h[124:136], fmt.Sprintf("%011o\x00", 1<<30))
	copy(h[148:156], "        ")
	sum := 0
	for _, c := range h {
		sum += int(c)
	}
	copy(h[148:156], fmt.Sprintf("%06o\x00 ", sum))
	l, err, read := list(t, b, "claims.tar")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) < 1 || l.Entries[0].Path != "first.txt" {
		t.Fatalf("%+v", l)
	}
	if read > int64(len(b))+512 {
		t.Errorf("read %d bytes of a %d-byte tar", read, len(b))
	}
}

func gz(t *testing.T, b []byte) []byte {
	t.Helper()
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	_, _ = w.Write(b)
	_ = w.Close()
	return buf.Bytes()
}

// A compressed tar cannot be seeked through, so it is read to the next
// header by inflating -- and that stops at tgzMaxBytes. A hundred megabytes
// of zeros in a few hundred kilobytes is listed as far as the bound, says so,
// and the entry after it is not reached.
func TestACompressedTarBombStopsAtItsBound(t *testing.T) {
	var raw bytes.Buffer
	tw := tar.NewWriter(&raw)
	_ = tw.WriteHeader(&tar.Header{Name: "zeros.bin", Size: tgzMaxBytes + 32<<20, Mode: 0o644, Typeflag: tar.TypeReg})
	_, _ = tw.Write(make([]byte, tgzMaxBytes+32<<20))
	_ = tw.WriteHeader(&tar.Header{Name: "beyond.txt", Size: 1, Mode: 0o644, Typeflag: tar.TypeReg})
	_, _ = tw.Write([]byte("x"))
	_ = tw.Close()
	b := gz(t, raw.Bytes())
	start := time.Now()
	l, err, _ := list(t, b, "bomb.tar.gz")
	if err != nil {
		t.Fatal(err)
	}
	if l.Format != "tar.gz" || !l.Truncated || len(l.Entries) != 1 || !strings.Contains(l.Note, "64 MB") {
		t.Errorf("%s truncated=%v %+v note %q", l.Format, l.Truncated, l.Entries, l.Note)
	}
	t.Logf("%d compressed bytes, listed in %v", len(b), time.Since(start))
}

// A gzip stream with no tar inside is one file, named after the stream or
// the file without .gz, with no size because gzip does not keep one it can
// be held to.
func TestAGzipThatIsNotATarIsOneFile(t *testing.T) {
	var buf bytes.Buffer
	w := gzip.NewWriter(&buf)
	w.Name = "recorded.log"
	w.ModTime = time.Unix(1700000000, 0)
	_, _ = w.Write(bytes.Repeat([]byte("log line\n"), 100))
	_ = w.Close()
	l, err, _ := list(t, buf.Bytes(), "whatever.gz")
	if err != nil {
		t.Fatal(err)
	}
	if l.Format != "gzip" || len(l.Entries) != 1 || l.Entries[0].Path != "recorded.log" ||
		l.Entries[0].Size != -1 || l.Entries[0].Modified != 1700000000 || !strings.Contains(l.Note, "size") {
		t.Errorf("%+v", l)
	}
}

// Damaged files are refused with a sentence, not a 500 and not a listing of
// nothing.
func TestADamagedArchiveIsRefusedWithASentence(t *testing.T) {
	good := zipOf(t, "a.txt")
	for name, b := range map[string][]byte{
		"zip with no end":       append([]byte("PK\x03\x04"), bytes.Repeat([]byte{1}, 300)...),
		"zip, directory moved":  bytes.Clone(good),
		"gzip of noise":         append([]byte{0x1f, 0x8b, 0x08, 0}, bytes.Repeat([]byte{0xff}, 100)...),
		"tar, damaged header":   append(bytes.Repeat([]byte{'z'}, 257), append([]byte("ustar\x0000"), bytes.Repeat([]byte{'z'}, 600)...)...),
		"not an archive at all": []byte("hello"),
	} {
		if name == "zip, directory moved" {
			// The directory's offset pointed one byte late: the record is
			// not where the end record says.
			eocd := len(b) - 22
			off := binary.LittleEndian.Uint32(b[eocd+16:])
			size := binary.LittleEndian.Uint32(b[eocd+12:])
			binary.LittleEndian.PutUint32(b[eocd+16:], off-1)
			binary.LittleEndian.PutUint32(b[eocd+12:], size+1)
		}
		_, err, _ := list(t, b, "x")
		var ae *ArchiveError
		if !errors.As(err, &ae) || ae.Reason == "" {
			t.Errorf("%s: %v, want an ArchiveError with a sentence", name, err)
		}
	}
}

// What SniffMedia calls these, and what SniffMagic still does not.
func TestArchivesAndDatabasesAreSniffed(t *testing.T) {
	tarHead := make([]byte, 512)
	copy(tarHead, "x.txt")
	copy(tarHead[257:], "ustar\x0000")
	for name, c := range map[string]struct {
		head []byte
		kind Kind
	}{
		"zip":       {[]byte("PK\x03\x04\x14\x00"), KindArchive},
		"empty zip": {[]byte("PK\x05\x06\x00\x00"), KindArchive},
		"gzip":      {[]byte{0x1f, 0x8b, 0x08, 0x00}, KindArchive},
		"tar":       {tarHead, KindArchive},
		"sqlite":    {[]byte("SQLite format 3\x00\x10\x00"), KindSQLite},
		"PK text":   {[]byte("PKG_CONFIG_PATH=/usr/lib\n"), KindBinary},
	} {
		if k, mime := SniffMedia(c.head, int64(len(c.head))); k != c.kind || mime != "" {
			t.Errorf("%s: %s %q, want %s and no media type", name, k, mime, c.kind)
		}
	}
}

// The same ten thousand for a tar.
func TestATarListingStopsAtTenThousand(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	for i := 0; i < ArchiveMaxEntries+1; i++ {
		_ = tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Mode: 0o644, Typeflag: tar.TypeReg, Format: tar.FormatUSTAR})
	}
	_ = tw.Close()
	l, err, _ := list(t, buf.Bytes(), "many.tar")
	if err != nil {
		t.Fatal(err)
	}
	if len(l.Entries) != ArchiveMaxEntries || !l.Truncated {
		t.Errorf("%d entries, truncated=%v", len(l.Entries), l.Truncated)
	}
}

// What a plain tar's headers make the reader read rather than seek: PAX
// records, up to a megabyte each, as many as the archive likes. Bounded in
// total, and the listing says it stopped.
func TestATarsHeadersAreReadOnlyToTheirBound(t *testing.T) {
	var buf bytes.Buffer
	tw := tar.NewWriter(&buf)
	long := strings.Repeat("c", 1000*1000)
	n := tarSpecialMaxBytes/len(long) + 4
	for i := 0; i < n; i++ {
		_ = tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("f%d", i), Mode: 0o644, Typeflag: tar.TypeReg,
			Format: tar.FormatPAX, PAXRecords: map[string]string{"comment": long}})
	}
	_ = tw.Close()
	l, err, read := list(t, buf.Bytes(), "pax.tar")
	if err != nil {
		t.Fatal(err)
	}
	if !l.Truncated || len(l.Entries) >= n || !strings.Contains(l.Note, "16 MB") {
		t.Errorf("%d of %d entries, truncated=%v, note %q", len(l.Entries), n, l.Truncated, l.Note)
	}
	if bound := int64(512 + tarSpecialMaxBytes + 64<<10); read > bound {
		t.Errorf("read %d bytes; the bound is %d", read, bound)
	}
}

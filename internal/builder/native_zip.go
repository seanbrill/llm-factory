package builder

// A ZIP writer for native packages. archive/zip can't write what they need:
// a stored (uncompressed) entry of more than 4 GiB, the model, whose local
// header other tools can read. Its CreateRaw leaves the ZIP64 sizes out of the
// local header (`unzip -t` then reports a bad CRC), and its streaming writer
// puts them in a data descriptor after the data, which tools that read an
// archive front to back can't handle for stored entries. This writer knows
// every entry's CRC-32 and sizes before writing it, so each local header
// carries them, with a ZIP64 extra field whenever a size or offset reaches
// 4 GiB (APPNOTE 4.5.3), and never uses data descriptors.
//
// Entries keep Unix permissions (the executable bit of the launcher, the
// start script and llama.cpp's programs) and symbolic links (llama.cpp's
// libraries on macOS and Linux), which macOS Archive Utility, ditto, unzip and
// bsdtar all restore.

import (
	"bytes"
	"compress/flate"
	"encoding/binary"
	"errors"
	"fmt"
	"hash/crc32"
	"io"
	"io/fs"
	"strings"
	"time"
	"unicode/utf8"
)

const (
	zipStore   = 0
	zipDeflate = 8

	zipLocalSig     = 0x04034b50
	zipCentralSig   = 0x02014b50
	zipEnd64Sig     = 0x06064b50
	zipEnd64LocSig  = 0x07064b50
	zipEndSig       = 0x06054b50
	zip64ExtraID    = 0x0001
	zipTimeExtraID  = 0x5455 // Info-ZIP extended timestamp (Unix mtime)
	zipFlagUTF8     = 0x0800
	zipMadeByUnix   = 3<<8 | 63 // Unix, spec 6.3
	zipVersion20    = 20
	zipVersion45    = 45 // ZIP64
	zipMax32        = 0xffffffff
	zipMax16        = 0xffff
	zipMsdosDirAttr = 0x10
)

// zipWriter writes an archive to w. It counts the bytes it writes, so entry
// offsets are known without seeking.
type zipWriter struct {
	w       io.Writer
	off     uint64
	entries []zipEntry
	force64 bool // tests: ZIP64 fields on every entry
	closed  bool
}

type zipEntry struct {
	name      string
	method    uint16
	crc       uint32
	csize     uint64
	usize     uint64
	offset    uint64
	mode      fs.FileMode
	mtime     time.Time
	zip64Size bool
}

func newZipWriter(w io.Writer) *zipWriter { return &zipWriter{w: w} }

func (z *zipWriter) write(b []byte) error {
	n, err := z.w.Write(b)
	z.off += uint64(n)
	return err
}

func checkZipName(name string) error {
	if name == "" || strings.HasPrefix(name, "/") || strings.Contains(name, `\`) || !utf8.ValidString(name) || len(name) > zipMax16 {
		return fmt.Errorf("zip: bad entry name %q", name)
	}
	for _, part := range strings.Split(strings.TrimSuffix(name, "/"), "/") {
		if part == "" || part == "." || part == ".." {
			return fmt.Errorf("zip: bad entry name %q", name)
		}
	}
	return nil
}

// dosTime is the MS-DOS date and time fields (local time, 2-second steps).
func dosTime(t time.Time) (date, tim uint16) {
	if t.Year() < 1980 {
		t = time.Date(1980, 1, 1, 0, 0, 0, 0, time.Local)
	}
	date = uint16(t.Day() + int(t.Month())<<5 + (t.Year()-1980)<<9)
	tim = uint16(t.Second()/2 + t.Minute()<<5 + t.Hour()<<11)
	return
}

// unixMode is the external attribute's high half: file type and permissions.
func unixMode(m fs.FileMode) uint32 {
	perm := uint32(m.Perm())
	switch {
	case m&fs.ModeSymlink != 0:
		return 0o120000 | perm
	case m.IsDir():
		return 0o040000 | perm
	default:
		return 0o100000 | perm
	}
}

// begin writes e's local header. e's sizes and CRC must be final.
func (z *zipWriter) begin(e *zipEntry) error {
	if z.closed {
		return errors.New("zip: writer closed")
	}
	if err := checkZipName(e.name); err != nil {
		return err
	}
	e.offset = z.off
	e.zip64Size = z.force64 || e.usize >= zipMax32 || e.csize >= zipMax32
	version := uint16(zipVersion20)
	if e.zip64Size || e.offset >= zipMax32 {
		version = zipVersion45
	}
	date, tim := dosTime(e.mtime)
	var extra bytes.Buffer
	csize32, usize32 := uint32(e.csize), uint32(e.usize)
	if e.zip64Size {
		csize32, usize32 = zipMax32, zipMax32
		le(&extra, uint16(zip64ExtraID), uint16(16), e.usize, e.csize)
	}
	le(&extra, uint16(zipTimeExtraID), uint16(5), uint8(1), uint32(e.mtime.Unix()))
	var h bytes.Buffer
	le(&h, uint32(zipLocalSig), version, uint16(zipFlagUTF8), e.method, tim, date,
		e.crc, csize32, usize32, uint16(len(e.name)), uint16(extra.Len()))
	h.WriteString(e.name)
	h.Write(extra.Bytes())
	if err := z.write(h.Bytes()); err != nil {
		return err
	}
	z.entries = append(z.entries, *e)
	return nil
}

// le appends values little-endian.
func le(b *bytes.Buffer, vals ...any) {
	for _, v := range vals {
		_ = binary.Write(b, binary.LittleEndian, v)
	}
}

// AddDir adds a folder entry ("name/").
func (z *zipWriter) AddDir(name string, mtime time.Time) error {
	name = strings.TrimSuffix(name, "/") + "/"
	return z.begin(&zipEntry{name: name, method: zipStore, mode: fs.ModeDir | 0o755, mtime: mtime})
}

// AddSymlink adds a symbolic link to target (a name relative to the link).
func (z *zipWriter) AddSymlink(name, target string, mtime time.Time) error {
	data := []byte(target)
	e := &zipEntry{name: name, method: zipStore, crc: crc32.ChecksumIEEE(data),
		csize: uint64(len(data)), usize: uint64(len(data)), mode: fs.ModeSymlink | 0o777, mtime: mtime}
	if err := z.begin(e); err != nil {
		return err
	}
	return z.write(data)
}

// AddBytes adds a small file, deflated when that makes it smaller.
func (z *zipWriter) AddBytes(name string, data []byte, mode fs.FileMode, mtime time.Time) error {
	e := &zipEntry{name: name, method: zipStore, crc: crc32.ChecksumIEEE(data),
		usize: uint64(len(data)), csize: uint64(len(data)), mode: mode.Perm(), mtime: mtime}
	body := data
	if len(data) > 64 {
		var c bytes.Buffer
		fw, _ := flate.NewWriter(&c, flate.DefaultCompression)
		_, _ = fw.Write(data)
		_ = fw.Close()
		if c.Len() < len(data) {
			e.method, e.csize, body = zipDeflate, uint64(c.Len()), c.Bytes()
		}
	}
	if err := z.begin(e); err != nil {
		return err
	}
	return z.write(body)
}

// AddStored adds a file stored as it is (the model weights), copied from r,
// whose size and CRC-32 the caller has worked out beforehand. Each block
// copied is also written to tee (for hashing) when it isn't nil. The copy is
// checked against size and crc, so a file that changed in between fails.
func (z *zipWriter) AddStored(name string, r io.Reader, size int64, crc uint32, mode fs.FileMode, mtime time.Time, tee io.Writer) error {
	e := &zipEntry{name: name, method: zipStore, crc: crc, usize: uint64(size), csize: uint64(size), mode: mode.Perm(), mtime: mtime}
	if err := z.begin(e); err != nil {
		return err
	}
	h := crc32.NewIEEE()
	dst := io.MultiWriter(writerFunc(z.write), h)
	if tee != nil {
		dst = io.MultiWriter(dst, tee)
	}
	n, err := io.Copy(dst, io.LimitReader(r, size+1))
	if err != nil {
		return err
	}
	if n != size || h.Sum32() != crc {
		return fmt.Errorf("zip: %s changed while it was being packed (%d of %d bytes, CRC %08x, expected %08x)", name, n, size, h.Sum32(), crc)
	}
	return nil
}

type writerFunc func([]byte) error

func (f writerFunc) Write(p []byte) (int, error) {
	if err := f(p); err != nil {
		return 0, err
	}
	return len(p), nil
}

// Close writes the central directory and the end records.
func (z *zipWriter) Close() error {
	if z.closed {
		return errors.New("zip: writer closed twice")
	}
	z.closed = true
	start := z.off
	for _, e := range z.entries {
		offset64 := z.force64 || e.offset >= zipMax32
		var extra bytes.Buffer
		if e.zip64Size || offset64 {
			var fields bytes.Buffer
			if e.zip64Size {
				le(&fields, e.usize, e.csize)
			}
			if offset64 {
				le(&fields, e.offset)
			}
			le(&extra, uint16(zip64ExtraID), uint16(fields.Len()))
			extra.Write(fields.Bytes())
		}
		le(&extra, uint16(zipTimeExtraID), uint16(5), uint8(1), uint32(e.mtime.Unix()))
		csize32, usize32, off32 := uint32(e.csize), uint32(e.usize), uint32(e.offset)
		if e.zip64Size {
			csize32, usize32 = zipMax32, zipMax32
		}
		if offset64 {
			off32 = zipMax32
		}
		version := uint16(zipVersion20)
		if e.zip64Size || offset64 {
			version = zipVersion45
		}
		ext := unixMode(e.mode) << 16
		if e.mode.IsDir() {
			ext |= zipMsdosDirAttr
		}
		date, tim := dosTime(e.mtime)
		var h bytes.Buffer
		le(&h, uint32(zipCentralSig), uint16(zipMadeByUnix), version, uint16(zipFlagUTF8), e.method, tim, date,
			e.crc, csize32, usize32, uint16(len(e.name)), uint16(extra.Len()), uint16(0), uint16(0), uint16(0), ext, off32)
		h.WriteString(e.name)
		h.Write(extra.Bytes())
		if err := z.write(h.Bytes()); err != nil {
			return err
		}
	}
	end := z.off
	size := end - start
	count := uint64(len(z.entries))
	if z.force64 || count >= zipMax16 || size >= zipMax32 || start >= zipMax32 {
		var r bytes.Buffer
		le(&r, uint32(zipEnd64Sig), uint64(44), uint16(zipMadeByUnix), uint16(zipVersion45), uint32(0), uint32(0),
			count, count, size, start)
		le(&r, uint32(zipEnd64LocSig), uint32(0), end, uint32(1))
		if err := z.write(r.Bytes()); err != nil {
			return err
		}
	}
	var r bytes.Buffer
	le(&r, uint32(zipEndSig), uint16(0), uint16(0),
		uint16(min(count, zipMax16)), uint16(min(count, zipMax16)),
		uint32(min(size, zipMax32)), uint32(min(start, zipMax32)), uint16(0))
	return z.write(r.Bytes())
}

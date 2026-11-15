package builder

import (
	"archive/zip"
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// localEntry is what a front-to-back reader sees in a local file header.
type localEntry struct {
	name         string
	method       uint16
	crc          uint32
	csize, usize uint64
}

// walkLocal reads an archive the way a streaming unzipper does: local header,
// data, next local header, using only what each local header says.
func walkLocal(t *testing.T, data []byte) []localEntry {
	t.Helper()
	var out []localEntry
	off := 0
	for off+4 <= len(data) && binary.LittleEndian.Uint32(data[off:]) == zipLocalSig {
		h := data[off:]
		flags := binary.LittleEndian.Uint16(h[6:])
		if flags&0x8 != 0 {
			t.Fatalf("entry at %d uses a data descriptor", off)
		}
		e := localEntry{
			method: binary.LittleEndian.Uint16(h[8:]),
			crc:    binary.LittleEndian.Uint32(h[14:]),
			csize:  uint64(binary.LittleEndian.Uint32(h[18:])),
			usize:  uint64(binary.LittleEndian.Uint32(h[22:])),
		}
		nlen := int(binary.LittleEndian.Uint16(h[26:]))
		xlen := int(binary.LittleEndian.Uint16(h[28:]))
		e.name = string(h[30 : 30+nlen])
		extra := h[30+nlen : 30+nlen+xlen]
		for len(extra) >= 4 {
			id, size := binary.LittleEndian.Uint16(extra), int(binary.LittleEndian.Uint16(extra[2:]))
			body := extra[4 : 4+size]
			if id == zip64ExtraID {
				if e.usize != zipMax32 || e.csize != zipMax32 || size != 16 {
					t.Fatalf("%s: zip64 extra without 0xffffffff sizes (%d %d, %d bytes)", e.name, e.csize, e.usize, size)
				}
				e.usize, e.csize = binary.LittleEndian.Uint64(body), binary.LittleEndian.Uint64(body[8:])
			}
			extra = extra[4+size:]
		}
		out = append(out, e)
		off += 30 + nlen + xlen + int(e.csize)
	}
	if off+4 > len(data) || binary.LittleEndian.Uint32(data[off:]) != zipCentralSig {
		t.Fatalf("no central directory after the last local entry (offset %d)", off)
	}
	return out
}

func TestZipWriterRoundTrip(t *testing.T) {
	for _, force64 := range []bool{false, true} {
		var buf bytes.Buffer
		z := newZipWriter(&buf)
		z.force64 = force64
		now := time.Date(2026, 11, 15, 10, 30, 0, 0, time.Local)
		readme := []byte(strings.Repeat("Start it with Start.command.\n", 20))
		script := []byte("#!/bin/sh\nexec ./bin/llmpack \"$@\"\n")
		weights := bytes.Repeat([]byte{0, 1, 2, 3, 4, 5, 6, 7}, 4096)
		steps := []error{
			z.AddDir("pkg", now),
			z.AddDir("pkg/bin/", now),
			z.AddBytes("pkg/README.txt", readme, 0o644, now),
			z.AddBytes("pkg/Start.command", script, 0o755, now),
			z.AddSymlink("pkg/bin/libllama.0.dylib", "libllama.0.29.0.dylib", now),
			z.AddStored("pkg/model/m.gguf", bytes.NewReader(weights), int64(len(weights)), crc32.ChecksumIEEE(weights), 0o644, now, nil),
			z.Close(),
		}
		for i, err := range steps {
			if err != nil {
				t.Fatalf("force64=%v step %d: %v", force64, i, err)
			}
		}
		data := buf.Bytes()
		zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
		if err != nil {
			t.Fatalf("force64=%v: archive/zip can't read it: %v", force64, err)
		}
		want := map[string]struct {
			mode fs.FileMode
			body []byte
		}{
			"pkg/":                     {fs.ModeDir | 0o755, nil},
			"pkg/bin/":                 {fs.ModeDir | 0o755, nil},
			"pkg/README.txt":           {0o644, readme},
			"pkg/Start.command":        {0o755, script},
			"pkg/bin/libllama.0.dylib": {fs.ModeSymlink | 0o777, []byte("libllama.0.29.0.dylib")},
			"pkg/model/m.gguf":         {0o644, weights},
		}
		if len(zr.File) != len(want) {
			t.Fatalf("force64=%v: %d entries, want %d", force64, len(zr.File), len(want))
		}
		for _, f := range zr.File {
			w, ok := want[f.Name]
			if !ok {
				t.Errorf("unexpected entry %s", f.Name)
				continue
			}
			if f.Mode() != w.mode {
				t.Errorf("force64=%v %s: mode %v, want %v", force64, f.Name, f.Mode(), w.mode)
			}
			if !f.Modified.Equal(now) {
				t.Errorf("%s: modified %v, want %v", f.Name, f.Modified, now)
			}
			if w.body == nil {
				continue
			}
			rc, err := f.Open()
			if err != nil {
				t.Fatal(err)
			}
			got, err := io.ReadAll(rc) // checks the CRC at the end
			rc.Close()
			if err != nil || !bytes.Equal(got, w.body) {
				t.Errorf("force64=%v %s: read %d bytes, %v", force64, f.Name, len(got), err)
			}
		}
		if zr.File[2].Method != zip.Deflate || zr.File[5].Method != zip.Store {
			t.Errorf("methods: README %d, weights %d", zr.File[2].Method, zr.File[5].Method)
		}
		// Front to back, every local header has the real sizes and CRC.
		local := walkLocal(t, data)
		if len(local) != len(zr.File) {
			t.Fatalf("%d local entries, %d central", len(local), len(zr.File))
		}
		for i, e := range local {
			f := zr.File[i]
			if e.name != f.Name || e.crc != f.CRC32 || e.csize != f.CompressedSize64 || e.usize != f.UncompressedSize64 {
				t.Errorf("force64=%v local %+v != central %s %08x %d %d", force64, e, f.Name, f.CRC32, f.CompressedSize64, f.UncompressedSize64)
			}
		}
		if force64 && !bytes.Contains(data, []byte{0x50, 0x4b, 0x06, 0x06}) {
			t.Error("force64: no ZIP64 end record")
		}
	}
}

func TestZipWriterChecksTheCopy(t *testing.T) {
	z := newZipWriter(io.Discard)
	data := []byte("weights")
	if err := z.AddStored("m.gguf", bytes.NewReader(data), int64(len(data)), crc32.ChecksumIEEE(data)+1, 0o644, time.Now(), nil); err == nil {
		t.Error("a CRC mismatch was accepted")
	}
	if err := z.AddStored("n.gguf", bytes.NewReader(data), int64(len(data))+3, crc32.ChecksumIEEE(data), 0o644, time.Now(), nil); err == nil {
		t.Error("a short file was accepted")
	}
	var tee bytes.Buffer
	if err := z.AddStored("o.gguf", bytes.NewReader(data), int64(len(data)), crc32.ChecksumIEEE(data), 0o644, time.Now(), &tee); err != nil || tee.String() != "weights" {
		t.Errorf("tee = %q, %v", tee.String(), err)
	}
}

func TestZipWriterRejectsBadNames(t *testing.T) {
	for _, name := range []string{"", "/abs", "a/../b", "../x", `a\b`, "a//b", "./a"} {
		z := newZipWriter(io.Discard)
		if err := z.AddBytes(name, []byte("x"), 0o644, time.Now()); err == nil {
			t.Errorf("%q accepted", name)
		}
	}
	z := newZipWriter(io.Discard)
	if err := z.Close(); err != nil {
		t.Fatal(err)
	}
	if err := z.AddDir("late", time.Now()); err == nil {
		t.Error("an entry after Close was accepted")
	}
}

// patternReader yields n bytes of a repeating pattern (position mod 251).
type patternReader struct {
	block []byte
	pos   int64
	n     int64
}

func newPatternReader(n int64) *patternReader {
	b := make([]byte, 251*4096)
	for i := range b {
		b[i] = byte(i % 251)
	}
	return &patternReader{block: b, n: n}
}

func (p *patternReader) Read(buf []byte) (int, error) {
	if p.pos >= p.n {
		return 0, io.EOF
	}
	off := int(p.pos % int64(len(p.block)))
	k := copy(buf, p.block[off:])
	if rest := p.n - p.pos; int64(k) > rest {
		k = int(rest)
	}
	p.pos += int64(k)
	return k, nil
}

// TestZipWriterBigEntry writes a package-shaped archive with a stored entry
// over 4 GiB, and a file after it, into $NATIVE_ZIP_BIG_DIR, for checking
// with other tools (unzip -t, ditto -x -k, bsdtar). It needs ~4.6 GB free.
func TestZipWriterBigEntry(t *testing.T) {
	dir := os.Getenv("NATIVE_ZIP_BIG_DIR")
	if dir == "" {
		t.Skip("set NATIVE_ZIP_BIG_DIR to write a >4 GiB archive")
	}
	const size = 4<<30 + 300<<20
	h := crc32.NewIEEE()
	if _, err := io.Copy(h, newPatternReader(size)); err != nil {
		t.Fatal(err)
	}
	f, err := os.Create(filepath.Join(dir, "big.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	z := newZipWriter(f)
	now := time.Now()
	for i, err := range []error{
		z.AddDir("big", now),
		z.AddBytes("big/README.txt", []byte(strings.Repeat("readme\n", 30)), 0o644, now),
		z.AddStored("big/model/big.gguf", newPatternReader(size), size, h.Sum32(), 0o644, now, nil),
		z.AddBytes("big/after.txt", []byte("after the weights\n"), 0o755, now),
		z.AddSymlink("big/link.txt", "after.txt", now),
		z.Close(),
	} {
		if err != nil {
			t.Fatalf("step %d: %v", i, err)
		}
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(filepath.Join(dir, "big.zip"))
	if err != nil {
		t.Fatal(err)
	}
	defer zr.Close()
	last := zr.File[len(zr.File)-2]
	rc, err := last.Open()
	if err != nil {
		t.Fatal(err)
	}
	got, _ := io.ReadAll(rc)
	rc.Close()
	if last.Name != "big/after.txt" || string(got) != "after the weights\n" {
		t.Errorf("after the big entry: %s %q", last.Name, got)
	}
	if zr.File[2].UncompressedSize64 != size {
		t.Errorf("big entry size %d", zr.File[2].UncompressedSize64)
	}
}

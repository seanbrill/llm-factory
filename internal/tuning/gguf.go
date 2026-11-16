package tuning

// A GGUF header reader: the metadata key/values only, never the tensors. It
// is what the memory estimate needs to size the KV cache (layers, KV heads,
// head sizes, the sliding window) and to cap the context at what the model
// was trained for.

import (
	"bufio"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
)

// GGUF value types (gguf.h).
const (
	ggufUint8 = iota
	ggufInt8
	ggufUint16
	ggufInt16
	ggufUint32
	ggufInt32
	ggufFloat32
	ggufBool
	ggufString
	ggufArray
	ggufUint64
	ggufInt64
	ggufFloat64
)

// Limits that keep a corrupt or hostile header from allocating much.
const (
	maxGGUFKeys      = 1 << 16
	maxGGUFString    = 1 << 20 // one string value or key
	maxGGUFArrayKeep = 4096    // longer arrays are skipped (counted, not kept)
)

// ErrNotGGUF is returned for a file that doesn't start with the GGUF magic.
var ErrNotGGUF = errors.New("not a GGUF file")

// GGUFMeta is a GGUF file's metadata: scalars as uint64/int64/float64/bool/
// string, short arrays as []any, and the length of every array (long ones are
// only counted).
type GGUFMeta struct {
	Version uint32
	Tensors uint64
	KV      map[string]any
	ArrLen  map[string]uint64
}

// ReadGGUFMetaFile reads the metadata of the GGUF file at path.
func ReadGGUFMetaFile(path string) (*GGUFMeta, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	return ReadGGUFMeta(f)
}

// ReadGGUFMeta reads GGUF metadata from the start of r, stopping before the
// tensor infos.
func ReadGGUFMeta(r io.Reader) (*GGUFMeta, error) { return ReadGGUFMetaUntil(r, nil) }

// ReadGGUFMetaUntil is ReadGGUFMeta that stops early (with what it has read)
// when stop, asked before each key, says so: a remote header read stops at
// the tokenizer, which is most of the header's bytes.
func ReadGGUFMetaUntil(r io.Reader, stop func(key string, m *GGUFMeta) bool) (*GGUFMeta, error) {
	br := bufio.NewReaderSize(r, 64<<10)
	p := &ggufParser{r: br}
	var magic [4]byte
	if _, err := io.ReadFull(br, magic[:]); err != nil {
		return nil, fmt.Errorf("gguf: %w", err)
	}
	if string(magic[:]) != "GGUF" {
		return nil, ErrNotGGUF
	}
	m := &GGUFMeta{KV: map[string]any{}, ArrLen: map[string]uint64{}}
	m.Version = p.u32()
	if p.err != nil {
		return nil, p.err
	}
	if m.Version == 0 || m.Version > 3 {
		// A big-endian file reads as a huge version; v1 used 32-bit counts.
		return nil, fmt.Errorf("gguf: unsupported version %d", m.Version)
	}
	var nKV uint64
	if m.Version == 1 {
		m.Tensors, nKV = uint64(p.u32()), uint64(p.u32())
	} else {
		m.Tensors, nKV = p.u64(), p.u64()
	}
	if p.err != nil {
		return nil, p.err
	}
	if nKV > maxGGUFKeys {
		return nil, fmt.Errorf("gguf: %d metadata keys", nKV)
	}
	for i := uint64(0); i < nKV; i++ {
		key := p.str(m.Version)
		if p.err != nil {
			return nil, p.err
		}
		if stop != nil && stop(key, m) {
			return m, nil
		}
		typ := p.u32()
		if p.err != nil {
			return nil, p.err
		}
		if typ == ggufArray {
			et := p.u32()
			n := p.count(m.Version)
			if p.err != nil {
				return nil, p.err
			}
			m.ArrLen[key] = n
			if n <= maxGGUFArrayKeep {
				arr := make([]any, 0, n)
				for j := uint64(0); j < n; j++ {
					arr = append(arr, p.value(et, m.Version))
				}
				m.KV[key] = arr
			} else {
				for j := uint64(0); j < n && p.err == nil; j++ {
					p.skip(et, m.Version)
				}
			}
		} else {
			m.KV[key] = p.value(typ, m.Version)
		}
		if p.err != nil {
			return nil, fmt.Errorf("gguf: key %q: %w", key, p.err)
		}
	}
	return m, nil
}

type ggufParser struct {
	r   *bufio.Reader
	err error
	buf [8]byte
}

func (p *ggufParser) read(n int) []byte {
	if p.err != nil {
		return p.buf[:n]
	}
	if _, err := io.ReadFull(p.r, p.buf[:n]); err != nil {
		p.err = err
	}
	return p.buf[:n]
}

func (p *ggufParser) u32() uint32 { return binary.LittleEndian.Uint32(p.read(4)) }
func (p *ggufParser) u64() uint64 { return binary.LittleEndian.Uint64(p.read(8)) }

func (p *ggufParser) count(version uint32) uint64 {
	if version == 1 {
		return uint64(p.u32())
	}
	return p.u64()
}

func (p *ggufParser) str(version uint32) string {
	n := p.count(version)
	if p.err != nil {
		return ""
	}
	if n > maxGGUFString {
		p.err = fmt.Errorf("a %d-byte string", n)
		return ""
	}
	b := make([]byte, n)
	if _, err := io.ReadFull(p.r, b); err != nil {
		p.err = err
	}
	return string(b)
}

func (p *ggufParser) value(typ, version uint32) any {
	switch typ {
	case ggufUint8:
		return uint64(p.read(1)[0])
	case ggufInt8:
		return int64(int8(p.read(1)[0]))
	case ggufUint16:
		return uint64(binary.LittleEndian.Uint16(p.read(2)))
	case ggufInt16:
		return int64(int16(binary.LittleEndian.Uint16(p.read(2))))
	case ggufUint32:
		return uint64(p.u32())
	case ggufInt32:
		return int64(int32(p.u32()))
	case ggufFloat32:
		return float64(math.Float32frombits(p.u32()))
	case ggufBool:
		return p.read(1)[0] != 0
	case ggufString:
		return p.str(version)
	case ggufUint64:
		return p.u64()
	case ggufInt64:
		return int64(p.u64())
	case ggufFloat64:
		return math.Float64frombits(p.u64())
	case ggufArray:
		// Nested arrays don't occur in practice; skip one rather than fail.
		et := p.u32()
		n := p.count(version)
		for j := uint64(0); j < n && p.err == nil; j++ {
			p.skip(et, version)
		}
		return nil
	}
	if p.err == nil {
		p.err = fmt.Errorf("unknown value type %d", typ)
	}
	return nil
}

func (p *ggufParser) skip(typ, version uint32) {
	var n int
	switch typ {
	case ggufUint8, ggufInt8, ggufBool:
		n = 1
	case ggufUint16, ggufInt16:
		n = 2
	case ggufUint32, ggufInt32, ggufFloat32:
		n = 4
	case ggufUint64, ggufInt64, ggufFloat64:
		n = 8
	case ggufString:
		l := p.count(version)
		if p.err != nil {
			return
		}
		if l > maxGGUFString {
			p.err = fmt.Errorf("a %d-byte string", l)
			return
		}
		if _, err := p.r.Discard(int(l)); err != nil {
			p.err = err
		}
		return
	default:
		p.value(typ, version)
		return
	}
	if p.err != nil {
		return
	}
	if _, err := p.r.Discard(n); err != nil {
		p.err = err
	}
}

// Uint returns an integer metadata value (any integer type, or the first
// element of an integer array).
func (m *GGUFMeta) Uint(key string) (uint64, bool) {
	switch v := m.KV[key].(type) {
	case uint64:
		return v, true
	case int64:
		if v >= 0 {
			return uint64(v), true
		}
	case []any:
		if len(v) > 0 {
			switch e := v[0].(type) {
			case uint64:
				return e, true
			case int64:
				if e >= 0 {
					return uint64(e), true
				}
			}
		}
	}
	return 0, false
}

// String returns a string metadata value.
func (m *GGUFMeta) String(key string) string {
	s, _ := m.KV[key].(string)
	return s
}

// Arch is general.architecture ("llama", "gemma3", "gpt-oss", ...).
func (m *GGUFMeta) Arch() string { return m.String("general.architecture") }

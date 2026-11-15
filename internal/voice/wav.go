package voice

import (
	"encoding/binary"
	"errors"
	"fmt"
	"math"
)

// Clip length limits for a voice reference.
const (
	MinClipSeconds = 3.0
	MaxClipSeconds = 30.0
	// STTRate is the sample rate whisper.cpp wants.
	STTRate = 16000
)

// WAV is a parsed RIFF/WAVE file.
type WAV struct {
	SampleRate int
	Channels   int
	Bits       int  // bits per sample
	Float      bool // IEEE float samples (else integer PCM)
	Frames     int  // samples per channel
	data       []byte
}

// Seconds is the clip's duration.
func (w WAV) Seconds() float64 {
	if w.SampleRate <= 0 {
		return 0
	}
	return float64(w.Frames) / float64(w.SampleRate)
}

var errNotWAV = errors.New("that isn't a WAV file (RIFF/WAVE)")

// ParseWAV reads a RIFF/WAVE file holding integer PCM (8, 16, 24 or 32 bit) or
// 32-bit float samples, plain or WAVE_FORMAT_EXTENSIBLE.
func ParseWAV(b []byte) (WAV, error) {
	if len(b) < 12 || string(b[0:4]) != "RIFF" || string(b[8:12]) != "WAVE" {
		return WAV{}, errNotWAV
	}
	var w WAV
	var format uint16
	haveFmt := false
	for off := 12; off+8 <= len(b); {
		id := string(b[off : off+4])
		size := int(binary.LittleEndian.Uint32(b[off+4 : off+8]))
		body := off + 8
		end := body + size
		if size < 0 || end > len(b) {
			end = len(b) // a streamed WAV with an unknown (or wrong) size: take what's there
		}
		switch id {
		case "fmt ":
			if end-body < 16 {
				return WAV{}, errors.New("the WAV's format chunk is too short")
			}
			f := b[body:end]
			format = binary.LittleEndian.Uint16(f[0:2])
			w.Channels = int(binary.LittleEndian.Uint16(f[2:4]))
			w.SampleRate = int(binary.LittleEndian.Uint32(f[4:8]))
			w.Bits = int(binary.LittleEndian.Uint16(f[14:16]))
			if format == 0xFFFE && len(f) >= 26 { // WAVE_FORMAT_EXTENSIBLE: the sub-format GUID starts with the real tag
				format = binary.LittleEndian.Uint16(f[24:26])
			}
			haveFmt = true
		case "data":
			w.data = b[body:end]
		}
		if size%2 == 1 {
			size++ // chunks are word-aligned
		}
		off = body + size
	}
	if !haveFmt {
		return WAV{}, errors.New("the WAV has no format chunk")
	}
	switch {
	case format == 1 && (w.Bits == 8 || w.Bits == 16 || w.Bits == 24 || w.Bits == 32):
	case format == 3 && w.Bits == 32:
		w.Float = true
	default:
		return WAV{}, fmt.Errorf("the WAV holds format %d at %d bits; use 16-bit PCM", format, w.Bits)
	}
	if w.Channels < 1 || w.SampleRate < 1000 || w.SampleRate > 384000 {
		return WAV{}, fmt.Errorf("the WAV's header is damaged (%d channels at %d Hz)", w.Channels, w.SampleRate)
	}
	if w.data == nil {
		return WAV{}, errors.New("the WAV has no audio data")
	}
	frame := w.Channels * w.Bits / 8
	w.Frames = len(w.data) / frame
	w.data = w.data[:w.Frames*frame]
	return w, nil
}

// Mono decodes the samples to float32 in [-1, 1], averaging the channels.
func (w WAV) Mono() []float32 {
	out := make([]float32, w.Frames)
	bps := w.Bits / 8
	frame := bps * w.Channels
	for i := 0; i < w.Frames; i++ {
		var sum float64
		for c := 0; c < w.Channels; c++ {
			p := w.data[i*frame+c*bps:]
			sum += sample(p, w.Bits, w.Float)
		}
		out[i] = float32(sum / float64(w.Channels))
	}
	return out
}

func sample(p []byte, bits int, isFloat bool) float64 {
	switch {
	case isFloat:
		return float64(math.Float32frombits(binary.LittleEndian.Uint32(p)))
	case bits == 8:
		return (float64(p[0]) - 128) / 128
	case bits == 16:
		return float64(int16(binary.LittleEndian.Uint16(p))) / 32768
	case bits == 24:
		v := int32(uint32(p[0])<<8|uint32(p[1])<<16|uint32(p[2])<<24) >> 8
		return float64(v) / 8388608
	default:
		return float64(int32(binary.LittleEndian.Uint32(p))) / 2147483648
	}
}

// Resample converts mono samples between rates by linear interpolation: plenty
// for speech going to a recognizer or a voice encoder.
func Resample(in []float32, from, to int) []float32 {
	if from == to || len(in) == 0 || from <= 0 || to <= 0 {
		return in
	}
	n := int(math.Round(float64(len(in)) * float64(to) / float64(from)))
	out := make([]float32, n)
	step := float64(from) / float64(to)
	for i := range out {
		pos := float64(i) * step
		j := int(pos)
		if j >= len(in)-1 {
			out[i] = in[len(in)-1]
			continue
		}
		f := float32(pos - float64(j))
		out[i] = in[j]*(1-f) + in[j+1]*f
	}
	return out
}

// EncodePCM16 writes mono samples as a 16-bit PCM WAV.
func EncodePCM16(samples []float32, rate int) []byte {
	data := len(samples) * 2
	b := make([]byte, 44+data)
	copy(b[0:], "RIFF")
	binary.LittleEndian.PutUint32(b[4:], uint32(36+data))
	copy(b[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(b[16:], 16)
	binary.LittleEndian.PutUint16(b[20:], 1) // PCM
	binary.LittleEndian.PutUint16(b[22:], 1) // mono
	binary.LittleEndian.PutUint32(b[24:], uint32(rate))
	binary.LittleEndian.PutUint32(b[28:], uint32(rate*2))
	binary.LittleEndian.PutUint16(b[32:], 2)
	binary.LittleEndian.PutUint16(b[34:], 16)
	copy(b[36:], "data")
	binary.LittleEndian.PutUint32(b[40:], uint32(data))
	for i, s := range samples {
		v := math.Max(-1, math.Min(1, float64(s)))
		binary.LittleEndian.PutUint16(b[44+i*2:], uint16(int16(math.Round(v*32767))))
	}
	return b
}

// ToSTT returns the WAV as 16 kHz mono 16-bit PCM, the shape whisper.cpp reads
// whatever its version. A file that already has that shape comes back as is.
func ToSTT(b []byte) ([]byte, error) {
	w, err := ParseWAV(b)
	if err != nil {
		return nil, err
	}
	if w.SampleRate == STTRate && w.Channels == 1 && w.Bits == 16 && !w.Float {
		return b, nil
	}
	return EncodePCM16(Resample(w.Mono(), w.SampleRate, STTRate), STTRate), nil
}

// CheckClip validates a voice reference clip: a mono WAV of 3–30 s. Errors are
// sentences for the person who recorded it.
func CheckClip(b []byte) (WAV, error) {
	w, err := ParseWAV(b)
	if err != nil {
		return WAV{}, fmt.Errorf("the clip can't be read: %v", err)
	}
	if w.Channels != 1 {
		return WAV{}, fmt.Errorf("the clip has %d channels; a voice clip must be mono", w.Channels)
	}
	secs := w.Seconds()
	const slack = 0.05 // encoders round the length
	if secs < MinClipSeconds-slack {
		return WAV{}, fmt.Errorf("the clip is %.1f s; record at least %.0f s of speech", secs, MinClipSeconds)
	}
	if secs > MaxClipSeconds+slack {
		return WAV{}, fmt.Errorf("the clip is %.1f s; keep it to %.0f s or less", secs, MaxClipSeconds)
	}
	return w, nil
}

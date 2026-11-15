package voice

import (
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"math"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

// wav builds a WAV: bits 16/24/32 PCM, or 32 float.
func wav(rate, channels, bits int, float bool, secs float64) []byte {
	n := int(float64(rate) * secs)
	bps := bits / 8
	data := make([]byte, n*channels*bps)
	for i := 0; i < n; i++ {
		v := 0.25 * math.Sin(2*math.Pi*300*float64(i)/float64(rate))
		for c := 0; c < channels; c++ {
			off := (i*channels + c) * bps
			switch {
			case float:
				binary.LittleEndian.PutUint32(data[off:], math.Float32bits(float32(v)))
			case bits == 16:
				binary.LittleEndian.PutUint16(data[off:], uint16(int16(v*32767)))
			case bits == 24:
				x := int32(v * 8388607)
				data[off], data[off+1], data[off+2] = byte(x), byte(x>>8), byte(x>>16)
			case bits == 32:
				binary.LittleEndian.PutUint32(data[off:], uint32(int32(v*2147483647)))
			}
		}
	}
	h := make([]byte, 44)
	copy(h, "RIFF")
	binary.LittleEndian.PutUint32(h[4:], uint32(36+len(data)))
	copy(h[8:], "WAVEfmt ")
	binary.LittleEndian.PutUint32(h[16:], 16)
	format := uint16(1)
	if float {
		format = 3
	}
	binary.LittleEndian.PutUint16(h[20:], format)
	binary.LittleEndian.PutUint16(h[22:], uint16(channels))
	binary.LittleEndian.PutUint32(h[24:], uint32(rate))
	binary.LittleEndian.PutUint32(h[28:], uint32(rate*channels*bps))
	binary.LittleEndian.PutUint16(h[32:], uint16(channels*bps))
	binary.LittleEndian.PutUint16(h[34:], uint16(bits))
	copy(h[36:], "data")
	binary.LittleEndian.PutUint32(h[40:], uint32(len(data)))
	return append(h, data...)
}

func TestParseWAVFormats(t *testing.T) {
	for _, c := range []struct {
		bits  int
		float bool
	}{{16, false}, {24, false}, {32, false}, {32, true}} {
		w, err := ParseWAV(wav(22050, 2, c.bits, c.float, 1))
		if err != nil {
			t.Fatalf("%d-bit float=%v: %v", c.bits, c.float, err)
		}
		if w.SampleRate != 22050 || w.Channels != 2 || math.Abs(w.Seconds()-1) > 0.01 {
			t.Errorf("%d-bit: %+v", c.bits, w)
		}
		mono := w.Mono()
		if len(mono) != 22050 {
			t.Errorf("%d-bit: %d mono samples", c.bits, len(mono))
		}
		peak := float32(0)
		for _, s := range mono {
			if s > peak {
				peak = s
			}
		}
		if peak < 0.24 || peak > 0.26 {
			t.Errorf("%d-bit float=%v: peak %v, want 0.25", c.bits, c.float, peak)
		}
	}
	for _, bad := range [][]byte{nil, []byte("RIFF....WAVE"), []byte("ID3 mp3 data here, not a wav at all")} {
		if _, err := ParseWAV(bad); err == nil {
			t.Errorf("parsed %q", bad)
		}
	}
}

func TestToSTT(t *testing.T) {
	out, err := ToSTT(wav(48000, 2, 24, false, 1))
	if err != nil {
		t.Fatal(err)
	}
	w, _ := ParseWAV(out)
	if w.SampleRate != STTRate || w.Channels != 1 || w.Bits != 16 || math.Abs(w.Seconds()-1) > 0.01 {
		t.Errorf("stt = %+v", w)
	}
	in := wav(16000, 1, 16, false, 1)
	if out, _ := ToSTT(in); &out[0] != &in[0] {
		t.Error("an already-16 kHz clip was re-encoded")
	}
}

func TestCheckAndNormalizeClip(t *testing.T) {
	if _, err := CheckClip(wav(16000, 1, 16, false, 2)); err == nil || !strings.Contains(err.Error(), "at least 3 s") {
		t.Errorf("2 s: %v", err)
	}
	if _, err := CheckClip(wav(16000, 1, 16, false, 31)); err == nil || !strings.Contains(err.Error(), "30 s or less") {
		t.Errorf("31 s: %v", err)
	}
	if _, err := CheckClip(wav(16000, 2, 16, false, 5)); err == nil || !strings.Contains(err.Error(), "mono") {
		t.Errorf("stereo: %v", err)
	}
	if _, err := CheckClip([]byte("not audio")); err == nil || !strings.Contains(err.Error(), "can't be read") {
		t.Errorf("garbage: %v", err)
	}
	// 30 s of 96 kHz float is 11.5 MB: stored as 48 kHz 16-bit (2.9 MB).
	big := wav(96000, 1, 32, true, 30)
	out, w, err := NormalizeClip(big)
	if err != nil {
		t.Fatal(err)
	}
	if w.SampleRate != ClipRate || w.Bits != 16 || len(out) > MaxRefBytes || math.Abs(w.Seconds()-30) > 0.05 {
		t.Errorf("normalized = %+v, %d bytes", w, len(out))
	}
	plain := wav(24000, 1, 16, false, 4)
	if out, _, _ := NormalizeClip(plain); len(out) != len(plain) {
		t.Error("a 16-bit 24 kHz clip was re-encoded")
	}
}

func TestFitRef(t *testing.T) {
	small := base64.StdEncoding.EncodeToString(wav(24000, 1, 16, false, 4))
	if got, err := FitRef(small, MaxRefBytes); err != nil || got != small {
		t.Errorf("small clip changed: %v", err)
	}
	big := base64.StdEncoding.EncodeToString(wav(48000, 1, 32, true, 30)) // 5.8 MB
	got, err := FitRef(big, MaxRefBytes)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := base64.StdEncoding.DecodeString(got)
	w, _ := ParseWAV(raw)
	if len(raw) > MaxRefBytes || w.SampleRate != 24000 || w.Bits != 16 {
		t.Errorf("fitted: %d bytes, %+v", len(raw), w)
	}
	if _, err := FitRef(big, 1000); err == nil {
		t.Error("a clip that can't fit was accepted")
	}
}

func TestVoiceNormalizeValidate(t *testing.T) {
	v := Voice{Kind: " Clone ", Name: "x", Clip: " v-0123456789ab "}.Normalize()
	if v.Kind != KindClone || v.Name != "" || v.Clip != "v-0123456789ab" || v.Validate() != nil || v.Label() != "clone" {
		t.Errorf("clone = %+v", v)
	}
	if err := (Voice{Kind: KindClone, Clip: "../etc/passwd"}).Validate(); err == nil {
		t.Error("a bad clip id passed")
	}
	if err := (Voice{Kind: KindBuiltin}).Validate(); err == nil {
		t.Error("a nameless built-in voice passed")
	}
	if err := (Voice{Kind: "robot"}).Validate(); err == nil {
		t.Error("an unknown kind passed")
	}
	if l := (Voice{Kind: KindBuiltin, Name: "alba"}).Label(); l != "builtin:alba" {
		t.Errorf("label = %q", l)
	}
}

func call(t *testing.T, s Speaker, r SpeechRequest) map[string]any {
	t.Helper()
	c, err := For(s).Call(r)
	if err != nil {
		t.Fatalf("%s: %v", s.Runtime, err)
	}
	if c.Path != "/v1/audio/speech" || c.ContentType != "application/json" {
		t.Errorf("call = %s %s", c.Path, c.ContentType)
	}
	var m map[string]any
	if err := json.Unmarshal(c.Body, &m); err != nil {
		t.Fatal(err)
	}
	return m
}

func TestAdapters(t *testing.T) {
	ref := base64.StdEncoding.EncodeToString(wav(24000, 1, 16, false, 4))
	clone := SpeechRequest{Input: "Hello.", RefAudio: ref, RefText: "Reference words."}

	// Piper: input only; no clones.
	if m := call(t, Speaker{Runtime: "cpp", Model: "piper-en-us"}, SpeechRequest{Input: "Hi", Voice: "amy", Speed: 1.2}); len(m) != 1 || m["input"] != "Hi" {
		t.Errorf("piper = %v", m)
	}
	_, err := For(Speaker{Runtime: "", Model: "piper-en-us"}).Call(clone)
	var ce *CloneError
	if !errors.As(err, &ce) || !strings.Contains(err.Error(), "piper-en-us") || !strings.Contains(err.Error(), "can't clone voices") {
		t.Errorf("piper clone: %v", err)
	}

	// Kokoro (pygate): voice and speed.
	if m := call(t, Speaker{Runtime: "python"}, SpeechRequest{Input: "Hi", Voice: "af_bella", Speed: 1.1}); m["voice"] != "af_bella" || m["speed"] != 1.1 || m["ref_audio"] != nil {
		t.Errorf("pygate = %v", m)
	}
	if _, err := For(Speaker{Runtime: "python"}).Call(clone); !errors.As(err, &ce) {
		t.Errorf("kokoro clone: %v", err)
	}

	// audio.cpp: model "tts"; a clone sends voice_ref + reference_text and never voice.
	ac := Speaker{Runtime: "audiocpp", Model: "pocket-tts-en", Clone: true}
	m := call(t, ac, SpeechRequest{Input: "Hi", Voice: "alba", Speed: 1})
	if m["model"] != "tts" || m["voice"] != "alba" || m["speed"] != nil || m["voice_ref"] != nil {
		t.Errorf("audiocpp built-in = %v", m)
	}
	c2 := clone
	c2.Voice = "alba"
	m = call(t, ac, c2)
	ref2, _ := m["voice_ref"].(map[string]any)
	if ref2["type"] != "base64" || ref2["data"] != ref || m["reference_text"] != "Reference words." || m["voice"] != nil {
		t.Errorf("audiocpp clone = %v", m)
	}
	c3 := clone
	c3.RefText = ""
	if m := call(t, ac, c3); m["reference_text"] != nil {
		t.Errorf("an empty transcript was sent: %v", m)
	}
	// Streaming keeps the path open for real-time speech.
	m = call(t, ac, SpeechRequest{Input: "Hi", StreamFormat: "sse", Speed: 1.5})
	if m["stream_format"] != "sse" || m["response_format"] != "pcm" || m["speed"] != 1.5 {
		t.Errorf("audiocpp stream = %v", m)
	}
	// Qwen3-TTS Base: transcript required; no built-in voices.
	qb := Speaker{Runtime: "audiocpp", Model: "qwen3-tts-0.6b-base", Clone: true, NeedsTranscript: true, CloneOnly: true}
	if _, err := For(qb).Call(c3); !errors.As(err, &ce) || ce.Reason != "transcript" {
		t.Errorf("no transcript: %v", err)
	}
	if _, err := For(qb).Call(SpeechRequest{Input: "Hi"}); !errors.As(err, &ce) || ce.Reason != "clip" || !strings.Contains(err.Error(), "no built-in voices") {
		t.Errorf("clone-only without a clip: %v", err)
	}
	call(t, qb, clone)
	// A cloning model that can't clone this request: not audio.cpp.
	if _, err := For(Speaker{Runtime: "audiocpp", Model: "kokoro"}).Call(clone); !errors.As(err, &ce) {
		t.Errorf("non-cloning audio.cpp model: %v", err)
	}

	// The contract runtime passes the request through.
	m = call(t, Speaker{Runtime: "contract", Clone: true}, clone)
	if m["ref_audio"] != ref || m["ref_text"] != "Reference words." || m["response_format"] != "wav" {
		t.Errorf("contract = %v", m)
	}

	// Input checks.
	if _, err := For(ac).Call(SpeechRequest{Input: "  "}); err == nil {
		t.Error("empty input accepted")
	}
	if _, err := For(ac).Call(SpeechRequest{Input: strings.Repeat("a", MaxInputChars+1)}); err == nil {
		t.Error("over-long input accepted")
	}
}

func TestCloneRules(t *testing.T) {
	if !CloneCapable("audiocpp") || CloneCapable("cpp") || CloneCapable("python") {
		t.Error("CloneCapable fallback")
	}
	for _, c := range []struct {
		family, def string
		want        bool
	}{{"xtts_v2", "", true}, {"qwen3_tts", "", true}, {"qwen3_tts", "Ryan", false}, {"pocket_tts", "alba", false}, {"voxcpm2", "", false}} {
		if got := CloneOnly(c.family, c.def); got != c.want {
			t.Errorf("CloneOnly(%s, %q) = %v", c.family, c.def, got)
		}
	}
}

func TestStore(t *testing.T) {
	dir := t.TempDir()
	s := NewStore(dir)
	clip := wav(44100, 1, 24, false, 5)
	if _, err := s.Add("Me", clip, "", false); err == nil || !strings.Contains(err.Error(), ConsentStatement) {
		t.Errorf("no consent: %v", err)
	}
	if _, err := s.Add("Me", wav(16000, 1, 16, false, 1), "", true); err == nil {
		t.Error("a 1 s clip was saved")
	}
	c, err := s.Add("  My   voice ", clip, " Hello there. ", true)
	if err != nil {
		t.Fatal(err)
	}
	if !ValidClipID(c.ID) || c.Name != "My voice" || c.Transcript != "Hello there." || c.Consent != ConsentStatement || c.ConsentAt == 0 ||
		c.SampleRate != 44100 || c.Seconds != 5 {
		t.Errorf("clip = %+v", c)
	}
	audio, err := s.Audio(c.ID)
	if err != nil {
		t.Fatal(err)
	}
	if w, err := ParseWAV(audio); err != nil || w.Bits != 16 || w.SampleRate != 44100 {
		t.Errorf("stored audio = %+v %v (want 16-bit at the clip's rate)", w, err)
	}
	if runtime.GOOS != "windows" {
		for _, p := range []string{s.Dir(), filepath.Join(s.Dir(), c.ID+".wav"), filepath.Join(s.Dir(), c.ID+".json")} {
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			want := os.FileMode(0o600)
			if fi.IsDir() {
				want = 0o700
			}
			if fi.Mode().Perm() != want {
				t.Errorf("%s mode = %v, want %v", filepath.Base(p), fi.Mode().Perm(), want)
			}
		}
	}
	name, tr := "Renamed", "New words."
	if u, err := s.Update(c.ID, &name, &tr); err != nil || u.Name != "Renamed" || u.Transcript != "New words." {
		t.Errorf("update = %+v %v", u, err)
	}
	time := c.CreatedAt
	c2, _ := s.Add("Second", clip, "", true)
	list, err := s.List()
	if err != nil || len(list) != 2 {
		t.Fatalf("list = %v %v", list, err)
	}
	if list[0].CreatedAt < time {
		t.Error("list isn't newest first")
	}
	if err := s.Delete(c.ID); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Get(c.ID); !errors.Is(err, ErrNoClip) {
		t.Errorf("get deleted = %v", err)
	}
	if _, err := s.Audio("../../etc/passwd"); err == nil {
		t.Error("a path as an id was read")
	}
	if err := s.Delete(c2.ID); err != nil {
		t.Fatal(err)
	}
	if list, _ := s.List(); len(list) != 0 {
		t.Errorf("left: %v", list)
	}
}

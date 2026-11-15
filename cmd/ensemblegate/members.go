package main

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

// Members is what the ensemble's members can do, as plain functions: one per
// member kind, each knowing its runtime's real endpoint and request shape. The
// chat handlers call them for a routing Decision; the planned agent router
// will offer them to the chat model as tools.
type Members struct {
	mg   *Manager
	http *http.Client
}

func newMembers(mg *Manager) *Members {
	return &Members{mg: mg, http: &http.Client{Transport: &http.Transport{
		Proxy:               nil, // member traffic never goes through an env proxy
		DialContext:         (&net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
		MaxIdleConnsPerHost: 4,
		IdleConnTimeout:     90 * time.Second,
		DisableCompression:  true, // keep streamed tokens flowing as they arrive
	}}}
}

// Timeouts for generative calls (the request's own context also bounds them).
const (
	imageTimeout = 15 * time.Minute
	videoTimeout = 60 * time.Minute
	sttTimeout   = 5 * time.Minute
	ttsTimeout   = 5 * time.Minute
)

// memberError is a member answering with an error status.
type memberError struct {
	label  string
	status int
	msg    string
}

func (e *memberError) Error() string {
	return fmt.Sprintf("%s answered %d: %s", e.label, e.status, e.msg)
}

// errorText pulls the message out of a member's error body ({"error": "..."},
// {"error": {"message": "..."}}, or plain text).
func errorText(b []byte) string {
	var v struct {
		Error   any    `json:"error"`
		Message string `json:"message"`
		Detail  any    `json:"detail"`
	}
	if json.Unmarshal(b, &v) == nil {
		switch e := v.Error.(type) {
		case string:
			return e
		case map[string]any:
			if m, ok := e["message"].(string); ok {
				return m
			}
		}
		if v.Message != "" {
			return v.Message
		}
		if v.Detail != nil {
			return fmt.Sprint(v.Detail)
		}
	}
	s := strings.TrimSpace(string(b))
	if len(s) > 400 {
		s = s[:400] + "…"
	}
	if s == "" {
		s = "no details"
	}
	return s
}

// post sends a request to a leased member and returns the response when it is
// 200; otherwise a memberError (the body is consumed).
func (ms *Members) post(ctx context.Context, l *Lease, path, contentType string, body []byte) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL(path), bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", contentType)
	resp, err := ms.http.Do(req)
	if err != nil {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s isn't answering: %v", memberLabel(l.Member), err)
	}
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
		resp.Body.Close()
		return nil, &memberError{label: memberLabel(l.Member), status: resp.StatusCode, msg: errorText(b)}
	}
	return resp, nil
}

// ---- chat -----------------------------------------------------------------

// Chat sends an OpenAI chat body to the chat or vision member. The caller
// closes the response and releases the lease.
func (ms *Members) Chat(ctx context.Context, tool string, body map[string]any, progress func(string)) (*http.Response, *Lease, error) {
	l, err := ms.mg.Acquire(ctx, tool, progress)
	if err != nil {
		return nil, nil, err
	}
	b, err := json.Marshal(body)
	if err != nil {
		l.Release()
		return nil, nil, err
	}
	resp, err := ms.post(ctx, l, "/v1/chat/completions", "application/json", b)
	if err != nil {
		l.Release()
		return nil, nil, err
	}
	return resp, l, nil
}

// ChatRaw posts a chat-completions body as is to the chat or vision member and
// returns its response whatever the status. The member stays leased (not
// evictable) until the response body is closed.
func (ms *Members) ChatRaw(ctx context.Context, tool string, body []byte, progress func(string)) (*http.Response, error) {
	l, err := ms.mg.Acquire(ctx, tool, progress)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, l.URL("/v1/chat/completions"), bytes.NewReader(body))
	if err != nil {
		l.Release()
		return nil, err
	}
	req.ContentLength = int64(len(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := ms.http.Do(req)
	if err != nil {
		l.Release()
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		return nil, fmt.Errorf("%s isn't answering: %v", memberLabel(l.Member), err)
	}
	resp.Body = &leasedBody{ReadCloser: resp.Body, lease: l}
	return resp, nil
}

// leasedBody releases its member's lease when closed.
type leasedBody struct {
	io.ReadCloser
	lease *Lease
}

func (b *leasedBody) Close() error {
	err := b.ReadCloser.Close()
	b.lease.Release()
	return err
}

// ChatText runs a non-streaming chat and returns the whole completion and its
// reply text.
func (ms *Members) ChatText(ctx context.Context, tool string, body map[string]any, progress func(string)) (map[string]any, string, error) {
	body["stream"] = false
	resp, l, err := ms.Chat(ctx, tool, body, progress)
	if err != nil {
		return nil, "", err
	}
	defer l.Release()
	defer resp.Body.Close()
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, "", fmt.Errorf("%s sent a reply that isn't JSON: %v", memberLabel(l.Member), err)
	}
	return out, completionText(out), nil
}

// completionText is choices[0].message.content of a chat completion.
func completionText(out map[string]any) string {
	choices, _ := out["choices"].([]any)
	if len(choices) == 0 {
		return ""
	}
	c, _ := choices[0].(map[string]any)
	msg, _ := c["message"].(map[string]any)
	s, _ := msg["content"].(string)
	return s
}

// ---- speech to text ---------------------------------------------------------

// Transcribe turns speech into text with the speech-to-text member: whisper.cpp
// at POST /inference, or an audio.cpp image (Moonshine; model "stt") at the
// OpenAI-style POST /v1/audio/transcriptions, which is also tried when
// /inference answers 404. A WAV is sent as 16 kHz mono first, the shape every
// version of both reads (audio.cpp reads WAV only).
func (ms *Members) Transcribe(ctx context.Context, audio []byte, filename, language string, progress func(string)) (string, error) {
	if b, err := voice.ToSTT(audio); err == nil {
		audio, filename = b, "speech.wav"
	}
	l, err := ms.mg.Acquire(ctx, ensemble.ToolTranscribe, progress)
	if err != nil {
		return "", err
	}
	defer l.Release()
	ctx, cancel := context.WithTimeout(ctx, sttTimeout)
	defer cancel()
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if filename == "" {
		filename = "speech.wav"
	}
	fw, _ := mw.CreateFormFile("file", filename)
	_, _ = fw.Write(audio)
	_ = mw.WriteField("model", "stt") // audio.cpp's id for its model; whisper.cpp ignores it
	_ = mw.WriteField("response_format", "json")
	_ = mw.WriteField("temperature", "0")
	if language != "" {
		_ = mw.WriteField("language", language)
	}
	_ = mw.Close()
	if progress != nil {
		progress("Transcribing…")
	}
	path := "/inference"
	if l.Member.Runtime == "audiocpp" {
		path = "/v1/audio/transcriptions"
	}
	resp, err := ms.post(ctx, l, path, mw.FormDataContentType(), buf.Bytes())
	var me *memberError
	if path == "/inference" && errors.As(err, &me) && me.status == http.StatusNotFound {
		resp, err = ms.post(ctx, l, "/v1/audio/transcriptions", mw.FormDataContentType(), buf.Bytes())
	}
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	var out struct {
		Text string `json:"text"`
	}
	if json.Unmarshal(b, &out) == nil {
		return strings.TrimSpace(out.Text), nil
	}
	return strings.TrimSpace(string(b)), nil
}

// ---- text to speech (the voice contract) ----------------------------------

// SpeakStream asks the speech member to say req (already resolved to a voice)
// and returns the audio as it arrives. The caller closes body and calls done.
func (ms *Members) SpeakStream(ctx context.Context, req voice.SpeechRequest, progress func(string)) (body io.ReadCloser, mime string, done func(), err error) {
	m, ok := ms.mg.Member(ensemble.ToolSpeak)
	if !ok {
		return nil, "", nil, &missingError{ensemble.ToolSpeak}
	}
	call, err := voice.For(speakerOf(m)).Call(req)
	if err != nil {
		return nil, "", nil, err // a CloneError or bad input, before starting anything
	}
	l, err := ms.mg.Acquire(ctx, ensemble.ToolSpeak, progress)
	if err != nil {
		return nil, "", nil, err
	}
	ctx, cancel := context.WithTimeout(ctx, ttsTimeout)
	if progress != nil {
		progress("Speaking…")
	}
	resp, err := ms.post(ctx, l, call.Path, call.ContentType, call.Body)
	if err != nil {
		cancel()
		l.Release()
		return nil, "", nil, err
	}
	mime = resp.Header.Get("Content-Type")
	if mime == "" || strings.HasPrefix(mime, "application/octet-stream") {
		mime = "audio/wav"
	}
	return resp.Body, mime, func() { cancel(); l.Release() }, nil
}

// speakerOf is the adapters' view of a speech member.
func speakerOf(m ensemble.Member) voice.Speaker {
	return voice.Speaker{Runtime: m.Runtime, Model: m.Model, Clone: m.VoiceClone,
		NeedsTranscript: m.CloneNeedsTranscript, CloneOnly: m.CloneOnly}
}

// Speak is SpeakStream collected into a data URL.
func (ms *Members) Speak(ctx context.Context, req voice.SpeechRequest, progress func(string)) (ensemble.Media, error) {
	body, mime, done, err := ms.SpeakStream(ctx, req, progress)
	if err != nil {
		return ensemble.Media{}, err
	}
	defer done()
	defer body.Close()
	b, err := io.ReadAll(io.LimitReader(body, 64<<20))
	if err != nil {
		return ensemble.Media{}, fmt.Errorf("the speech member's audio was cut off: %v", err)
	}
	if strings.HasPrefix(mime, "application/json") || strings.HasPrefix(mime, "text/") {
		return ensemble.Media{}, fmt.Errorf("the speech member sent no audio: %s", errorText(b))
	}
	return ensemble.Media{Type: "audio", MIME: mime, URL: "data:" + mime + ";base64," + base64.StdEncoding.EncodeToString(b)}, nil
}

// ---- image generation (stable-diffusion.cpp sd-server) --------------------

// imageSettings are sampling defaults per image model: distilled models want
// few steps and CFG 1 (mirrors the UI's imageDefaults).
var imageSettings = map[string]struct {
	steps int
	cfg   float64
	size  int
}{
	"z-image-turbo":      {8, 1, 512},
	"flux.2-klein-4b":    {4, 1, 512},
	"flux.3-image-klein": {16, 1, 512},
	"sdxl-turbo":         {6, 1.5, 512},
	"sdxl-base":          {25, 7, 768},
	"sd-1.5":             {20, 7, 512},
}

// GenerateImage draws prompt with the image member.
func (ms *Members) GenerateImage(ctx context.Context, prompt string, progress func(string)) (ensemble.Media, error) {
	l, err := ms.mg.Acquire(ctx, ensemble.ToolGenerateImage, progress)
	if err != nil {
		return ensemble.Media{}, err
	}
	defer l.Release()
	s, ok := imageSettings[l.Member.Model]
	if !ok {
		s = imageSettings["sdxl-turbo"]
	}
	body, _ := json.Marshal(map[string]any{
		"prompt": prompt, "steps": s.steps, "cfg_scale": s.cfg, "width": s.size, "height": s.size, "seed": -1,
	})
	ctx, cancel := context.WithTimeout(ctx, imageTimeout)
	defer cancel()
	if progress != nil {
		progress("Generating the image…")
	}
	resp, err := ms.post(ctx, l, "/sdapi/v1/txt2img", "application/json", body)
	if err != nil {
		return ensemble.Media{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Images []string `json:"images"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || len(out.Images) == 0 || out.Images[0] == "" {
		return ensemble.Media{}, errors.New("the image member finished without an image")
	}
	url := out.Images[0]
	if !strings.HasPrefix(url, "data:") {
		url = "data:image/png;base64," + url
	}
	return ensemble.Media{Type: "image", MIME: dataMIME(url, "image/png"), URL: url}, nil
}

// ---- video generation (videogate: POST /generate) --------------------------

// GenerateVideo makes a short clip with the video member: 480p on a GPU, a
// smaller, shorter clip on CPU (where video takes a very long time).
func (ms *Members) GenerateVideo(ctx context.Context, prompt string, progress func(string)) (ensemble.Media, error) {
	l, err := ms.mg.Acquire(ctx, ensemble.ToolGenerateVideo, progress)
	if err != nil {
		return ensemble.Media{}, err
	}
	defer l.Release()
	req := map[string]any{"prompt": prompt, "width": 832, "height": 480, "frames": 33, "steps": 10}
	if ms.mg.gpuFor(l.Member) == "" {
		req["width"], req["height"], req["frames"] = 416, 240, 17
	}
	body, _ := json.Marshal(req)
	ctx, cancel := context.WithTimeout(ctx, videoTimeout)
	defer cancel()
	if progress != nil {
		progress("Generating the video (this takes minutes)…")
	}
	resp, err := ms.post(ctx, l, "/generate", "application/json", body)
	if err != nil {
		return ensemble.Media{}, err
	}
	defer resp.Body.Close()
	var out struct {
		Video string `json:"video"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || out.Video == "" {
		return ensemble.Media{}, errors.New("the video member finished without a video")
	}
	return ensemble.Media{Type: "video", MIME: dataMIME(out.Video, "video/webm"), URL: out.Video}, nil
}

// dataMIME reads the MIME type of a data URL.
func dataMIME(url, def string) string {
	if strings.HasPrefix(url, "data:") {
		if i := strings.IndexAny(url, ";,"); i > 5 {
			return url[5:i]
		}
	}
	return def
}

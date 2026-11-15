package server

import (
	"bytes"
	"encoding/json"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strconv"
	"testing"
)

// /api/transcribe reaches whisper.cpp at /inference and an audio.cpp
// speech-to-text image (Moonshine), which has no /inference, at
// /v1/audio/transcriptions with model "stt".
func TestTranscribeRoutesWhisperAndAudioCpp(t *testing.T) {
	whisper := http.NewServeMux()
	whisper.HandleFunc("/inference", func(w http.ResponseWriter, r *http.Request) {
		_ = json.NewEncoder(w).Encode(map[string]string{"text": " from whisper "})
	})
	audiocpp := http.NewServeMux()
	audiocpp.HandleFunc("/v1/audio/transcriptions", func(w http.ResponseWriter, r *http.Request) {
		if err := r.ParseMultipartForm(1 << 20); err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if r.FormValue("model") != "stt" {
			http.Error(w, "unknown model id: "+r.FormValue("model"), http.StatusBadRequest)
			return
		}
		f, _, err := r.FormFile("file")
		if err != nil {
			http.Error(w, "no file", http.StatusBadRequest)
			return
		}
		f.Close()
		_ = json.NewEncoder(w).Encode(map[string]any{"text": "from audio.cpp", "timing": map[string]float64{"rtf": 0.1}})
	})

	for name, c := range map[string]struct {
		mux  *http.ServeMux
		want string
	}{
		"whisper":  {whisper, "from whisper"},
		"audiocpp": {audiocpp, "from audio.cpp"},
	} {
		t.Run(name, func(t *testing.T) {
			up := httptest.NewServer(c.mux)
			defer up.Close()
			u, _ := url.Parse(up.URL)
			port, _ := strconv.Atoi(u.Port())

			var body bytes.Buffer
			mw := multipart.NewWriter(&body)
			fw, _ := mw.CreateFormFile("file", "clip.wav")
			fw.Write([]byte("RIFF....WAVE"))
			mw.WriteField("port", strconv.Itoa(port))
			mw.Close()
			req := httptest.NewRequest(http.MethodPost, "/api/transcribe", &body)
			req.Header.Set("Content-Type", mw.FormDataContentType())
			rec := httptest.NewRecorder()

			(&Server{modelHost: u.Hostname()}).handleTranscribe(rec, req)
			var out map[string]string
			if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil || rec.Code != http.StatusOK || out["text"] != c.want {
				t.Fatalf("got %d %s, want text %q", rec.Code, rec.Body.String(), c.want)
			}
		})
	}
}

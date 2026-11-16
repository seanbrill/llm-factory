package main

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"

	"github.com/seanbrill/local-llm/internal/ensemble"
	"github.com/seanbrill/local-llm/internal/voice"
)

func userMsg(text string) map[string]any {
	return map[string]any{"model": "test", "messages": []any{map[string]any{"role": "user", "content": text}}}
}

func setPersona(t *testing.T, te *testEns, cfg ensemble.Config) {
	t.Helper()
	resp, b := te.post("/ensemble/config", cfg)
	if resp.StatusCode != 200 {
		t.Fatalf("config: %d %s", resp.StatusCode, b)
	}
}

func cloneVoice(t *testing.T) *ensemble.ConfigVoice {
	return &ensemble.ConfigVoice{Kind: voice.KindClone, Clip: "v-0123456789ab",
		RefAudio: base64.StdEncoding.EncodeToString(testWAV(24000, 1, 4)), RefText: "This is my voice."}
}

// ---- agent routing ---------------------------------------------------------------

func TestAgentChatWithPersona(t *testing.T) {
	te := newTestEns(t)
	setPersona(t, te, ensemble.Config{Persona: &ensemble.ConfigPersona{Name: "Ada", Prompt: "You are Ada, a pirate."}})
	code, out := te.chat(map[string]any{"model": "test", "messages": []any{
		map[string]any{"role": "system", "content": "Be brief."},
		map[string]any{"role": "user", "content": "hi"},
	}})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	if msg := message(t, out); msg["content"] != "Hello there." {
		t.Errorf("content = %v", msg["content"])
	}
	if h, _ := out["harness"].(map[string]any); h == nil {
		t.Error("no harness report in the reply")
	}
	body := te.brain.bodies(t)[0]
	sys := systemOf(body)
	if !strings.HasPrefix(sys, "You are Ada, a pirate.\n\nBe brief.") || !strings.Contains(sys, "never write links") {
		t.Errorf("system prompt = %q", sys)
	}
	names := strings.Join(toolNames(body), ",")
	if names != "generate_image,generate_video,speak,look_at_image" {
		t.Errorf("tools offered = %s", names)
	}
}

func TestAgentGeneratesImageStreaming(t *testing.T) {
	te := newTestEns(t)
	te.brain.turns = []turn{
		{calls: [][2]string{{"generate_image", `{"prompt":"a red fox in snow"}`}}},
		{text: "Here is your fox."},
	}
	body := userMsg("draw a fox")
	body["stream"] = true
	resp, b := te.post("/v1/chat/completions", body)
	if resp.StatusCode != 200 || !strings.HasPrefix(resp.Header.Get("Content-Type"), "text/event-stream") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if resp.Header.Get("X-Ensemble-Route") != "agent" {
		t.Errorf("route = %q", resp.Header.Get("X-Ensemble-Route"))
	}
	events, comments, done := sseEvents(t, b)
	if !done {
		t.Error("no [DONE]")
	}
	content, media := streamContent(events)
	if !strings.Contains(content, "Here is your fox.") || !strings.Contains(content, "![generated image](data:image/png;base64,") {
		t.Errorf("content = %q", content)
	}
	if len(media) != 1 || media[0].(map[string]any)["type"] != "image" {
		t.Fatalf("media = %v", media)
	}
	if !strings.Contains(strings.Join(comments, "|"), "ensemble: Starting the image member (sdxl-turbo)") {
		t.Errorf("comments = %q", comments)
	}
	var sawToolEvent bool
	for _, e := range events {
		if h, ok := e["harness"].(map[string]any); ok && h["name"] == "generate_image" {
			sawToolEvent = true
		}
	}
	if !sawToolEvent {
		t.Error("no harness tool event in the stream")
	}
	// The image prompt reached the image member; the tool result went back to
	// the brain without the image data.
	if r := te.sd.rec.last(t); !strings.Contains(string(r.Body), "a red fox in snow") {
		t.Errorf("sd got %s", r.Body)
	}
	bodies := te.brain.bodies(t)
	if len(bodies) != 2 {
		t.Fatalf("brain calls = %d", len(bodies))
	}
	second, _ := json.Marshal(bodies[1]["messages"])
	if strings.Contains(string(second), "base64") || !strings.Contains(string(second), "The image is ready") {
		t.Errorf("tool result to the brain = %s", second)
	}
}

func TestAgentGeneratesVideoJSON(t *testing.T) {
	te := newTestEns(t)
	te.brain.turns = []turn{
		{calls: [][2]string{{"generate_video", `{"prompt":"waves at dusk"}`}}},
		{text: "Done."},
	}
	code, out := te.chat(userMsg("make a video of waves"))
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	msg := message(t, out)
	media, _ := msg["media"].([]any)
	if len(media) != 1 || media[0].(map[string]any)["type"] != "video" || media[0].(map[string]any)["mime"] != "video/webm" {
		t.Fatalf("media = %v", msg["media"])
	}
	if !strings.Contains(string(te.video.rec.last(t).Body), `"width":416`) {
		t.Errorf("a CPU video member should get the small size: %s", te.video.rec.last(t).Body)
	}
}

func TestAgentSpeaksInClonedVoice(t *testing.T) {
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolSpeak, Modality: "tts", Image: "local-llm/pocket",
		Model: "pocket-tts-en", Runtime: "audiocpp", VoiceClone: true, VRAMGB: 1}))
	te.image("local-llm/pocket", te.tts.handler())
	setPersona(t, te, ensemble.Config{Voice: cloneVoice(t)})
	te.brain.turns = []turn{{calls: [][2]string{{"speak", `{"text":"**Ahoy** there!"}`}}}, {text: "Ahoy there!"}}
	code, out := te.chat(userMsg("say ahoy"))
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	media, _ := message(t, out)["media"].([]any)
	if len(media) != 1 || media[0].(map[string]any)["type"] != "audio" {
		t.Fatalf("media = %v", media)
	}
	var sent map[string]any
	_ = json.Unmarshal(te.tts.rec.last(t).Body, &sent)
	ref, _ := sent["voice_ref"].(map[string]any)
	if sent["model"] != "tts" || sent["input"] != "Ahoy there!" || ref["type"] != "base64" || ref["data"] == "" ||
		sent["reference_text"] != "This is my voice." {
		t.Errorf("audio.cpp got %v", sent)
	}
	if _, ok := sent["voice"]; ok {
		t.Error("voice was sent together with voice_ref")
	}
}

func TestAgentLooksAtImageWithVisionMember(t *testing.T) {
	te := newTestEns(t)
	te.brain.turns = []turn{{calls: [][2]string{{"look_at_image", `{"question":"What color is the ball?","image":"1"}`}}}, {text: "It's red."}}
	img := "data:image/png;base64," + base64.StdEncoding.EncodeToString([]byte("PNG"))
	code, out := te.chat(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "text", "text": "what's this?"},
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}},
	}}}})
	if code != 200 || message(t, out)["content"] != "It's red." {
		t.Fatalf("%d %v", code, out)
	}
	first, _ := json.Marshal(te.brain.bodies(t)[0]["messages"])
	if strings.Contains(string(first), "base64") || !strings.Contains(string(first), "[image 1 attached: call look_at_image to see it]") {
		t.Errorf("the brain got %s", first)
	}
	vbody, _ := json.Marshal(te.vision.bodies(t)[0])
	if !strings.Contains(string(vbody), img) || !strings.Contains(string(vbody), "What color is the ball?") {
		t.Errorf("the vision member got %s", vbody)
	}
	second, _ := json.Marshal(te.brain.bodies(t)[1]["messages"])
	if !strings.Contains(string(second), "Image 1: A red ball.") || !strings.Contains(string(second), `tool_output tool=`) {
		t.Errorf("the vision answer should reach the brain as untrusted tool output: %s", second)
	}
}

func TestVisionBrainSeesImagesItself(t *testing.T) {
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolChat, Modality: "vision", Image: "local-llm/chat", Runtime: "cpp", VRAMGB: 6}))
	img := "data:image/png;base64,UE5H"
	te.chat(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "image_url", "image_url": map[string]any{"url": img}}}}}})
	body := te.brain.bodies(t)[0]
	raw, _ := json.Marshal(body["messages"])
	if !strings.Contains(string(raw), img) {
		t.Errorf("a vision brain should get the image: %s", raw)
	}
	for _, n := range toolNames(body) {
		if n == "look_at_image" {
			t.Error("look_at_image offered to a brain that sees")
		}
	}
}

func TestAgentFallsBackWhenToolsRefused(t *testing.T) {
	te := newTestEns(t)
	te.brain.turns = []turn{
		{status: 500, errMsg: "tools param requires --jinja flag"},
		{text: "plain answer"},
	}
	code, out := te.chat(userMsg("hi"))
	if code != 200 || message(t, out)["content"] != "plain answer" {
		t.Fatalf("%d %v", code, out)
	}
	bodies := te.brain.bodies(t)
	if len(bodies) != 2 || bodies[1]["tools"] != nil {
		t.Fatalf("the retry should go without tools: %v", bodies)
	}
	if te.g.agentOn() {
		t.Fatal("agent routing still on after the brain refused tools")
	}
	// From now on the heuristic routes: "draw" goes straight to the image member.
	code, out = te.chat(userMsg("draw a cat"))
	if code != 200 || len(message(t, out)["media"].([]any)) != 1 {
		t.Fatalf("%d %v", code, out)
	}
	_, b := te.get("/ensemble/status")
	var st ensemble.Status
	_ = json.Unmarshal(b, &st)
	if st.Router != "heuristic" || !strings.Contains(st.RouterNote, "refused tools") {
		t.Errorf("status router = %q (%s)", st.Router, st.RouterNote)
	}
}

func TestAgentErrorBeforeStream(t *testing.T) {
	te := newTestEns(t)
	te.brain.turns = []turn{{status: 400, errMsg: "the request exceeds the available context size"}}
	body := userMsg("hi")
	body["stream"] = true
	resp, b := te.post("/v1/chat/completions", body)
	if resp.StatusCode != 200 {
		t.Fatalf("a stream starts at once (status lines while warming): %d", resp.StatusCode)
	}
	events, _, done := sseEvents(t, b)
	if !done || len(events) == 0 {
		t.Fatalf("stream = %s", b)
	}
	e, _ := events[len(events)-1]["error"].(map[string]any)
	if e == nil || !strings.Contains(e["message"].(string), "context window") {
		t.Errorf("last event = %v", events[len(events)-1])
	}
	if !te.g.agentOn() {
		t.Error("a context error isn't a tools refusal")
	}
}

func TestAgentHarnessFalsePassesThrough(t *testing.T) {
	te := newTestEns(t)
	body := userMsg("hi")
	body["harness"] = false
	code, out := te.chat(body)
	if code != 200 || message(t, out)["content"] != "Hello there." {
		t.Fatalf("%d %v", code, out)
	}
	if b := te.brain.bodies(t)[0]; b["tools"] != nil || b["harness"] != nil {
		t.Errorf("passthrough body = %v", b)
	}
}

func TestInputAudioIsTranscribed(t *testing.T) {
	te := newTestEns(t)
	wav := base64.StdEncoding.EncodeToString(testWAV(16000, 1, 1))
	code, out := te.chat(map[string]any{"messages": []any{map[string]any{"role": "user", "content": []any{
		map[string]any{"type": "input_audio", "input_audio": map[string]any{"data": wav, "format": "wav"}}}}}})
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	raw, _ := json.Marshal(te.brain.bodies(t)[0]["messages"])
	if !strings.Contains(string(raw), "what is the capital of france") || strings.Contains(string(raw), "input_audio") {
		t.Errorf("the brain got %s", raw)
	}
}

func TestNoBrainUsesHeuristic(t *testing.T) {
	te := newTestEns(t, without(ensemble.ToolChat, ensemble.ToolSeeImage))
	if te.g.brain != nil || !strings.Contains(te.g.brainOff, "no chat member") {
		t.Fatalf("brain = %v (%s)", te.g.brain, te.g.brainOff)
	}
	code, out := te.chat(userMsg("draw a cat"))
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	code, out = te.chat(userMsg("tell me a story"))
	if code != 400 || !strings.Contains(out["error"].(map[string]any)["message"].(string), "no chat member") {
		t.Errorf("%d %v", code, out)
	}
}

func TestPythonBrainCantCallTools(t *testing.T) {
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolChat, Modality: "text", Image: "local-llm/chat", Runtime: "python", VRAMGB: 6}))
	if te.g.brain != nil || !strings.Contains(te.g.brainOff, "can't call tools") {
		t.Fatalf("brainOff = %q", te.g.brainOff)
	}
}

// ---- heuristic routing ---------------------------------------------------------

func TestHeuristicRoutes(t *testing.T) {
	te := newTestEns(t, routing(ensemble.RoutingHeuristic))
	if te.g.brain != nil {
		t.Fatal("heuristic routing built a brain")
	}
	code, out := te.chat(userMsg("draw a lighthouse at night"))
	if code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	msg := message(t, out)
	if media, _ := msg["media"].([]any); len(media) != 1 || !strings.Contains(string(te.sd.rec.last(t).Body), `"prompt":"lighthouse at night"`) {
		t.Errorf("image route: %v / %s", msg, te.sd.rec.last(t).Body)
	}
	// say "..." speaks exactly that, in the persona's built-in voice.
	setPersona(t, te, ensemble.Config{Voice: &ensemble.ConfigVoice{Kind: voice.KindBuiltin, Name: "amy"}})
	body := userMsg(`say "good morning"`)
	body["stream"] = true
	_, b := te.post("/v1/chat/completions", body)
	events, _, _ := sseEvents(t, b)
	content, media := streamContent(events)
	if content != "good morning" || len(media) != 1 {
		t.Errorf("speak route: %q %v", content, media)
	}
	if got := string(te.tts.rec.last(t).Body); got != `{"input":"good morning"}` {
		t.Errorf("piper got %s", got)
	}
}

// ---- speech endpoints -----------------------------------------------------------

func multipartBody(t *testing.T, fields map[string]string, file []byte) (*bytes.Buffer, string) {
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	if file != nil {
		fw, _ := mw.CreateFormFile("file", "speech.wav")
		_, _ = fw.Write(file)
	}
	for k, v := range fields {
		_ = mw.WriteField(k, v)
	}
	_ = mw.Close()
	return &buf, mw.FormDataContentType()
}

func (te *testEns) postForm(path string, fields map[string]string, file []byte) (*http.Response, []byte) {
	te.t.Helper()
	buf, ct := multipartBody(te.t, fields, file)
	resp, err := http.Post(te.srv.URL+path, ct, buf)
	if err != nil {
		te.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func (te *testEns) get(path string) (*http.Response, []byte) {
	te.t.Helper()
	resp, err := http.Get(te.srv.URL + path)
	if err != nil {
		te.t.Fatal(err)
	}
	defer resp.Body.Close()
	b, _ := io.ReadAll(resp.Body)
	return resp, b
}

func TestTranscriptionsWhisper(t *testing.T) {
	te := newTestEns(t)
	resp, b := te.postForm("/v1/audio/transcriptions", map[string]string{"model": "test"}, testWAV(44100, 2, 1))
	if resp.StatusCode != 200 || string(bytes.TrimSpace(b)) != `{"text":"what is the capital of france"}` {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	r := te.stt.rec.last(t)
	w, err := voice.ParseWAV(r.File)
	if r.Path != "/inference" || err != nil || w.SampleRate != 16000 || w.Channels != 1 {
		t.Errorf("whisper got %s, %+v %v", r.Path, w, err)
	}
	resp, b = te.postForm("/v1/audio/transcriptions", map[string]string{"response_format": "text"}, testWAV(16000, 1, 1))
	if resp.StatusCode != 200 || string(b) != "what is the capital of france" {
		t.Errorf("text format: %d %q", resp.StatusCode, b)
	}
}

func TestTranscriptionsAudioCpp(t *testing.T) {
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolTranscribe, Modality: "audio-stt",
		Image: "local-llm/moonshine", Model: "moonshine-streaming-small", Runtime: "audiocpp", VRAMGB: 1}))
	stt := &fakeSTT{text: "hello", openai: true}
	te.image("local-llm/moonshine", stt.handler())
	resp, b := te.postForm("/v1/audio/transcriptions", nil, testWAV(16000, 1, 1))
	if resp.StatusCode != 200 || !strings.Contains(string(b), `"hello"`) {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if r := stt.rec.last(t); r.Path != "/v1/audio/transcriptions" || r.Form["model"] != "stt" {
		t.Errorf("moonshine got %s %v", r.Path, r.Form)
	}
}

func TestTranscriptionsFallBackWhenNoInference(t *testing.T) {
	// A custom image without catalog runtime: /inference 404s, so the OpenAI path is tried.
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolTranscribe, Modality: "audio-stt", Image: "local-llm/custom-stt", VRAMGB: 1}))
	stt := &fakeSTT{text: "fallback", openai: true}
	te.image("local-llm/custom-stt", stt.handler())
	resp, b := te.postForm("/v1/audio/transcriptions", nil, testWAV(16000, 1, 1))
	if resp.StatusCode != 200 || !strings.Contains(string(b), "fallback") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
}

func TestTranscriptionsNeedsSTTMember(t *testing.T) {
	te := newTestEns(t, without(ensemble.ToolTranscribe))
	resp, b := te.postForm("/v1/audio/transcriptions", nil, testWAV(16000, 1, 1))
	if resp.StatusCode != 400 || !strings.Contains(string(b), "no speech-to-text member") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	resp, b = te.postForm("/v1/audio/transcriptions", nil, nil)
	if resp.StatusCode != 400 || !strings.Contains(string(b), "missing the audio") {
		t.Fatalf("no file: %d %s", resp.StatusCode, b)
	}
}

func TestSpeechUsesPersonaVoice(t *testing.T) {
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolSpeak, Modality: "tts", Image: "local-llm/pocket",
		Model: "pocket-tts-en", Runtime: "audiocpp", VoiceClone: true, VRAMGB: 1}))
	te.image("local-llm/pocket", te.tts.handler())
	setPersona(t, te, ensemble.Config{Voice: &ensemble.ConfigVoice{Kind: voice.KindBuiltin, Name: "alba"}})
	resp, b := te.post("/v1/audio/speech", map[string]any{"model": "test", "input": "Hi.", "voice": "alloy", "speed": 1.25})
	if resp.StatusCode != 200 || resp.Header.Get("Content-Type") != "audio/wav" || resp.Header.Get("X-Ensemble-Voice") != "builtin:alba" {
		t.Fatalf("%d %v %s", resp.StatusCode, resp.Header, b)
	}
	if _, err := voice.ParseWAV(b); err != nil {
		t.Errorf("not a WAV: %v", err)
	}
	var sent map[string]any
	_ = json.Unmarshal(te.tts.rec.last(t).Body, &sent)
	if sent["voice"] != "alba" || sent["speed"] != 1.25 || sent["model"] != "tts" {
		t.Errorf("audio.cpp got %v (alloy is a stock name: the persona's voice wins)", sent)
	}
	// A clone, and a named voice that isn't stock.
	setPersona(t, te, ensemble.Config{Voice: cloneVoice(t)})
	resp, _ = te.post("/v1/audio/speech", map[string]any{"input": "Hi."})
	if resp.Header.Get("X-Ensemble-Voice") != "clone" {
		t.Errorf("voice header = %q", resp.Header.Get("X-Ensemble-Voice"))
	}
	te.post("/v1/audio/speech", map[string]any{"input": "Hi.", "voice": "cosette"})
	_ = json.Unmarshal(te.tts.rec.last(t).Body, &sent)
	if sent["voice"] != "cosette" || sent["voice_ref"] != nil {
		t.Errorf("a named voice should win: %v", sent)
	}
}

func TestSpeechCloneErrors(t *testing.T) {
	te := newTestEns(t) // piper: no cloning
	setPersona(t, te, ensemble.Config{Voice: cloneVoice(t)})
	resp, b := te.post("/v1/audio/speech", map[string]any{"input": "Hi."})
	if resp.StatusCode != 400 || !strings.Contains(string(b), "can't clone voices") {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	if n := te.eng.runCount("local-llm/piper"); n != 0 {
		t.Error("the speech member was started for a request it can't serve")
	}

	te = newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolSpeak, Modality: "tts", Image: "local-llm/qwen-base",
		Model: "qwen3-tts-0.6b-base", Runtime: "audiocpp", VoiceClone: true, CloneNeedsTranscript: true, CloneOnly: true, VRAMGB: 2}))
	te.image("local-llm/qwen-base", te.tts.handler())
	resp, b = te.post("/v1/audio/speech", map[string]any{"input": "Hi."})
	if resp.StatusCode != 400 || !strings.Contains(string(b), "has no built-in voices") {
		t.Errorf("clone-only without a clip: %d %s", resp.StatusCode, b)
	}
	v := cloneVoice(t)
	v.RefText = ""
	setPersona(t, te, ensemble.Config{Voice: v})
	resp, b = te.post("/v1/audio/speech", map[string]any{"input": "Hi."})
	if resp.StatusCode != 400 || !strings.Contains(string(b), "needs the voice clip's transcript") {
		t.Errorf("no transcript: %d %s", resp.StatusCode, b)
	}
}

func TestConfigValidation(t *testing.T) {
	te := newTestEns(t)
	bad := cloneVoice(t)
	bad.RefAudio = base64.StdEncoding.EncodeToString(testWAV(16000, 1, 1)) // 1 s: too short
	resp, b := te.post("/ensemble/config", ensemble.Config{Voice: bad})
	if resp.StatusCode != 400 || !strings.Contains(string(b), "at least 3 s") {
		t.Errorf("short clip: %d %s", resp.StatusCode, b)
	}
	stereo := cloneVoice(t)
	stereo.RefAudio = base64.StdEncoding.EncodeToString(testWAV(16000, 2, 4))
	if resp, b := te.post("/ensemble/config", ensemble.Config{Voice: stereo}); resp.StatusCode != 400 || !strings.Contains(string(b), "mono") {
		t.Errorf("stereo clip: %d %s", resp.StatusCode, b)
	}
	resp, b = te.post("/ensemble/config", ensemble.Config{Persona: &ensemble.ConfigPersona{Name: " Ada ", Prompt: " Be kind. "}, Voice: cloneVoice(t)})
	var st ensemble.PersonaStatus
	_ = json.Unmarshal(b, &st)
	if resp.StatusCode != 200 || st.Name != "Ada" || st.Voice != "clone" || st.Hash == "" {
		t.Errorf("good config: %d %s", resp.StatusCode, b)
	}
	_, b = te.get("/ensemble/config")
	if strings.Contains(string(b), "UklGR") {
		t.Error("GET /ensemble/config leaks the clip")
	}
}

// ---- the voice chain --------------------------------------------------------------

func TestVoiceChatChain(t *testing.T) {
	te := newTestEns(t, withMember(ensemble.Member{Tool: ensemble.ToolSpeak, Modality: "tts", Image: "local-llm/pocket",
		Model: "pocket-tts-en", Runtime: "audiocpp", VoiceClone: true, VRAMGB: 1}))
	te.image("local-llm/pocket", te.tts.handler())
	setPersona(t, te, ensemble.Config{Persona: &ensemble.ConfigPersona{Name: "Ada", Prompt: "You are Ada."}, Voice: cloneVoice(t)})
	te.brain.turns = []turn{{text: "The capital of **France** is Paris."}}
	history, _ := json.Marshal([]any{map[string]any{"role": "assistant", "content": "Hi! ![generated image](data:image/png;base64,AAAA)"}})
	resp, b := te.postForm("/ensemble/voice-chat", map[string]string{"messages": string(history)}, testWAV(48000, 1, 2))
	if resp.StatusCode != 200 {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	var out VoiceReply
	if err := json.Unmarshal(b, &out); err != nil {
		t.Fatal(err)
	}
	if out.Transcript != "what is the capital of france" || out.Route != "agent" || out.Reply.Content != "The capital of **France** is Paris." ||
		!strings.HasPrefix(out.Audio, "data:audio/wav;base64,") || out.SpeechError != "" {
		t.Errorf("reply = %+v", out)
	}
	for _, k := range []string{"stt_ms", "chat_ms", "tts_ms"} {
		if _, ok := out.Timings[k]; !ok {
			t.Errorf("no %s timing", k)
		}
	}
	body := te.brain.bodies(t)[0]
	for _, n := range toolNames(body) {
		if n == "speak" {
			t.Error("speak offered in voice chat (the whole answer is spoken anyway)")
		}
	}
	raw, _ := json.Marshal(body["messages"])
	if strings.Contains(string(raw), "AAAA") || !strings.Contains(string(raw), "what is the capital of france") {
		t.Errorf("brain messages = %s", raw)
	}
	var sent map[string]any
	_ = json.Unmarshal(te.tts.rec.last(t).Body, &sent)
	if sent["input"] != "The capital of France is Paris." || sent["voice_ref"] == nil {
		t.Errorf("speech got %v", sent)
	}
}

func TestVoiceChatSpeechFailureKeepsText(t *testing.T) {
	te := newTestEns(t) // piper can't clone
	setPersona(t, te, ensemble.Config{Voice: cloneVoice(t)})
	resp, b := te.postForm("/ensemble/voice-chat", nil, testWAV(16000, 1, 1))
	var out VoiceReply
	_ = json.Unmarshal(b, &out)
	if resp.StatusCode != 200 || out.Reply.Content != "Hello there." || out.Audio != "" || !strings.Contains(out.SpeechError, "can't clone") {
		t.Errorf("%d %+v", resp.StatusCode, out)
	}
}

func TestVoiceChatSilence(t *testing.T) {
	te := newTestEns(t)
	te.stt.text = "[BLANK_AUDIO]"
	resp, b := te.postForm("/ensemble/voice-chat", nil, testWAV(16000, 1, 1))
	if resp.StatusCode != 422 || !strings.Contains(string(b), "no speech was heard") {
		t.Errorf("%d %s", resp.StatusCode, b)
	}
}

// ---- status, models, members -------------------------------------------------------

func TestStatusModelsHarness(t *testing.T) {
	te := newTestEns(t)
	_, b := te.get("/ensemble/status")
	var st ensemble.Status
	if err := json.Unmarshal(b, &st); err != nil {
		t.Fatal(err)
	}
	if st.ID != "test" || st.Router != "agent" || len(st.Members) != 6 || strings.Join(st.AgentTools, ",") != "generate_image,generate_video,speak,look_at_image" ||
		!strings.Contains(st.RouterNote, "chat member (qwen) is the brain") {
		t.Errorf("status = %+v", st)
	}
	_, b = te.get("/v1/models")
	if !strings.Contains(string(b), `"id":"test"`) || !strings.Contains(string(b), `"kind":"ensemble"`) {
		t.Errorf("models = %s", b)
	}
	_, b = te.get("/v1/harness")
	var info map[string]any
	_ = json.Unmarshal(b, &info)
	if info["enabled"] != true || info["host"] != "ensemblegate" {
		t.Errorf("harness = %s", b)
	}
}

func TestMembersEndpoint(t *testing.T) {
	te := newTestEns(t)
	resp, b := te.post("/ensemble/members", map[string]string{"tool": "speak", "action": "start"})
	if resp.StatusCode != 200 || memberState(t, te.g.mg, "speak").State != ensemble.StateReady {
		t.Fatalf("%d %s", resp.StatusCode, b)
	}
	resp, _ = te.post("/ensemble/members", map[string]string{"tool": "speak", "action": "stop"})
	if resp.StatusCode != 200 || memberState(t, te.g.mg, "speak").State != ensemble.StateStopped {
		t.Fatalf("stop: %d", resp.StatusCode)
	}
	resp, b = te.post("/ensemble/members", map[string]string{"tool": "speak", "action": "dance"})
	if resp.StatusCode != 400 {
		t.Errorf("bad action: %d %s", resp.StatusCode, b)
	}
}

func TestGetOnPostEndpoints(t *testing.T) {
	te := newTestEns(t)
	for _, p := range []string{"/v1/chat/completions", "/v1/audio/speech", "/v1/audio/transcriptions", "/ensemble/voice-chat"} {
		if resp, _ := te.get(p); resp.StatusCode != 405 {
			t.Errorf("GET %s = %d", p, resp.StatusCode)
		}
	}
}

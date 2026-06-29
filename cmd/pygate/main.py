# pygate — the in-container shim for the "python" runtime family. Mirrors the Go
# gates (llmgate/videogate) but for PyTorch models that have no C++/GGUF port.
#
# This first build serves Kokoro TTS behind the OpenAI-compatible
# POST /v1/audio/speech endpoint the factory's /api/tts already calls, so the
# existing chat "speak" flow works unchanged. Returns a 24kHz WAV.
#
# Env: VOICE (default af_heart), KOKORO_LANG (default 'a' = American English),
#      PORT (default 8080).
import io
import os

import numpy as np
import soundfile as sf
from fastapi import FastAPI, Request, Response

LANG = os.environ.get("KOKORO_LANG", "a")
VOICE = os.environ.get("VOICE", "af_heart")
SR = 24000  # Kokoro output sample rate

app = FastAPI()
_pipe = None


def pipe():
    # Lazy-load so the server binds its port immediately (and a failed model
    # download surfaces on the first request, not as a crash loop at boot).
    global _pipe
    if _pipe is None:
        from kokoro import KPipeline

        _pipe = KPipeline(lang_code=LANG)
    return _pipe


@app.get("/health")
def health():
    return {"ok": True, "runtime": "python", "model": "kokoro"}


@app.post("/v1/audio/speech")
async def speech(req: Request):
    body = await req.json()
    text = (body.get("input") or "").strip()
    voice = body.get("voice") or VOICE
    if not text:
        return Response('{"error":"empty input"}', status_code=400, media_type="application/json")
    try:
        chunks = []
        for _gs, _ps, audio in pipe()(text, voice=voice):
            a = audio.detach().cpu().numpy() if hasattr(audio, "detach") else np.asarray(audio)
            chunks.append(a.astype(np.float32))
        if not chunks:
            return Response('{"error":"no audio produced"}', status_code=500, media_type="application/json")
        wav = np.concatenate(chunks) if len(chunks) > 1 else chunks[0]
        buf = io.BytesIO()
        sf.write(buf, wav, SR, format="WAV")
        return Response(buf.getvalue(), media_type="audio/wav")
    except Exception as e:  # surface the real error to the UI instead of a 502
        return Response('{"error":%r}' % str(e), status_code=500, media_type="application/json")

# Voice cloning

Some text-to-speech models in the catalog can speak in a voice copied from a
short recording you give them. In the Catalog they carry a **Clones voices** chip,
and **Filters → Voice** lists only them. All of them run on
[audio.cpp](https://github.com/0xShug0/audio.cpp), CPU only, from the image pinned in
`docker/Dockerfile.audiocpp.cpu`.

## Consent

**Clone only your own voice, or a voice whose speaker has agreed to it.** Don't
use a cloned voice to pass as someone, or present what it says as a real recording
of them.

This is a condition of use, not only advice. Kyutai's Pocket TTS terms say:
"Prohibited uses include, without limitation, voice impersonation or cloning
without explicit and lawful consent; misinformation, disinformation, or deception
(including fake news, fraudulent calls, or presenting generated content as genuine
recordings of real people or events)". The same rule applies to every model on
this page.

## The models

| Catalog id | Model | License | Commercial use | Reference it needs | Streams | Weights |
|---|---|---|---|---|---|---|
| `pocket-tts-en` | Pocket TTS 100M (English) | CC-BY-4.0, credit Kyutai | Yes | a few seconds of audio | Yes | 0.29 GB |
| `pocket-tts-small-en` | Pocket TTS Small 18M (English, experimental) | CC-BY-4.0, credit Kyutai | Yes | a few seconds of audio | Yes | 0.12 GB |
| `qwen3-tts-0.6b-base` | Qwen3-TTS 0.6B Base | Apache-2.0 | Yes | about 3 s of audio **and its transcript** | Yes | 1.99 GB |
| `qwen3-tts-1.7b-base` | Qwen3-TTS 1.7B Base | Apache-2.0 | Yes | about 3 s of audio **and its transcript** | Yes | 2.70 GB |
| `voxcpm2` | VoxCPM2 2B | Apache-2.0 | Yes | audio; a transcript makes it closer | Yes | 2.96 GB |
| `ming-3.0-tts-0.5b` | Ming 3.0 TTS 0.5B | MIT | Yes | audio; a transcript makes it closer | Yes | 1.74 GB |
| `xtts-v2` | XTTS v2 | Coqui Public Model License (CPML) | **No** | 6–30 s of audio | No | 0.57 GB |

**XTTS v2 is non-commercial only.** The CPML forbids commercial use of the model
and of the audio it makes. The catalog marks it with a **Non-commercial license**
chip and a warning in its details. The GGUF comes from a community mirror
(`onicubag/xTTSv2-GGUF`); its repo page says MPL-2.0, but the weights are still
Coqui's, under the CPML.

Not cloners: Qwen3-TTS 1.7B CustomVoice (9 built-in speakers you steer with a style
instruction), Kokoro and Piper. Piper's en_US voice is MIT, but the recordings it
was trained on allow non-commercial use only, so the catalog marks it non-commercial
too.

The catalog records these facts per model as `voice_clone`,
`clone_needs_transcript`, `license` and `commercial_use` (in
`internal/catalog/models.default.json`, mirrored in `config/models.json`).

## Calling a cloning image

An audio.cpp image serves its one model as `"tts"` on port 8080, at
`POST /v1/audio/speech`. Put the reference recording in `voice_ref` as a base64
WAV (at most 5 MiB decoded) and, for the Qwen3 Base models, its transcript in
`reference_text`:

```json
{
  "model": "tts",
  "input": "Text to speak in the cloned voice.",
  "voice_ref": { "type": "base64", "data": "UklGRh..." },
  "reference_text": "What the reference recording says."
}
```

Send `voice_ref` without `voice`. Pocket TTS refuses a request that names both a
built-in voice and a reference.

The reply is a WAV file. Every model above except XTTS v2 is built in streaming
mode, so it can also send audio as it speaks. Add `"response_format": "pcm"` and
either:

- `"stream_format": "sse"` for server-sent events: `speech.audio.delta` events
  with base64 PCM, then `speech.audio.done`, then `data: [DONE]`.
- `"stream_format": "audio"` for raw PCM over a chunked response.

VoxCPM2's image sets `retry_badcase: false`, which audio.cpp requires before
VoxCPM2 can stream.

## Speech to text on audio.cpp

`moonshine-streaming-small` (Moonshine Streaming Small, English, MIT, 0.30 GB) is
an audio.cpp speech-to-text image. It serves its model as `"stt"` at:

- `POST /v1/audio/transcriptions`: a multipart WAV upload. Add `stream=true` to
  get the text as it decodes.
- `POST /v1/audio/transcriptions/live`: raw PCM as it's captured.

It reads WAV only. The factory's `/api/transcribe`, which the Playground uses for
an attached audio file, reaches it as well as whisper.cpp images.

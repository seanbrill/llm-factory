// Microphone recording and WAV conversion, all in the browser. The factory's
// speech-to-text (whisper.cpp) and the voice-clip store both take a plain WAV:
// mono, 16-bit PCM. A recording (webm/opus or mp4 from MediaRecorder) or an
// uploaded file (wav, mp3, m4a…) is decoded with the Web Audio API, mixed down
// to one channel, resampled with an OfflineAudioContext and written as WAV.
//
// Rates: 16000 Hz for questions to speech-to-text, 24000 Hz for voice clips
// (closer to what speech models speak at).

export const STT_RATE = 16000;
export const CLIP_RATE = 24000;

/** A recording in progress: stop() resolves the captured audio, cancel() drops it. */
export interface Recording {
  started: number; // performance.now() at start
  stop(): Promise<Blob>;
  cancel(): void;
}

/** A sentence for a failed microphone or decode, with what to do about it. */
export function audioError(e: unknown): string {
  const err = e as { name?: string; message?: string } | undefined;
  switch (err?.name) {
    case "NotAllowedError":
    case "SecurityError":
      return "The browser blocked the microphone. Allow it for this page (the icon in the address bar), then try again.";
    case "NotFoundError":
    case "OverconstrainedError":
      return "No microphone was found. Plug one in or pick one in your system's sound settings, then try again.";
    case "NotReadableError":
    case "AbortError":
      return "The microphone is busy or unavailable. Close other apps that use it, then try again.";
    case "EncodingError":
      return "The browser couldn't read that audio. Try a WAV, MP3 or M4A file.";
  }
  return err?.message || "The microphone didn't start.";
}

/** Whether this page may use the microphone at all (https or localhost only). */
export function micAvailable(): string {
  if (typeof navigator === "undefined" || !navigator.mediaDevices?.getUserMedia || typeof MediaRecorder === "undefined") {
    return "The browser only allows the microphone on https or localhost. Open the factory at http://localhost and try again.";
  }
  return "";
}

const MIME_PREFS = ["audio/webm;codecs=opus", "audio/webm", "audio/mp4", "audio/ogg;codecs=opus", "audio/ogg"];

/**
 * Start recording from the microphone. `raw` turns off the browser's echo
 * cancellation, noise suppression and gain control: better for a voice clip a
 * model will copy, worse for a question in a noisy room.
 */
export async function startRecording(raw = false): Promise<Recording> {
  const why = micAvailable();
  if (why) throw new Error(why);
  const stream = await navigator.mediaDevices.getUserMedia({
    audio: { channelCount: 1, echoCancellation: !raw, noiseSuppression: !raw, autoGainControl: !raw },
  });
  const mimeType = MIME_PREFS.find((m) => MediaRecorder.isTypeSupported?.(m));
  let rec: MediaRecorder;
  try {
    rec = new MediaRecorder(stream, mimeType ? { mimeType } : undefined);
  } catch (e) {
    stream.getTracks().forEach((t) => t.stop());
    throw e;
  }
  const chunks: Blob[] = [];
  rec.ondataavailable = (e) => {
    if (e.data.size) chunks.push(e.data);
  };
  const release = () => stream.getTracks().forEach((t) => t.stop());
  rec.start(250);
  return {
    started: performance.now(),
    stop: () =>
      new Promise<Blob>((resolve, reject) => {
        rec.onstop = () => {
          release();
          resolve(new Blob(chunks, { type: rec.mimeType || mimeType || "audio/webm" }));
        };
        rec.onerror = (e) => {
          release();
          reject((e as unknown as { error?: Error }).error ?? new Error("The recording failed."));
        };
        if (rec.state === "inactive") {
          release();
          resolve(new Blob(chunks, { type: rec.mimeType || "audio/webm" }));
        } else rec.stop();
      }),
    cancel: () => {
      rec.onstop = null;
      if (rec.state !== "inactive") rec.stop();
      release();
    },
  };
}

type AnyAudioContext = typeof AudioContext;

/** Decode any audio the browser can play into an AudioBuffer. */
async function decode(data: ArrayBuffer): Promise<AudioBuffer> {
  const Ctx: AnyAudioContext | undefined =
    window.AudioContext ?? (window as unknown as { webkitAudioContext?: AnyAudioContext }).webkitAudioContext;
  if (!Ctx) throw new Error("This browser has no Web Audio support, so it can't convert audio.");
  const ctx = new Ctx();
  try {
    return await ctx.decodeAudioData(data);
  } catch {
    const e = new Error("The browser couldn't read that audio. Try a WAV, MP3 or M4A file.");
    e.name = "EncodingError";
    throw e;
  } finally {
    ctx.close().catch(() => {});
  }
}

/** Mix down to one channel and resample to `rate`. */
async function monoAt(buf: AudioBuffer, rate: number): Promise<Float32Array> {
  const frames = Math.max(1, Math.ceil(buf.duration * rate));
  const off = new OfflineAudioContext(1, frames, rate);
  const src = off.createBufferSource();
  src.buffer = buf;
  src.connect(off.destination); // a one-channel destination downmixes (L+R)/2
  src.start();
  const out = await off.startRendering();
  return out.getChannelData(0);
}

/** 16-bit PCM WAV bytes for mono samples in [-1, 1]. */
export function encodeWav(samples: Float32Array, rate: number): Blob {
  const data = samples.length * 2;
  const buf = new ArrayBuffer(44 + data);
  const v = new DataView(buf);
  const str = (o: number, s: string) => {
    for (let i = 0; i < s.length; i++) v.setUint8(o + i, s.charCodeAt(i));
  };
  str(0, "RIFF");
  v.setUint32(4, 36 + data, true);
  str(8, "WAVE");
  str(12, "fmt ");
  v.setUint32(16, 16, true); // fmt chunk size
  v.setUint16(20, 1, true); // PCM
  v.setUint16(22, 1, true); // mono
  v.setUint32(24, rate, true);
  v.setUint32(28, rate * 2, true); // byte rate
  v.setUint16(32, 2, true); // block align
  v.setUint16(34, 16, true); // bits per sample
  str(36, "data");
  v.setUint32(40, data, true);
  let o = 44;
  for (let i = 0; i < samples.length; i++, o += 2) {
    const s = Math.max(-1, Math.min(1, samples[i]));
    v.setInt16(o, s < 0 ? s * 0x8000 : s * 0x7fff, true);
  }
  return new Blob([buf], { type: "audio/wav" });
}

/**
 * Any recorded or uploaded audio -> a mono 16-bit WAV at `rate`, and its
 * length. maxSeconds > 0 cuts it to that length (a recording that ran a moment
 * past its limit).
 */
export async function toWav(audio: Blob, rate: number, maxSeconds = 0): Promise<{ wav: Blob; seconds: number }> {
  const buf = await decode(await audio.arrayBuffer());
  let samples = await monoAt(buf, rate);
  if (maxSeconds > 0 && samples.length > maxSeconds * rate) samples = samples.subarray(0, Math.floor(maxSeconds * rate));
  return { wav: encodeWav(samples, rate), seconds: samples.length / rate };
}

/** "0:07" for a running timer. */
export function elapsed(ms: number): string {
  const s = Math.max(0, Math.floor(ms / 1000));
  return `${Math.floor(s / 60)}:${String(s % 60).padStart(2, "0")}`;
}

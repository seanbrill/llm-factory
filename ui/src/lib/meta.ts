// Per-model capability ratings, strength tags, capability dimensions, category
// icons, and the plain-language glossary — ported from the vanilla model-meta.js
// + app.js. UI-only guidance (0-100, rough, calibrated within local open models).
import { ICONS } from "./icons";

export interface ModelMeta {
  strengths: string[];
  summary: string;
  weakness: string;
  ratings: Record<string, number>;
}

export const META: Record<string, ModelMeta> = {
  "qwen3-0.6b": {
    "strengths": [
      "lightweight",
      "fast",
      "multilingual"
    ],
    "summary": "Ultra-tiny model best for on-device speed and simple tasks with surprising basic reasoning.",
    "weakness": "Weak on hard reasoning, math, coding, and broad knowledge due to tiny size.",
    "ratings": {
      "Reasoning": 34,
      "Coding": 28,
      "Math": 32,
      "Knowledge": 26,
      "Writing": 38,
      "Instruction": 46,
      "Multilingual": 50
    }
  },
  "qwen3-4b": {
    "strengths": [
      "reasoning",
      "multilingual",
      "instruction-following",
      "fast"
    ],
    "summary": "Punches well above its weight with hybrid reasoning, strong for a 4B all-rounder.",
    "weakness": "Limited depth of knowledge and coding versus 8B+ peers.",
    "ratings": {
      "Reasoning": 66,
      "Coding": 58,
      "Math": 68,
      "Knowledge": 54,
      "Writing": 60,
      "Instruction": 70,
      "Multilingual": 68
    }
  },
  "qwen3-8b": {
    "strengths": [
      "reasoning",
      "math",
      "multilingual",
      "instruction-following"
    ],
    "summary": "Strong mid-size hybrid-reasoning generalist with solid math and agentic skills.",
    "weakness": "Knowledge breadth still trails 14B+ models.",
    "ratings": {
      "Reasoning": 76,
      "Coding": 68,
      "Math": 78,
      "Knowledge": 64,
      "Writing": 68,
      "Instruction": 78,
      "Multilingual": 76
    }
  },
  "qwen3-14b": {
    "strengths": [
      "reasoning",
      "math",
      "multilingual",
      "agentic"
    ],
    "summary": "Top-tier local all-rounder with excellent reasoning, math, and agentic performance.",
    "weakness": "Slower and heavier; coding lags dedicated coder models.",
    "ratings": {
      "Reasoning": 87,
      "Coding": 78,
      "Math": 87,
      "Knowledge": 76,
      "Writing": 76,
      "Instruction": 86,
      "Multilingual": 84
    }
  },
  "qwen2.5-1.5b": {
    "strengths": [
      "lightweight",
      "fast",
      "multilingual"
    ],
    "summary": "Small efficient prior-gen model good for lightweight tasks and basic instruction-following.",
    "weakness": "Falls short on complex reasoning, math, and coding.",
    "ratings": {
      "Reasoning": 40,
      "Coding": 34,
      "Math": 38,
      "Knowledge": 38,
      "Writing": 46,
      "Instruction": 50,
      "Multilingual": 50
    }
  },
  "qwen2.5-3b": {
    "strengths": [
      "lightweight",
      "multilingual",
      "instruction-following",
      "fast"
    ],
    "summary": "Capable small generalist balancing size and ability for everyday tasks.",
    "weakness": "Modest reasoning and knowledge depth compared to 7B models.",
    "ratings": {
      "Reasoning": 50,
      "Coding": 48,
      "Math": 52,
      "Knowledge": 48,
      "Writing": 54,
      "Instruction": 58,
      "Multilingual": 58
    }
  },
  "qwen2.5-7b": {
    "strengths": [
      "multilingual",
      "instruction-following",
      "knowledge",
      "coding"
    ],
    "summary": "Well-rounded prior-gen 7B with strong general knowledge and reliable instruction-following.",
    "weakness": "Lacks the hybrid reasoning boost of Qwen3 peers.",
    "ratings": {
      "Reasoning": 64,
      "Coding": 64,
      "Math": 66,
      "Knowledge": 66,
      "Writing": 66,
      "Instruction": 72,
      "Multilingual": 72
    }
  },
  "qwen2.5-14b": {
    "strengths": [
      "knowledge",
      "multilingual",
      "coding",
      "instruction-following"
    ],
    "summary": "Strong prior-gen large model with broad knowledge and solid coding and writing.",
    "weakness": "Older generation; reasoning trails Qwen3-14B.",
    "ratings": {
      "Reasoning": 74,
      "Coding": 76,
      "Math": 76,
      "Knowledge": 78,
      "Writing": 76,
      "Instruction": 80,
      "Multilingual": 80
    }
  },
  "gemma-3-1b-it": {
    "strengths": [
      "lightweight",
      "fast",
      "multilingual"
    ],
    "summary": "Tiny, fast Google instruct model for lightweight on-device chat and basic multilingual tasks.",
    "weakness": "Weak at hard reasoning, math, and coding due to its 1B size.",
    "ratings": {
      "Reasoning": 30,
      "Coding": 24,
      "Math": 26,
      "Knowledge": 34,
      "Writing": 48,
      "Instruction": 50,
      "Multilingual": 54
    }
  },
  "gemma-3-4b-it": {
    "strengths": [
      "multilingual",
      "creative-writing",
      "instruction-following",
      "lightweight"
    ],
    "summary": "Capable 4B Gemma 3 with strong writing and multilingual ability for its size.",
    "weakness": "Limited depth on advanced math and coding versus larger models.",
    "ratings": {
      "Reasoning": 52,
      "Coding": 44,
      "Math": 46,
      "Knowledge": 55,
      "Writing": 68,
      "Instruction": 66,
      "Multilingual": 70
    }
  },
  "gemma-3-12b-it": {
    "strengths": [
      "multilingual",
      "creative-writing",
      "knowledge",
      "instruction-following"
    ],
    "summary": "Strong 12B generalist with excellent writing and broad multilingual coverage.",
    "weakness": "Coding and rigorous math trail dedicated specialist models.",
    "ratings": {
      "Reasoning": 70,
      "Coding": 62,
      "Math": 64,
      "Knowledge": 74,
      "Writing": 84,
      "Instruction": 78,
      "Multilingual": 83
    }
  },
  "mistral-small-3.2-24b": {
    "strengths": [
      "reasoning",
      "instruction-following",
      "knowledge",
      "structured-output"
    ],
    "summary": "Top-tier local 24B generalist with strong reasoning, instruction-following, and knowledge.",
    "weakness": "Heaviest model here; needs more VRAM than the smaller options.",
    "ratings": {
      "Reasoning": 85,
      "Coding": 78,
      "Math": 80,
      "Knowledge": 86,
      "Writing": 84,
      "Instruction": 89,
      "Multilingual": 80
    }
  },
  "llama-3.2-1b": {
    "strengths": [
      "lightweight",
      "fast",
      "instruction-following"
    ],
    "summary": "Ultra-light Meta 1B for fast, simple on-device chat and edge deployment.",
    "weakness": "Struggles with reasoning, math, and coding at 1B scale.",
    "ratings": {
      "Reasoning": 28,
      "Coding": 22,
      "Math": 24,
      "Knowledge": 32,
      "Writing": 44,
      "Instruction": 48,
      "Multilingual": 40
    }
  },
  "llama-3.2-3b": {
    "strengths": [
      "lightweight",
      "fast",
      "instruction-following",
      "knowledge"
    ],
    "summary": "Solid small 3B Llama generalist with good instruction-following for its size.",
    "weakness": "Math and coding remain shallow compared to mid-size models.",
    "ratings": {
      "Reasoning": 48,
      "Coding": 40,
      "Math": 44,
      "Knowledge": 52,
      "Writing": 56,
      "Instruction": 62,
      "Multilingual": 48
    }
  },
  "llama-3.1-8b": {
    "strengths": [
      "reasoning",
      "knowledge",
      "instruction-following",
      "long-context"
    ],
    "summary": "Reliable 8B all-rounder with broad knowledge and long-context support.",
    "weakness": "No standout specialty; coding and math are decent but not leading.",
    "ratings": {
      "Reasoning": 68,
      "Coding": 58,
      "Math": 60,
      "Knowledge": 72,
      "Writing": 70,
      "Instruction": 74,
      "Multilingual": 58
    }
  },
  "phi-3.5-mini": {
    "strengths": [
      "reasoning",
      "math",
      "lightweight",
      "fast"
    ],
    "summary": "Compact 3.8B that punches above its size on reasoning and math.",
    "weakness": "Narrow world knowledge and weaker multilingual/creative writing.",
    "ratings": {
      "Reasoning": 66,
      "Coding": 56,
      "Math": 70,
      "Knowledge": 54,
      "Writing": 54,
      "Instruction": 62,
      "Multilingual": 44
    }
  },
  "mistral-7b-v0.3": {
    "strengths": [
      "instruction-following",
      "fast",
      "knowledge",
      "structured-output"
    ],
    "summary": "Classic dependable 7B generalist, fast and easy to fine-tune.",
    "weakness": "Older architecture; reasoning and math lag newer 7-8B models.",
    "ratings": {
      "Reasoning": 56,
      "Coding": 50,
      "Math": 52,
      "Knowledge": 62,
      "Writing": 62,
      "Instruction": 64,
      "Multilingual": 50
    }
  },
  "qwen2.5-coder-7b": {
    "strengths": [
      "coding",
      "fast",
      "structured-output"
    ],
    "summary": "Best-in-class 7B coder with strong fill-in-the-middle and code completion.",
    "weakness": "Limited general knowledge, writing, and broad reasoning outside code.",
    "ratings": {
      "Reasoning": 62,
      "Coding": 83,
      "Math": 64,
      "Knowledge": 56,
      "Writing": 54,
      "Instruction": 68,
      "Multilingual": 56
    }
  },
  "qwen3-coder-30b-a3b": {
    "strengths": [
      "coding",
      "agentic",
      "long-context"
    ],
    "summary": "30B MoE coder excelling at agentic coding and long-context software tasks.",
    "weakness": "Knowledge and creative writing trail dedicated generalists.",
    "ratings": {
      "Reasoning": 78,
      "Coding": 90,
      "Math": 74,
      "Knowledge": 70,
      "Writing": 64,
      "Instruction": 80,
      "Multilingual": 68
    }
  },
  "deepseek-r1-distill-qwen-7b": {
    "strengths": [
      "reasoning",
      "math"
    ],
    "summary": "R1 reasoning distilled into 7B Qwen for strong step-by-step math.",
    "weakness": "Weak knowledge breadth and writing; verbose chain-of-thought.",
    "ratings": {
      "Reasoning": 76,
      "Coding": 58,
      "Math": 80,
      "Knowledge": 50,
      "Writing": 46,
      "Instruction": 58,
      "Multilingual": 52
    }
  },
  "deepseek-r1-distill-qwen-14b": {
    "strengths": [
      "reasoning",
      "math"
    ],
    "summary": "14B R1 distill with very strong math and multi-step reasoning.",
    "weakness": "Knowledge and writing lag behind generalist peers.",
    "ratings": {
      "Reasoning": 84,
      "Coding": 66,
      "Math": 87,
      "Knowledge": 58,
      "Writing": 52,
      "Instruction": 64,
      "Multilingual": 58
    }
  },
  "qwq-32b": {
    "strengths": [
      "reasoning",
      "math"
    ],
    "summary": "Top-tier local reasoning model with frontier-like math and logic.",
    "weakness": "Slow due to long reasoning traces; weaker plain writing.",
    "ratings": {
      "Reasoning": 91,
      "Coding": 76,
      "Math": 91,
      "Knowledge": 70,
      "Writing": 60,
      "Instruction": 74,
      "Multilingual": 66
    }
  },
  "qwen2.5-vl-7b": {
    "strengths": [
      "ocr",
      "vision",
      "structured-output",
      "multilingual"
    ],
    "summary": "Best-in-class local VLM for OCR, documents, charts, and structured data extraction.",
    "weakness": "General creative writing and broad world knowledge lag dedicated text LLMs.",
    "ratings": {
      "Reasoning": 70,
      "Vision": 85,
      "OCR": 90,
      "Knowledge": 68,
      "Writing": 66,
      "Multilingual": 78
    }
  },
  "gemma-3-12b-vision": {
    "strengths": [
      "vision",
      "knowledge",
      "reasoning",
      "multilingual"
    ],
    "summary": "Strongest general-purpose local VLM for broad image understanding and reasoning.",
    "weakness": "OCR on dense documents/charts trails the Qwen2.5-VL specialist.",
    "ratings": {
      "Reasoning": 78,
      "Vision": 83,
      "OCR": 72,
      "Knowledge": 78,
      "Writing": 78,
      "Multilingual": 80
    }
  },
  "gemma-3-4b-vision": {
    "strengths": [
      "vision",
      "multilingual",
      "fast",
      "instruction-following"
    ],
    "summary": "Capable compact general VLM with solid all-around image understanding.",
    "weakness": "Weaker OCR and reasoning than its 12B sibling and the Qwen specialist.",
    "ratings": {
      "Reasoning": 64,
      "Vision": 72,
      "OCR": 60,
      "Knowledge": 66,
      "Writing": 68,
      "Multilingual": 72
    }
  },
  "smolvlm-instruct": {
    "strengths": [
      "lightweight",
      "fast",
      "vision"
    ],
    "summary": "Ultra-light VLM for basic captioning and image Q&A on constrained hardware.",
    "weakness": "Limited OCR, reasoning, and knowledge depth versus larger VLMs.",
    "ratings": {
      "Reasoning": 40,
      "Vision": 56,
      "OCR": 44,
      "Knowledge": 42,
      "Writing": 46,
      "Multilingual": 38
    }
  },
  "nomic-embed-text-v1.5": {
    "strengths": [
      "retrieval",
      "long-context",
      "lightweight",
      "fast"
    ],
    "summary": "Compact long-context English retriever with strong quality-per-parameter.",
    "weakness": "Limited multilingual coverage; trails larger models on absolute retrieval quality.",
    "ratings": {
      "Retrieval": 76,
      "Multilingual": 40,
      "Efficiency": 88
    }
  },
  "bge-large-en-v1.5": {
    "strengths": [
      "retrieval",
      "knowledge"
    ],
    "summary": "Top-tier English retrieval quality on MTEB for local embedding.",
    "weakness": "English-only and larger/slower, with no real multilingual support.",
    "ratings": {
      "Retrieval": 85,
      "Multilingual": 18,
      "Efficiency": 58
    }
  },
  "qwen3-embedding-0.6b": {
    "strengths": [
      "retrieval",
      "multilingual",
      "long-context"
    ],
    "summary": "Strong multilingual retriever balancing quality and broad language coverage.",
    "weakness": "Largest of the three, so slower and heavier than the compact alternatives.",
    "ratings": {
      "Retrieval": 83,
      "Multilingual": 86,
      "Efficiency": 55
    }
  },
  "sdxl-turbo": {
    "strengths": [
      "fast",
      "image-quality",
      "lightweight"
    ],
    "summary": "Distilled SDXL that produces solid 1024px images in just 1-4 steps, ideal for real-time/interactive generation.",
    "weakness": "Lower peak fidelity and weaker prompt adherence than full SDXL; limited at high step counts and complex compositions.",
    "ratings": {
      "Image quality": 78,
      "Prompt adherence": 68,
      "Speed": 92,
      "Versatility": 70
    }
  },
  "sdxl-base": {
    "strengths": [
      "image-quality",
      "prompt-adherence",
      "photorealism",
      "creative-writing"
    ],
    "summary": "Full SDXL delivering the best image quality, detail, and prompt adherence of the three at native 1024px.",
    "weakness": "Slow (needs ~25-40 steps) and heavier on VRAM, making it impractical for real-time use on light hardware.",
    "ratings": {
      "Image quality": 88,
      "Prompt adherence": 82,
      "Speed": 55,
      "Versatility": 84
    }
  },
  "sd-1.5": {
    "strengths": [
      "fast",
      "lightweight",
      "creative-writing"
    ],
    "summary": "Lightweight, fast 512px legacy model with a massive LoRA/ControlNet ecosystem for endless customization.",
    "weakness": "Lower native image quality and weaker prompt adherence; struggles with anatomy and complex multi-subject prompts.",
    "ratings": {
      "Image quality": 62,
      "Prompt adherence": 58,
      "Speed": 82,
      "Versatility": 80
    }
  },
  "whisper-large-v3-turbo": {
    "strengths": [
      "transcription",
      "multilingual",
      "fast"
    ],
    "summary": "Near-large-v3 accuracy across 99 languages at much faster speed.",
    "weakness": "809M footprint is heavier than smaller Whisper variants for constrained devices.",
    "ratings": {
      "Accuracy": 90,
      "Multilingual": 90,
      "Speed": 80,
      "Robustness": 85
    }
  },
  "whisper-small": {
    "strengths": [
      "transcription",
      "multilingual",
      "fast",
      "lightweight"
    ],
    "summary": "Balanced multilingual transcription with good accuracy at modest size.",
    "weakness": "Noticeably less accurate and robust on noisy or accented audio than turbo/large.",
    "ratings": {
      "Accuracy": 72,
      "Multilingual": 70,
      "Speed": 85,
      "Robustness": 65
    }
  },
  "whisper-base-en": {
    "strengths": [
      "transcription",
      "lightweight",
      "fast"
    ],
    "summary": "Tiny English-only model for very fast, low-footprint transcription.",
    "weakness": "Low accuracy on hard audio and no multilingual support at all.",
    "ratings": {
      "Accuracy": 52,
      "Multilingual": 8,
      "Speed": 95,
      "Robustness": 45
    }
  },
  "piper-en-us": {
    "strengths": [
      "fast",
      "lightweight",
      "voice-quality"
    ],
    "summary": "Fast, lightweight CPU TTS producing clear, intelligible English speech.",
    "weakness": "Single English voice with limited expressiveness vs large neural TTS.",
    "ratings": {
      "Voice quality": 72,
      "Naturalness": 65,
      "Speed": 92,
      "Languages": 30
    }
  }
};

// Controlled strength vocabulary actually used across the catalog.
export const TAG_LIST: string[] = ["agentic", "coding", "creative-writing", "fast", "instruction-following", "knowledge", "lightweight", "long-context", "math", "multilingual", "ocr", "prompt-adherence", "photorealism", "reasoning", "retrieval", "structured-output", "transcription", "vision", "voice-quality", "image-quality"];

// Capability dimensions shown as rating bars, per modality group. LLM-like
// modalities share one set so they're comparable.
const DIMS: Record<string, string[]> = {
  llm: ["Reasoning", "Coding", "Math", "Knowledge", "Writing", "Instruction", "Multilingual"],
  vision: ["Reasoning", "Vision", "OCR", "Knowledge", "Writing", "Multilingual"],
  embedding: ["Retrieval", "Multilingual", "Efficiency"],
  image: ["Image quality", "Prompt adherence", "Speed", "Versatility"],
  video: ["Image quality", "Prompt adherence", "Speed", "Versatility"],
  "audio-stt": ["Accuracy", "Multilingual", "Speed", "Robustness"],
  tts: ["Voice quality", "Naturalness", "Speed", "Languages"],
};
const DIM_SET: Record<string, string> = {
  text: "llm", code: "llm", reasoning: "llm", vision: "vision",
  embedding: "embedding", image: "image", video: "video",
  "audio-stt": "audio-stt", tts: "tts",
};
export function dimsFor(mod: string | undefined): string[] {
  return DIMS[DIM_SET[mod ?? "llm"] ?? "llm"];
}

// Category -> icon name (all present in icons.ts).
export const CAT_ICON: Record<string, string> = {
  Reasoning: "idea", Coding: "code", Math: "sigma", Knowledge: "book", Writing: "pen",
  Instruction: "list", Multilingual: "globe", Vision: "eye", OCR: "scan",
  Retrieval: "search", Efficiency: "zap",
  "Image quality": "image", "Prompt adherence": "target", Speed: "gauge", Versatility: "shapes",
  Accuracy: "target", Robustness: "shield", "Voice quality": "speaker", Naturalness: "wave", Languages: "globe",
};
export const catIcon = (label: string): string => (CAT_ICON[label] && ICONS[CAT_ICON[label]] ? CAT_ICON[label] : "");

// Plain-language explanations shown as tooltips. Keys are kind-prefixed
// (mod:/cat:/tag:/perf:) to avoid collisions.
export const GLOSSARY: Record<string, string> = {
  // Modalities — note the explicit understand-vs-generate distinction.
  "mod:text": "General conversation, writing, Q&A and reasoning — the everyday all-rounder.",
  "mod:code": "Specialized for programming: code completion, fill-in-the-middle, refactoring and debugging.",
  "mod:reasoning": "Thinks step-by-step before answering (visible chain-of-thought). Best for math and logic — but slower and more verbose.",
  "mod:vision": "UNDERSTANDS images you send it — describe a photo, analyze a chart, read a document. It does NOT create images.",
  "mod:embedding": "Turns text into vectors for search, RAG and similarity. It is not a chatbot — it returns numbers, not replies.",
  "mod:image": "GENERATES images from a text prompt. It cannot view or understand images you upload.",
  "mod:audio-stt": "Transcribes spoken audio into text (Whisper).",
  "mod:tts": "Speaks text aloud as audio (Piper). Runs on CPU only here.",
  // Capability categories (the progress bars).
  "cat:Reasoning": "Multi-step logic, breaking problems down, and drawing sound conclusions.",
  "cat:Coding": "Writing, completing and debugging code across programming languages.",
  "cat:Math": "Arithmetic, algebra, word problems and quantitative reasoning.",
  "cat:Knowledge": "Breadth of world facts it can recall without looking anything up.",
  "cat:Writing": "Fluent, well-structured prose: essays, emails, summaries and stories.",
  "cat:Instruction": "How reliably it follows your directions and the output format you ask for.",
  "cat:Multilingual": "Quality in languages other than English.",
  "cat:Vision": "Understanding the visual content of images — scenes, objects, charts, layout.",
  "cat:OCR": "Reading text that appears inside images: documents, screenshots, receipts, handwriting.",
  "cat:Retrieval": "How well its embeddings rank the right results for search and RAG.",
  "cat:Efficiency": "Speed and lightness for its quality — a bigger bar means leaner on resources.",
  "cat:Image quality": "Sharpness, coherence and overall aesthetics of generated images.",
  "cat:Prompt adherence": "How faithfully the generated image matches what you asked for.",
  "cat:Speed": "How quickly it produces a result.",
  "cat:Versatility": "The range of styles, subjects and tasks it handles well.",
  "cat:Accuracy": "Transcription correctness — a low word-error rate.",
  "cat:Robustness": "Holding up on noisy audio, strong accents and background crosstalk.",
  "cat:Voice quality": "Clarity and pleasantness of the synthesized voice.",
  "cat:Naturalness": "How human (vs robotic) the speech sounds.",
  "cat:Languages": "How many languages and voices it can speak.",
  // Key-strength tags.
  "tag:general-purpose": "A well-rounded all-rounder with no major weak spot — a safe default pick.",
  "tag:agentic": "Strong at tool use and multi-step, autonomous workflows.",
  "tag:coding": "A strong programming model.",
  "tag:creative-writing": "Shines at stories, marketing copy and expressive prose.",
  "tag:fast": "Quick responses for its quality tier.",
  "tag:instruction-following": "Sticks closely to your directions and requested format.",
  "tag:knowledge": "Broad general knowledge.",
  "tag:lightweight": "Tiny footprint — runs comfortably on modest hardware.",
  "tag:long-context": "Handles very long inputs (a large context window).",
  "tag:math": "Strong quantitative and symbolic reasoning.",
  "tag:multilingual": "Works well across many languages.",
  "tag:ocr": "Reads text out of images well.",
  "tag:prompt-adherence": "Image output matches the prompt closely.",
  "tag:photorealism": "Produces realistic, photographic-looking images.",
  "tag:reasoning": "Strong step-by-step problem solving.",
  "tag:retrieval": "High-quality embeddings for search and RAG.",
  "tag:structured-output": "Reliable at JSON and other structured formats.",
  "tag:transcription": "Accurate speech-to-text.",
  "tag:vision": "Understands images you send it.",
  "tag:voice-quality": "Clear, pleasant synthesized speech.",
  "tag:image-quality": "High visual quality of generated images.",
  // Performance levels (appended to the CPU/GPU chip tooltips).
  "perf:excellent": "Runs great on this hardware.",
  "perf:good": "Runs well.",
  "perf:fair": "Usable, but noticeably slower.",
  "perf:warning": "Slow and memory-tight — expect to wait.",
  "perf:poor": "Very slow on this hardware.",
  "perf:impossible": "Won't fit in your memory — it can't run here.",
  "perf:na": "Not applicable for this model.",
};

export const tip = (key: string): string => GLOSSARY[key] ?? "";

// Native context window per family (prefix match, longest first). Display-only;
// the image actually serves at the chosen ctx tier.
const CONTEXT: [string, string][] = [
  ["qwen3-coder", "256K"], ["qwen3-embedding", "32K"], ["qwen3", "32K–128K"],
  ["qwen2.5-coder", "32K–128K"], ["qwen2.5-vl", "32K–128K"], ["qwen2.5", "32K–128K"],
  ["gemma-3", "128K"], ["llama-3.2", "128K"], ["llama-3.1", "128K"],
  ["mistral-small", "128K"], ["mistral-7b", "32K"], ["phi-3.5", "128K"],
  ["deepseek-r1", "128K"], ["qwq", "32K–128K"], ["nomic-embed", "8K"], ["bge-large", "512"],
];
export function contextFor(id: string): string {
  const f = CONTEXT.find(([p]) => id.startsWith(p));
  return f ? f[1] : "";
}

// Output/visual specs for the modalities the catalog can't express in size alone:
// image gen resolution, video resolution + clip length, vision input handling.
export interface VisualSpec { res?: string; length?: string; note?: string }
const VISUAL: Record<string, VisualSpec> = {
  // image gen
  "sdxl-turbo": { res: "512×512", note: "1–4 step turbo" },
  "sdxl-base": { res: "1024×1024" },
  "sd-1.5": { res: "512×512" },
  // video gen (stable-diffusion.cpp vid_gen)
  "wan2.1-t2v-1.3b": { res: "480p · 832×480", length: "~2–5s (default 33 frames)" },
  "wan2.2-ti2v-5b": { res: "up to 720p", length: "~3–5s clip" },
  "wan2.2-t2v-a14b": { res: "480p / 720p", length: "~3–5s clip" },
  // vision (input understanding, not generation)
  "qwen2.5-vl-7b": { note: "Understands input images up to ~3.5M px (dynamic)" },
  "gemma-3-12b-vision": { note: "Understands input images at 896×896" },
  "gemma-3-4b-vision": { note: "Understands input images at 896×896" },
  "smolvlm-instruct": { note: "Understands input images at 384–512px" },
};
const VISUAL_BY_MOD: Record<string, VisualSpec> = {
  image: { res: "512–1024px (model-dependent)" },
  video: { res: "480–720p", length: "few-second clip" },
  vision: { note: "Understands images you send it — does not generate them" },
};
export function visualSpec(id: string, mod: string | undefined): VisualSpec | null {
  if (mod !== "image" && mod !== "video" && mod !== "vision") return null;
  return VISUAL[id] ?? VISUAL_BY_MOD[mod] ?? null;
}

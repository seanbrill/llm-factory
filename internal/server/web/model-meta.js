// Per-model capability ratings + key-strength tags for the build picker.
// GENERATED — calibrated within the world of local open models (0-100, rough guidance).
// Consumed by app.js (window.MODEL_META). Safe to hand-edit; UI-only, hot-reloads in dev.
window.MODEL_META = {
  "models": [
    {
      "id": "qwen3-0.6b",
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
    {
      "id": "qwen3-4b",
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
    {
      "id": "qwen3-8b",
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
    {
      "id": "qwen3-14b",
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
    {
      "id": "qwen2.5-1.5b",
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
    {
      "id": "qwen2.5-3b",
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
    {
      "id": "qwen2.5-7b",
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
    {
      "id": "qwen2.5-14b",
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
    {
      "id": "gemma-3-1b-it",
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
    {
      "id": "gemma-3-4b-it",
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
    {
      "id": "gemma-3-12b-it",
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
    {
      "id": "mistral-small-3.2-24b",
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
    {
      "id": "llama-3.2-1b",
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
    {
      "id": "llama-3.2-3b",
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
    {
      "id": "llama-3.1-8b",
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
    {
      "id": "phi-3.5-mini",
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
    {
      "id": "mistral-7b-v0.3",
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
    {
      "id": "qwen2.5-coder-7b",
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
    {
      "id": "qwen3-coder-30b-a3b",
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
    {
      "id": "deepseek-r1-distill-qwen-7b",
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
    {
      "id": "deepseek-r1-distill-qwen-14b",
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
    {
      "id": "qwq-32b",
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
    {
      "id": "qwen2.5-vl-7b",
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
    {
      "id": "gemma-3-12b-vision",
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
    {
      "id": "gemma-3-4b-vision",
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
    {
      "id": "smolvlm-instruct",
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
    {
      "id": "nomic-embed-text-v1.5",
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
    {
      "id": "bge-large-en-v1.5",
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
    {
      "id": "qwen3-embedding-0.6b",
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
    {
      "id": "sdxl-turbo",
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
    {
      "id": "sdxl-base",
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
    {
      "id": "sd-1.5",
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
    {
      "id": "whisper-large-v3-turbo",
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
    {
      "id": "whisper-small",
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
    {
      "id": "whisper-base-en",
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
    {
      "id": "piper-en-us",
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
  ],
  "tagList": [
    "agentic",
    "coding",
    "creative-writing",
    "fast",
    "instruction-following",
    "knowledge",
    "lightweight",
    "long-context",
    "math",
    "multilingual",
    "ocr",
    "prompt-adherence",
    "photorealism",
    "reasoning",
    "retrieval",
    "structured-output",
    "transcription",
    "vision",
    "voice-quality",
    "image-quality"
  ]
};

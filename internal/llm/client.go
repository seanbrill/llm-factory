// Package llm is a thin, dependency-free client for the llama.cpp server's
// OpenAI-compatible /v1/chat/completions endpoint.
//
// Only the standard library is used (net/http + encoding/json); the wire format
// is a small, stable subset of the OpenAI Chat Completions schema, so no SDK is
// needed. Chat() is the generic entry point used by the builder UI's test
// panel; AnalyzeSignal() is a typed example layered on top of it.
package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"
)

// ---------------------------------------------------------------------------
// Client
// ---------------------------------------------------------------------------

// Message is a single chat turn.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatOptions tunes a single completion.
type ChatOptions struct {
	Temperature float64
	MaxTokens   int
	JSONObject  bool // force valid-JSON output (llama.cpp constrained decoding)
}

// Client is safe for concurrent use; http.Client pools connections internally.
type Client struct {
	baseURL string
	model   string
	http    *http.Client
}

// NewClient builds a client. baseURL is the llama-server root, e.g.
// "http://127.0.0.1:8080". requestTimeout is the hard ceiling for one
// inference — CPU generation is slow, so 60-120s is reasonable.
func NewClient(baseURL, model string, requestTimeout time.Duration) *Client {
	if model == "" {
		// llama.cpp ignores the name when one model is loaded, but the OpenAI
		// schema requires the field.
		model = "local-model"
	}
	return &Client{
		baseURL: strings.TrimRight(baseURL, "/"),
		model:   model,
		http: &http.Client{
			Timeout: requestTimeout,
			Transport: &http.Transport{
				MaxIdleConns:        10,
				MaxIdleConnsPerHost: 10,
				IdleConnTimeout:     90 * time.Second,
			},
		},
	}
}

// ---------------------------------------------------------------------------
// Wire types (the subset llama.cpp implements)
// ---------------------------------------------------------------------------

type responseFormat struct {
	Type string `json:"type"` // "json_object"
}

type chatRequest struct {
	Model          string          `json:"model"`
	Messages       []Message       `json:"messages"`
	Temperature    float64         `json:"temperature"`
	MaxTokens      int             `json:"max_tokens"`
	ResponseFormat *responseFormat `json:"response_format,omitempty"`
}

type chatResponse struct {
	Choices []struct {
		Message      Message `json:"message"`
		FinishReason string  `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error,omitempty"`
}

// Chat runs one completion and returns the assistant's raw text content.
//
// Pass a context with its own deadline (e.g. a queue job's deadline); it is
// honored alongside the client Timeout and lets a worker cancel in-flight work.
func (c *Client) Chat(ctx context.Context, messages []Message, opts ChatOptions) (string, error) {
	if opts.MaxTokens == 0 {
		opts.MaxTokens = 512
	}
	reqBody := chatRequest{
		Model:       c.model,
		Messages:    messages,
		Temperature: opts.Temperature,
		MaxTokens:   opts.MaxTokens,
	}
	if opts.JSONObject {
		reqBody.ResponseFormat = &responseFormat{Type: "json_object"}
	}

	payload, err := json.Marshal(reqBody)
	if err != nil {
		return "", fmt.Errorf("marshal request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.baseURL+"/v1/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := c.http.Do(req)
	if err != nil {
		// Covers dial failures, the Timeout firing, and ctx cancellation.
		return "", fmt.Errorf("call llm: %w", err)
	}
	defer resp.Body.Close()

	var parsed chatResponse
	if err := json.NewDecoder(resp.Body).Decode(&parsed); err != nil {
		return "", fmt.Errorf("decode response (status %d): %w", resp.StatusCode, err)
	}
	if resp.StatusCode != http.StatusOK {
		if parsed.Error != nil {
			return "", fmt.Errorf("llm error (status %d): %s", resp.StatusCode, parsed.Error.Message)
		}
		return "", fmt.Errorf("llm returned status %d", resp.StatusCode)
	}
	if len(parsed.Choices) == 0 {
		return "", fmt.Errorf("llm returned no choices")
	}
	return strings.TrimSpace(parsed.Choices[0].Message.Content), nil
}

// ---------------------------------------------------------------------------
// Typed example: structured financial-signal analysis.
//
// This is how a queue worker would call the model for a real task — ask for a
// strict JSON schema, then decode it into a domain struct.
// ---------------------------------------------------------------------------

type Sentiment string

const (
	SentimentBullish Sentiment = "bullish"
	SentimentBearish Sentiment = "bearish"
	SentimentNeutral Sentiment = "neutral"
)

// SignalAnalysis is the parsed result of one analysis call.
type SignalAnalysis struct {
	Sentiment  Sentiment `json:"sentiment"`
	Confidence float64   `json:"confidence"`
	Summary    string    `json:"summary"`
	KeyFactors []string  `json:"key_factors"`
	RiskFlags  []string  `json:"risk_flags"`
}

// Validate guards against syntactically-valid but semantically-bogus output.
func (s SignalAnalysis) Validate() error {
	switch s.Sentiment {
	case SentimentBullish, SentimentBearish, SentimentNeutral:
	default:
		return fmt.Errorf("invalid sentiment %q", s.Sentiment)
	}
	if s.Confidence < 0 || s.Confidence > 1 {
		return fmt.Errorf("confidence %.3f out of range [0,1]", s.Confidence)
	}
	return nil
}

const signalSystemPrompt = `You are a financial-signal analysis engine.
Analyze the user's news headline or trading signal and respond with ONLY a
single JSON object, no markdown, matching exactly this schema:
{
  "sentiment":   "bullish" | "bearish" | "neutral",
  "confidence":  number between 0 and 1,
  "summary":     "one concise sentence",
  "key_factors": ["short", "phrases"],
  "risk_flags":  ["short", "phrases"]
}`

// AnalyzeSignal runs one structured analysis.
func (c *Client) AnalyzeSignal(ctx context.Context, signal string) (*SignalAnalysis, error) {
	content, err := c.Chat(ctx,
		[]Message{
			{Role: "system", Content: signalSystemPrompt},
			{Role: "user", Content: signal},
		},
		ChatOptions{Temperature: 0.2, MaxTokens: 512, JSONObject: true},
	)
	if err != nil {
		return nil, err
	}

	var out SignalAnalysis
	if err := json.Unmarshal([]byte(content), &out); err != nil {
		return nil, fmt.Errorf("model did not return valid JSON: %w (raw: %q)", err, content)
	}
	if err := out.Validate(); err != nil {
		return nil, fmt.Errorf("model output failed validation: %w", err)
	}
	return &out, nil
}

// ---------------------------------------------------------------------------
// Queue-worker integration sketch
//
// The Go backend pulls jobs from a BullMQ queue (Redis-backed). BullMQ stores
// jobs in well-known Redis keys, so a Go worker reads them with a Redis client
// (e.g. github.com/redis/go-redis) and calls AnalyzeSignal per job:
//
//   func runWorker(ctx context.Context, client *llm.Client, jobs <-chan Job) {
//       for job := range jobs {
//           jobCtx, cancel := context.WithTimeout(ctx, 90*time.Second)
//           result, err := client.AnalyzeSignal(jobCtx, job.Signal)
//           cancel()
//           if err != nil {
//               job.Retry(err)       // re-queue / move to BullMQ failed set
//               continue
//           }
//           job.Complete(result)     // persist + return to the Next.js frontend
//       }
//   }
// ---------------------------------------------------------------------------

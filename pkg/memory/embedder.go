package memory

import (
	"context"
	"fmt"
	"sync/atomic"

	sdkopenai "github.com/openai/openai-go"
	"github.com/openai/openai-go/option"
)

// EmbedderConfig configures the remote embedder. It mirrors the LLM provider
// config so the same API key / base URL can be reused; Model is the embedding
// model (e.g. text-embedding-3-small), independent of the chat model.
type EmbedderConfig struct {
	APIKey  string
	BaseURL string
	Model   string
}

const defaultEmbedModel = "text-embedding-3-small"

// OpenAIEmbedder embeds text via an OpenAI-compatible embeddings API using the
// official openai-go SDK — no local model, so the no-CGO/single-binary constraint
// holds. Batch requests embed many texts in one call.
type OpenAIEmbedder struct {
	client sdkopenai.Client
	model  string
	dims   atomic.Int64 // learned from the first response (embedding length)
}

var _ Embedder = (*OpenAIEmbedder)(nil)

// NewOpenAIEmbedder builds an embedder from config.
func NewOpenAIEmbedder(cfg EmbedderConfig) *OpenAIEmbedder {
	var opts []option.RequestOption
	if cfg.APIKey != "" {
		opts = append(opts, option.WithAPIKey(cfg.APIKey))
	}
	if cfg.BaseURL != "" {
		opts = append(opts, option.WithBaseURL(cfg.BaseURL))
	}
	model := cfg.Model
	if model == "" {
		model = defaultEmbedModel
	}
	return &OpenAIEmbedder{client: sdkopenai.NewClient(opts...), model: model}
}

// Embed returns one vector per input text, in input order. The provider may
// return embeddings out of order, so results are placed by their Index.
func (e *OpenAIEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	resp, err := e.client.Embeddings.New(ctx, sdkopenai.EmbeddingNewParams{
		Model: sdkopenai.EmbeddingModel(e.model),
		Input: sdkopenai.EmbeddingNewParamsInputUnion{OfArrayOfStrings: texts},
	})
	if err != nil {
		return nil, fmt.Errorf("memory: embeddings request: %w", err)
	}
	if len(resp.Data) != len(texts) {
		return nil, fmt.Errorf("memory: embedder returned %d vectors for %d texts", len(resp.Data), len(texts))
	}
	out := make([][]float32, len(texts))
	for _, d := range resp.Data {
		idx := int(d.Index)
		if idx < 0 || idx >= len(out) {
			return nil, fmt.Errorf("memory: embedding index %d out of range for %d texts", idx, len(texts))
		}
		v := make([]float32, len(d.Embedding))
		for i, f := range d.Embedding {
			v[i] = float32(f)
		}
		out[idx] = v
	}
	if len(out) > 0 && len(out[0]) > 0 {
		e.dims.CompareAndSwap(0, int64(len(out[0])))
	}
	return out, nil
}

// Dimensions reports the embedding length, learned from the first Embed call
// (0 until then, since it is model-dependent and discovered from the API).
func (e *OpenAIEmbedder) Dimensions() int { return int(e.dims.Load()) }

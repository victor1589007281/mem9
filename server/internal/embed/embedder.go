package embed

import (
	"bytes"
	"container/list"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync"
	"time"
)

// Embedder generates vector embeddings from text.
type Embedder struct {
	apiKey  string
	baseURL string
	model   string
	dims    int
	client  *http.Client
	cache   *embeddingCache
}

// Config holds embedding provider configuration.
type Config struct {
	APIKey  string // OpenAI key; "local" or empty for Ollama
	BaseURL string // Override for Ollama/LM Studio (e.g., http://localhost:11434/v1)
	Model   string // Model name (default: text-embedding-3-small)
	Dims    int    // Vector dimensions (default: 1536)
}

const (
	defaultModel    = "text-embedding-3-small"
	defaultDims     = 1536
	defaultBaseURL  = "https://api.openai.com/v1"
	defaultCacheMax = 1024
)

// New creates an Embedder from config. Returns nil if not configured
// (no API key and no base URL).
func New(cfg Config) *Embedder {
	if cfg.APIKey == "" && cfg.BaseURL == "" {
		return nil
	}
	model := cfg.Model
	if model == "" {
		model = defaultModel
	}
	dims := cfg.Dims
	if dims <= 0 {
		dims = defaultDims
	}
	baseURL := cfg.BaseURL
	if baseURL == "" {
		baseURL = defaultBaseURL
	}
	apiKey := cfg.APIKey
	if apiKey == "" {
		apiKey = "local"
	}
	return &Embedder{
		apiKey:  apiKey,
		baseURL: baseURL,
		model:   model,
		dims:    dims,
		client:  &http.Client{Timeout: 30 * time.Second},
		cache:   newEmbeddingCache(defaultCacheMax),
	}
}

// Dims returns the configured vector dimensions.
func (e *Embedder) Dims() int {
	return e.dims
}

type embeddingRequest struct {
	Model          string `json:"model"`
	Input          any    `json:"input"` // string for single, []string for batch
	EncodingFormat string `json:"encoding_format"`
}

type embeddingResponse struct {
	Data []struct {
		Index     int       `json:"index"`
		Embedding []float32 `json:"embedding"`
	} `json:"data"`
}

// Embed generates a vector embedding for the given text.
// Results are cached in an LRU cache keyed by model+text.
func (e *Embedder) Embed(ctx context.Context, text string) ([]float32, error) {
	key := e.model + ":" + text
	if cached, ok := e.cache.get(key); ok {
		return cached, nil
	}

	reqBody := embeddingRequest{
		Model:          e.model,
		Input:          text,
		EncodingFormat: "float",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("embedding API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var result embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode embedding response: %w", err)
	}
	if len(result.Data) == 0 || len(result.Data[0].Embedding) == 0 {
		return nil, fmt.Errorf("empty embedding response")
	}

	vec := result.Data[0].Embedding
	e.cache.put(key, vec)
	return vec, nil
}

// EmbedBatch generates vector embeddings for multiple texts in a single API call.
// The OpenAI /v1/embeddings endpoint accepts an array of strings as input.
// Results are returned in the same order as input texts and cached individually.
func (e *Embedder) EmbedBatch(ctx context.Context, texts []string) ([][]float32, error) {
	if len(texts) == 0 {
		return nil, nil
	}
	if len(texts) == 1 {
		vec, err := e.Embed(ctx, texts[0])
		if err != nil {
			return nil, err
		}
		return [][]float32{vec}, nil
	}

	results := make([][]float32, len(texts))
	uncachedIdxs := make([]int, 0, len(texts))
	uncachedTexts := make([]string, 0, len(texts))

	for i, text := range texts {
		key := e.model + ":" + text
		if cached, ok := e.cache.get(key); ok {
			results[i] = cached
		} else {
			uncachedIdxs = append(uncachedIdxs, i)
			uncachedTexts = append(uncachedTexts, text)
		}
	}

	if len(uncachedTexts) == 0 {
		return results, nil
	}

	reqBody := embeddingRequest{
		Model:          e.model,
		Input:          uncachedTexts,
		EncodingFormat: "float",
	}
	body, err := json.Marshal(reqBody)
	if err != nil {
		return nil, fmt.Errorf("marshal batch embedding request: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, "POST", e.baseURL+"/embeddings", bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create batch embedding request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+e.apiKey)

	resp, err := e.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("batch embedding API call: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		respBody, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("batch embedding API returned %d: %s", resp.StatusCode, string(respBody))
	}

	var apiResult embeddingResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResult); err != nil {
		return nil, fmt.Errorf("decode batch embedding response: %w", err)
	}
	if len(apiResult.Data) != len(uncachedTexts) {
		return nil, fmt.Errorf("batch embedding returned %d results for %d inputs", len(apiResult.Data), len(uncachedTexts))
	}

	// OpenAI returns results in order of index, but sort by index just in case.
	indexed := make(map[int][]float32, len(apiResult.Data))
	for _, d := range apiResult.Data {
		indexed[d.Index] = d.Embedding
	}

	for i, origIdx := range uncachedIdxs {
		vec, ok := indexed[i]
		if !ok || len(vec) == 0 {
			return nil, fmt.Errorf("missing embedding at batch index %d", i)
		}
		results[origIdx] = vec
		e.cache.put(e.model+":"+uncachedTexts[i], vec)
	}

	return results, nil
}

// ---------------------------------------------------------------------------
// LRU embedding cache (goroutine-safe, bounded)
// ---------------------------------------------------------------------------

type cacheEntry struct {
	key string
	vec []float32
}

type embeddingCache struct {
	mu      sync.Mutex
	maxSize int
	items   map[string]*list.Element
	order   *list.List // front = most recent
}

func newEmbeddingCache(maxSize int) *embeddingCache {
	return &embeddingCache{
		maxSize: maxSize,
		items:   make(map[string]*list.Element, maxSize),
		order:   list.New(),
	}
}

func (c *embeddingCache) get(key string) ([]float32, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		return el.Value.(*cacheEntry).vec, true
	}
	return nil, false
}

func (c *embeddingCache) put(key string, vec []float32) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if el, ok := c.items[key]; ok {
		c.order.MoveToFront(el)
		el.Value.(*cacheEntry).vec = vec
		return
	}

	if c.order.Len() >= c.maxSize {
		oldest := c.order.Back()
		if oldest != nil {
			c.order.Remove(oldest)
			delete(c.items, oldest.Value.(*cacheEntry).key)
		}
	}

	el := c.order.PushFront(&cacheEntry{key: key, vec: vec})
	c.items[key] = el
}

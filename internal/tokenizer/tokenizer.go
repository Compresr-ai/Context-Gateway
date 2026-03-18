package tokenizer

import (
	"strings"
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

// encoderCache stores encoders by encoding name for reuse.
var (
	encoderCache   = make(map[string]*tiktoken.Tiktoken)
	encoderCacheMu sync.RWMutex
	defaultEncoder *tiktoken.Tiktoken
	defaultOnce    sync.Once
)

// getDefaultEncoder returns the default cl100k_base encoder.
func getDefaultEncoder() *tiktoken.Tiktoken {
	defaultOnce.Do(func() {
		enc, err := tiktoken.GetEncoding("cl100k_base")
		if err != nil {
			panic("tokenizer: failed to initialize cl100k_base: " + err.Error())
		}
		defaultEncoder = enc
	})
	return defaultEncoder
}

// getEncoderForModel returns the appropriate tiktoken encoder for a model.
// Uses cl100k_base for Claude/Anthropic models (close BPE approximation).
// Uses o200k_base for GPT-4o and newer OpenAI models.
// Uses cl100k_base for GPT-4, GPT-3.5, and unknown models.
func getEncoderForModel(model string) *tiktoken.Tiktoken {
	encoding := encodingForModel(model)

	// Check cache first
	encoderCacheMu.RLock()
	if enc, ok := encoderCache[encoding]; ok {
		encoderCacheMu.RUnlock()
		return enc
	}
	encoderCacheMu.RUnlock()

	// Create new encoder
	encoderCacheMu.Lock()
	defer encoderCacheMu.Unlock()

	// Double-check after acquiring write lock
	if enc, ok := encoderCache[encoding]; ok {
		return enc
	}

	enc, err := tiktoken.GetEncoding(encoding)
	if err != nil {
		// Fallback to default
		return getDefaultEncoder()
	}
	encoderCache[encoding] = enc
	return enc
}

// encodingForModel returns the tiktoken encoding name for a model.
func encodingForModel(model string) string {
	m := strings.ToLower(model)

	// OpenAI GPT-4o and newer use o200k_base
	if strings.Contains(m, "gpt-4o") || strings.Contains(m, "o1") || strings.Contains(m, "o3") {
		return "o200k_base"
	}

	// All other models (Claude, GPT-4, GPT-3.5, Gemini, etc.) use cl100k_base
	// Claude uses BPE tokenization similar to cl100k_base (~5% variance)
	return "cl100k_base"
}

// CountTokens returns the token count for a string using default encoding.
func CountTokens(text string) int {
	return len(getDefaultEncoder().Encode(text, nil, nil))
}

// CountTokensForModel returns the token count using model-specific encoding.
// This is the preferred method when the model name is known.
func CountTokensForModel(text string, model string) int {
	return len(getEncoderForModel(model).Encode(text, nil, nil))
}

// CountBytes returns the token count for raw bytes using default encoding.
func CountBytes(data []byte) int {
	return CountTokens(string(data))
}

// CountBytesForModel returns the token count for raw bytes using model-specific encoding.
// This is the preferred method when the model name is known.
func CountBytesForModel(data []byte, model string) int {
	return CountTokensForModel(string(data), model)
}

// COMPRESSION RATIO

// CompressionRatio computes the token-based compression ratio: fraction of tokens removed.
//
// Definition: removed fraction = 1 - (compressedTokens / originalTokens).
//   - 0.0 = no compression (nothing removed, or compression expanded the content)
//   - 0.5 = 50% of tokens removed (medium aggressiveness)
//   - 0.9 = 90% removed (very aggressive)
//   - 1.0 = perfect compression (everything removed)
//
// Higher value = more aggressive compression. Matches the API's target_compression_ratio convention.
// Returns 0.0 when originalTokens == 0 to avoid NaN / division-by-zero.
// Returns 0.0 (clamped) when compressedTokens > originalTokens — a compression API that returns
// more tokens than it received is a failure, not negative savings.
// All compression ratio calculations in the codebase MUST use this function.
func CompressionRatio(originalTokens, compressedTokens int) float64 {
	if originalTokens == 0 {
		return 0.0
	}
	ratio := 1.0 - float64(compressedTokens)/float64(originalTokens)
	if ratio < 0.0 {
		return 0.0
	}
	return ratio
}

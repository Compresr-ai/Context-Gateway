package tokenizer

import (
	"sync"

	tiktoken "github.com/pkoukk/tiktoken-go"
)

var (
	encoder *tiktoken.Tiktoken
	once    sync.Once
)

func getEncoder() *tiktoken.Tiktoken {
	once.Do(func() {
		enc, err := tiktoken.EncodingForModel("gpt-4")
		if err != nil {
			// Fallback to cl100k_base directly
			enc, err = tiktoken.GetEncoding("cl100k_base")
			if err != nil {
				// Should never happen with a valid encoding name
				panic("tokenizer: failed to initialize cl100k_base: " + err.Error())
			}
		}
		encoder = enc
	})
	return encoder
}

// CountTokens returns the token count for a string.
func CountTokens(text string) int {
	return len(getEncoder().Encode(text, nil, nil))
}

// CountBytes returns the token count for raw bytes.
func CountBytes(data []byte) int {
	return CountTokens(string(data))
}

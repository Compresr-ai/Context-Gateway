// Trimming compressor for testing expand_context behavior.
//
// Strategy: Keep only the tail of the tool output based on targetCompressionRatio.
// A ratio of 0.9 discards 90% of the content (head), keeping only the last 10% (tail).
//
// This is intentionally destructive — it produces incomplete output that forces
// agents to call expand_context to recover the full content. Ideal for testing
// that expand_context works correctly across all supported agent types.
package tooloutput

import (
	"fmt"

	"github.com/compresr/context-gateway/internal/tokenizer"
)

// compressTrimming keeps only the tail of the content based on the target compression ratio.
// keepRatio = 1 - targetCompressionRatio, so ratio=0.9 → keep last 10%.
// Works at the character level for speed; token count is checked by the caller.
func (p *Pipe) compressTrimming(content string) string {
	ratio := p.targetCompressionRatio
	if ratio <= 0 || ratio >= 1 {
		// Fallback: keep last 10% when ratio is out of range
		ratio = 0.9
	}

	keepRatio := 1.0 - ratio
	keepLen := int(float64(len(content)) * keepRatio)

	if keepLen <= 0 {
		keepLen = 1
	}
	if keepLen >= len(content) {
		return content
	}

	tail := content[len(content)-keepLen:]
	origTokens := tokenizer.CountTokens(content)
	tailTokens := tokenizer.CountTokens(tail)

	header := fmt.Sprintf("[TRIMMED — showing last %d%% of content (%d/%d tokens). Call expand_context to see full output.]\n",
		int(keepRatio*100), tailTokens, origTokens)
	return header + tail
}

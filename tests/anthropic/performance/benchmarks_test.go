// Benchmark Tests
//
// Performance benchmarks for the compression pipeline.
package performance

import (
	"strings"
	"testing"

	"github.com/compresr/context-gateway/internal/config"
	tooloutput "github.com/compresr/context-gateway/internal/pipes/tool_output"
	"github.com/compresr/context-gateway/tests/anthropic/fixtures"
)

func BenchmarkPassthrough_SmallContent(b *testing.B) {
	pipe := tooloutput.New(fixtures.PassthroughConfig(), fixtures.TestStore())
	reqBody := fixtures.RequestWithSmallToolOutput()

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := fixtures.TestPipeContext(reqBody)
		_, _ = pipe.Process(ctx)
	}
}

func BenchmarkPassthrough_LargeContent(b *testing.B) {
	pipe := tooloutput.New(fixtures.PassthroughConfig(), fixtures.TestStore())
	reqBody := fixtures.RequestWithLargeToolOutput(10000)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := fixtures.TestPipeContext(reqBody)
		_, _ = pipe.Process(ctx)
	}
}

func BenchmarkExtraction_OpenAI(b *testing.B) {
	content := strings.Repeat("content ", 1000)
	reqBody := fixtures.RequestWithSingleToolOutput(content)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := fixtures.TestPipeContext(reqBody)
		_, _ = ctx.Adapter.ExtractToolOutput(reqBody)
	}
}

func BenchmarkExtraction_Anthropic(b *testing.B) {
	content := strings.Repeat("content ", 1000)
	reqBody := fixtures.AnthropicSingleToolOutputRequest(content)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := fixtures.TestPipeContextAnthropic(reqBody)
		_, _ = ctx.Adapter.ExtractToolOutput(reqBody)
	}
}

func BenchmarkPipeline_WithCompression(b *testing.B) {
	cfg := fixtures.TestConfig(config.StrategyCompresr, 50, true)
	cfg.Pipes.ToolOutput.Compresr.Endpoint = "http://localhost:9999/compress" // Will fail fast
	cfg.Pipes.ToolOutput.Compresr.Timeout = 1                                 // Very short timeout

	pipe := tooloutput.New(cfg, fixtures.TestStore())
	content := strings.Repeat("content ", 500)
	reqBody := fixtures.RequestWithSingleToolOutput(content)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := fixtures.TestPipeContext(reqBody)
		_, _ = pipe.Process(ctx)
	}
}

func BenchmarkMultipleToolOutputs(b *testing.B) {
	pipe := tooloutput.New(fixtures.PassthroughConfig(), fixtures.TestStore())

	content1 := strings.Repeat("content1 ", 100)
	content2 := strings.Repeat("content2 ", 100)
	content3 := strings.Repeat("content3 ", 100)
	reqBody := fixtures.MultiToolOutputRequest(content1, content2, content3)

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		ctx := fixtures.TestPipeContext(reqBody)
		_, _ = pipe.Process(ctx)
	}
}

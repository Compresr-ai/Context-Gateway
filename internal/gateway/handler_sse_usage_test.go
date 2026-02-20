package gateway

import "testing"

func TestSSEUsageParser_SplitChunksAndEscapedTokenKeys(t *testing.T) {
	stream := "" +
		"event: message_start\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":10000,"cache_creation_input_tokens":1000,"cache_read_input_tokens":7000}}}` + "\n\n" +
		"event: content_block_delta\n" +
		`data: {"type":"content_block_delta","delta":{"type":"text_delta","text":"{\"output_tokens\":999999,\"input_tokens\":888888}"}}` + "\n\n" +
		"event: message_delta\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":250}}` + "\n\n"

	parser := newSSEUsageParser()
	streamBytes := []byte(stream)
	for i := 0; i < len(streamBytes); i += 13 {
		end := i + 13
		if end > len(streamBytes) {
			end = len(streamBytes)
		}
		parser.Feed(streamBytes[i:end])
	}

	usage := parser.Usage()
	if usage.InputTokens != 10000 {
		t.Fatalf("InputTokens = %d, want 10000", usage.InputTokens)
	}
	if usage.OutputTokens != 250 {
		t.Fatalf("OutputTokens = %d, want 250", usage.OutputTokens)
	}
	if usage.CacheCreationInputTokens != 1000 {
		t.Fatalf("CacheCreationInputTokens = %d, want 1000", usage.CacheCreationInputTokens)
	}
	if usage.CacheReadInputTokens != 7000 {
		t.Fatalf("CacheReadInputTokens = %d, want 7000", usage.CacheReadInputTokens)
	}
	if usage.TotalTokens != 18250 {
		t.Fatalf("TotalTokens = %d, want 18250", usage.TotalTokens)
	}
}

func TestSSEUsageParser_CRLFAndFlushTrailingEvent(t *testing.T) {
	stream := "" +
		"event: message_start\r\n" +
		`data: {"type":"message_start","message":{"usage":{"input_tokens":42}}}` + "\r\n\r\n" +
		"event: message_delta\r\n" +
		`data: {"type":"message_delta","usage":{"output_tokens":9}}`

	parser := newSSEUsageParser()
	parser.Feed([]byte(stream))
	usage := parser.Usage()

	if usage.InputTokens != 42 {
		t.Fatalf("InputTokens = %d, want 42", usage.InputTokens)
	}
	if usage.OutputTokens != 9 {
		t.Fatalf("OutputTokens = %d, want 9", usage.OutputTokens)
	}
	if usage.TotalTokens != 51 {
		t.Fatalf("TotalTokens = %d, want 51", usage.TotalTokens)
	}
}
